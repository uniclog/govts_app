package server

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"uniclog.io/sonoryx/apps/server/internal/media"
	"uniclog.io/sonoryx/apps/server/internal/persist"
	"uniclog.io/sonoryx/apps/server/internal/voice"
	"uniclog.io/sonoryx/internal/appversion"
	"uniclog.io/sonoryx/internal/identity"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

type RunConfig struct {
	Version           string
	BootstrapPath     string
	DatabasePath      string
	VoiceIdentityPath string
	VoicePort         int
	MediaPort         int
	MediaMinPort      int
	MediaMaxPort      int
	MediaAdvertisedIP string
	MediaIdentity     string
	MediaAdaptive     bool
	VoiceRedundancy   bool
	PublicStatus      bool
	MaxSessions       int
	VoiceReserves     VoiceReserveConfig
	Files             FileServerConfig
}

type VoiceReserveConfig struct {
	Uplink                                                         bool
	RecipientKiB, GlobalKiB, ControlRecipientKiB, ControlGlobalKiB int
}

func (c VoiceReserveConfig) validate() error {
	for _, value := range []int{c.RecipientKiB, c.GlobalKiB, c.ControlRecipientKiB, c.ControlGlobalKiB} {
		if value < 1 || value > 1048576 {
			return errors.New("voice reserve must be 1..1048576 KiB/s")
		}
	}
	return nil
}

func Run(ctx context.Context, config RunConfig) (runErr error) {
	if err := config.VoiceReserves.validate(); err != nil {
		return err
	}
	if config.MaxSessions < 1 {
		return errors.New("max-sessions must be positive")
	}
	currentVersion, err := appversion.Parse(config.Version)
	if err != nil {
		return fmt.Errorf("invalid embedded server version: %w", err)
	}
	if config.VoicePort < 1 || config.VoicePort > 65535 {
		return fmt.Errorf("invalid UDP port %d: must be 1..65535", config.VoicePort)
	}
	mediaPort := config.MediaPort
	if mediaPort == -1 {
		mediaPort = config.VoicePort + 2
	}
	startedAt := time.Now()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var source BootstrapSource = BuiltinBootstrapSource{}
	configSource := "builtin"
	if config.BootstrapPath != "" {
		source = JSONBootstrapSource{Path: config.BootstrapPath}
		configSource = config.BootstrapPath
	}
	// Admission policy is read on every start, independently of the channel
	// bootstrap stored in SQLite.
	definition, err := source.Load(ctx)
	if err != nil {
		return fmt.Errorf("load server admission config: %w", err)
	}
	store, err := persist.Open(config.DatabasePath)
	if err != nil {
		return fmt.Errorf("open server database: %w", err)
	}
	defer store.Close()
	hub, err := BootstrapPersistentHub(ctx, store, source)
	if err != nil {
		return fmt.Errorf("bootstrap server channels: %w", err)
	}
	hub.SetServerVersion(currentVersion)
	hub.SetVoiceBundles(config.VoiceRedundancy)
	hub.SetVoiceUplink(config.VoiceReserves.Uplink)
	hub.SetVoiceReserves(config.VoiceReserves.RecipientKiB*1024, config.VoiceReserves.GlobalKiB*1024)
	hub.SetVoiceControlRates(config.VoiceReserves.ControlRecipientKiB*1024, config.VoiceReserves.ControlGlobalKiB*1024)

	hub.SetPublicStatus(config.PublicStatus)
	hub.SetMaxSessions(config.MaxSessions)
	policyGate := &sync.Mutex{}
	activation := make(chan struct{})
	fileConfig := config.Files
	fileConfig.activation = activation
	stopFiles, err := startFileServer(ctx, cancel, store, hub, fileConfig, policyGate)
	if err != nil {
		return fmt.Errorf("files: %w", err)
	}
	var conn *udp.ServerPacketConn
	defer func() {
		cancel()
		runErr = errors.Join(runErr, stopFiles())
		if conn != nil {
			_ = conn.Close()
		}
	}()
	if mediaPort != 0 && (mediaPort < 1 || mediaPort > 65535) {
		return errors.New("invalid media signaling port")
	}
	hub.SetMediaPort(uint16(mediaPort))
	signer, err := identity.LoadOrCreate(config.VoiceIdentityPath)
	if err != nil {
		return fmt.Errorf("load voice server identity: %w", err)
	}
	log.Printf("voice server identity fingerprint=%s", identity.PublicKeyFingerprint(signer.Public().(ed25519.PublicKey)))
	secureCodec := protocol.NewSecureDatagramCodec(true)
	authenticator, err := voice.NewAuthenticator(signer, store, secureCodec)
	if err != nil {
		return err
	}
	authenticator.SetPolicyGate(policyGate)
	password, overridden, err := store.ServerPassword(ctx)
	if err != nil {
		return fmt.Errorf("load saved server password: %w", err)
	}
	if !overridden {
		password = definition.Password
	}
	if err := authenticator.SetPassword(password); err != nil {
		return fmt.Errorf("configure server password: %w", err)
	}
	rawConn, err := udp.ListenUDP(config.VoicePort)
	if err != nil {
		return fmt.Errorf("listen UDP: %w", err)
	}
	conn, err = udp.NewServerPacketConn(rawConn, secureCodec)
	if err != nil {
		_ = rawConn.Close()
		return fmt.Errorf("configure UDP packet connection: %w", err)
	}
	var mediaServer *http.Server
	var mediaManager *media.Manager
	mediaErrCh := make(chan error, 1)
	stopMedia := func() error { return nil }
	waitWorkers := func() error { return nil }
	if mediaPort != 0 {
		if mediaPort < 1 || mediaPort > 65535 || config.MediaMinPort < 1 || config.MediaMaxPort > 65535 || config.MediaMaxPort < config.MediaMinPort {
			return errors.New("invalid media port configuration")
		}
		identity, identityErr := media.LoadOrCreateIdentity(config.MediaIdentity+".crt", config.MediaIdentity+".key")
		if identityErr != nil {
			return fmt.Errorf("media identity: %w", identityErr)
		}
		mediaManager, err = media.NewManagerWithConfig(hub, media.Config{MinUDPPort: uint16(config.MediaMinPort), MaxUDPPort: uint16(config.MediaMaxPort), AdvertisedIP: config.MediaAdvertisedIP, AdaptiveBitrate: config.MediaAdaptive})
		if err != nil {
			return fmt.Errorf("create media manager: %w", err)
		}
		closeManager := sync.OnceFunc(mediaManager.Close)
		defer closeManager()
		handler, handlerErr := media.NewHTTPHandler(hub, mediaManager, policyGate)
		if handlerErr != nil {
			return handlerErr
		}
		mediaServer = &http.Server{Addr: fmt.Sprintf(":%d", mediaPort), Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 30 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
		log.Printf("media signaling configured on https://0.0.0.0:%d fingerprint=%s udp=%d-%d advertised_ip=%q adaptive_bitrate=%t", mediaPort, identity.Fingerprint, config.MediaMinPort, config.MediaMaxPort, config.MediaAdvertisedIP, config.MediaAdaptive)
		if config.MediaAdvertisedIP == "" {
			log.Printf("media warning: advertised IP is empty; public-IP deployments must set -media-advertised-ip")
		}
		mediaListener, err := net.Listen("tcp", mediaServer.Addr)
		if err != nil {
			return fmt.Errorf("listen media: %w", err)
		}
		defer mediaListener.Close()
		stopMedia = func() error {
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutdownCancel()
			if err := mediaServer.Shutdown(shutdownCtx); err != nil {
				_ = mediaServer.Close()
			}
			closeManager()
			err := <-mediaErrCh
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		}
		go func() {
			mediaErrCh <- mediaServer.ServeTLS(mediaListener, identity.CertPath, identity.KeyPath)
			cancel()
		}()
	} else {
		close(mediaErrCh)
	}

	defer func() {
		cancel()
		runErr = errors.Join(runErr, stopMedia(), waitWorkers())
	}()
	cache := voice.NewRequestCache()
	cleanupErrCh := make(chan error, 1)
	dispatchErrCh := make(chan error, 1)
	go func() {
		err := voice.DispatchEvents(ctx, hub, conn)
		dispatchErrCh <- err
		cancel()
	}()

	go func() {
		cleanupErrCh <- CleanupLoopWithPolicyGate(
			ctx,
			hub,
			cache,
			SessionTimeout,
			CleanupInterval,
			policyGate,
			secureCodec,
		)
	}()
	waitWorkers = func() error {
		cleanupErr := <-cleanupErrCh
		dispatchErr := <-dispatchErrCh
		return errors.Join(withoutCancellation(cleanupErr), withoutCancellation(dispatchErr))
	}
	if err := waitDeployActivation(ctx, store, fileConfig.ReadyFile, fileConfig.ActivateFile); err != nil {
		return err
	}
	close(activation)
	console := NewConsole(hub, os.Stdin, os.Stdout, ConsoleInfo{
		StartedAt:     startedAt,
		ListenAddress: rawConn.LocalAddr().String(),
		ConfigSource:  configSource,
	}, store)
	console.SetPolicyGate(policyGate)
	notifyPrivileges := func(userID int64, level uint16, privileges uint8) {
		payload := []byte{byte(level >> 8), byte(level), privileges}
		for _, session := range hub.Inspect().Sessions {
			if session.UserID == userID {
				if err := voice.SendToSession(conn, session, protocol.VoicePacket{Type: protocol.PacketAccountPrivileges, SessionID: session.ID, Payload: payload}); err != nil {
					log.Printf("cannot notify account %d: %v", userID, err)
				}
			}
		}
	}
	console.SetPrivilegeNotifier(notifyPrivileges)
	administration := voice.NewAdministration(store, hub)
	administration.SetPrivilegeNotifier(notifyPrivileges)
	authenticator.SetAdministration(administration)
	log.Println("local server console ready; type help")
	go func() {
		if err := console.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("server console disabled: %v", err)
		}
	}()

	log.Printf(
		"voice server version=%s listening on %s config=%q channels=%d",
		currentVersion,
		rawConn.LocalAddr(),
		configSource,
		len(hub.ListChannels()),
	)
	return withoutCancellation(voice.ServeUDP(ctx, conn, hub, cache, authenticator))
}

func withoutCancellation(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
