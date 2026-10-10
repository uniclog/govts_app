package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"uniclog.io/sonoryx/internal/appversion"
	"uniclog.io/sonoryx/internal/identity"
	"uniclog.io/sonoryx/internal/logging"
	"uniclog.io/sonoryx/internal/media"
	"uniclog.io/sonoryx/internal/persist"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/server"
	"uniclog.io/sonoryx/internal/transport/udp"
	"uniclog.io/sonoryx/internal/voice"
)

func main() {
	showVersion := flag.Bool("version", false, "print server version and exit")
	configPath := flag.String("config", "", "path to server JSON config")
	databasePath := flag.String("db", "govts.db", "path to persistent SQLite database")
	logPath := flag.String("log-file", "logs/server.log", "base path for per-run server log files")
	port := flag.Int("port", 9000, "UDP listen port (1..65535)")
	mediaPort := flag.Int("media-port", -1, "HTTPS media signaling port; -1 uses voice port + 2, 0 disables screen sharing")
	mediaMinPort := flag.Int("media-min-port", 20000, "first UDP port used by WebRTC")
	mediaMaxPort := flag.Int("media-max-port", 20100, "last UDP port used by WebRTC")
	mediaAdvertisedIP := flag.String("media-advertised-ip", "", "public IP advertised by WebRTC; empty uses local interfaces")
	mediaIdentity := flag.String("media-identity", "govts-media", "path prefix for generated media TLS certificate and key")
	voiceIdentity := flag.String("voice-identity", "govts-voice.seed", "path to persistent server signing seed")
	voiceRedundancy := flag.Bool("voice-redundancy", false, "repeat the previous voice frame in each server → client datagram for clients that support it")
	publicStatus := flag.Bool("public-status", false, "allow unauthenticated UDP queries of the connected session count")
	flag.Parse()
	if *showVersion {
		fmt.Println(serverVersion())
		return
	}

	stopLogging, err := logging.StartLocal(*logPath)
	if err != nil {
		log.Printf("file logging unavailable; continuing with console logging: %v", err)
	}
	log.Printf("server starting: version=%s", serverVersion())
	err = run(*configPath, *databasePath, *voiceIdentity, *port, *mediaPort, *mediaMinPort, *mediaMaxPort, *mediaAdvertisedIP, *mediaIdentity, *voiceRedundancy, *publicStatus)
	if err != nil {
		log.Printf("server stopped with error: %v", err)
	}
	stopLogging()
	if err != nil {
		os.Exit(1)
	}
}

func run(configPath, databasePath, voiceIdentityPath string, port, mediaPort, mediaMinPort, mediaMaxPort int, mediaAdvertisedIP, mediaIdentity string, voiceRedundancy, publicStatus bool) error {
	currentVersion, err := appversion.Parse(serverVersion())
	if err != nil {
		return fmt.Errorf("invalid embedded server version: %w", err)
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("invalid UDP port %d: must be 1..65535", port)
	}
	if mediaPort == -1 {
		mediaPort = port + 2
	}
	startedAt := time.Now()
	ctx, cancel := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer cancel()

	var source server.BootstrapSource = server.BuiltinBootstrapSource{}
	configSource := "builtin"
	if configPath != "" {
		source = server.JSONBootstrapSource{Path: configPath}
		configSource = configPath
	}
	store, err := persist.Open(databasePath)
	if err != nil {
		return fmt.Errorf("open server database: %w", err)
	}
	defer store.Close()
	hub, err := server.BootstrapPersistentHub(ctx, store, source)
	if err != nil {
		return fmt.Errorf("bootstrap server channels: %w", err)
	}
	hub.SetServerVersion(currentVersion)
	hub.SetVoiceBundles(voiceRedundancy)
	hub.SetPublicStatus(publicStatus)
	if mediaPort != 0 && (mediaPort < 1 || mediaPort > 65535) {
		return errors.New("invalid media signaling port")
	}
	hub.SetMediaPort(uint16(mediaPort))
	signer, err := identity.LoadOrCreate(voiceIdentityPath)
	if err != nil {
		return fmt.Errorf("load voice server identity: %w", err)
	}
	secureCodec := protocol.NewSecureDatagramCodec(true)
	authenticator, err := voice.NewAuthenticator(signer, store, secureCodec)
	if err != nil {
		return err
	}
	policyGate := &sync.Mutex{}
	authenticator.SetPolicyGate(policyGate)
	rawConn, err := udp.ListenUDP(port)
	if err != nil {
		return fmt.Errorf("listen UDP: %w", err)
	}
	conn, err := udp.NewServerPacketConn(rawConn, secureCodec)
	if err != nil {
		_ = rawConn.Close()
		return fmt.Errorf("configure UDP packet connection: %w", err)
	}
	defer conn.Close()
	var mediaServer *http.Server
	var mediaManager *media.Manager
	mediaErrCh := make(chan error, 1)
	if mediaPort != 0 {
		if mediaPort < 1 || mediaPort > 65535 || mediaMinPort < 1 || mediaMaxPort > 65535 || mediaMaxPort < mediaMinPort {
			return errors.New("invalid media port configuration")
		}
		identity, identityErr := media.LoadOrCreateIdentity(mediaIdentity+".crt", mediaIdentity+".key")
		if identityErr != nil {
			return fmt.Errorf("media identity: %w", identityErr)
		}
		mediaManager, err = media.NewManagerWithConfig(hub, media.Config{MinUDPPort: uint16(mediaMinPort), MaxUDPPort: uint16(mediaMaxPort), AdvertisedIP: mediaAdvertisedIP})
		if err != nil {
			return fmt.Errorf("create media manager: %w", err)
		}
		handler, handlerErr := media.NewHTTPHandler(hub, mediaManager, policyGate)
		if handlerErr != nil {
			return handlerErr
		}
		mediaServer = &http.Server{Addr: fmt.Sprintf(":%d", mediaPort), Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 30 * time.Second}
		log.Printf("media signaling configured on https://0.0.0.0:%d fingerprint=%s udp=%d-%d advertised_ip=%q", mediaPort, identity.Fingerprint, mediaMinPort, mediaMaxPort, mediaAdvertisedIP)
		if mediaAdvertisedIP == "" {
			log.Printf("media warning: advertised IP is empty; public-IP deployments must set -media-advertised-ip")
		}
		go func() { mediaErrCh <- mediaServer.ListenAndServeTLS(identity.CertPath, identity.KeyPath); cancel() }()
	} else {
		close(mediaErrCh)
	}

	cache := voice.NewRequestCache()
	cleanupErrCh := make(chan error, 1)
	dispatchErrCh := make(chan error, 1)
	go func() {
		err := voice.DispatchEvents(ctx, hub, conn)
		dispatchErrCh <- err
		cancel()
	}()

	go func() {
		cleanupErrCh <- server.CleanupLoopWithPolicyGate(
			ctx,
			hub,
			cache,
			server.SessionTimeout,
			server.CleanupInterval,
			policyGate,
			secureCodec,
		)
	}()
	console := server.NewConsole(hub, os.Stdin, os.Stdout, server.ConsoleInfo{
		StartedAt:     startedAt,
		ListenAddress: rawConn.LocalAddr().String(),
		ConfigSource:  configSource,
	}, store)
	console.SetPolicyGate(policyGate)
	console.SetPrivilegeNotifier(func(userID int64, level uint16, permissions uint8) {
		payload := []byte{byte(level >> 8), byte(level), permissions}
		for _, session := range hub.Inspect().Sessions {
			if session.UserID == userID {
				if err := voice.SendToSession(conn, session, protocol.VoicePacket{Type: protocol.PacketAccountPrivileges, SessionID: session.ID, Payload: payload}); err != nil {
					log.Printf("cannot notify account %d: %v", userID, err)
				}
			}
		}
	})
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
	serveErr := voice.ServeUDP(ctx, conn, hub, cache, authenticator)
	cancel()
	if mediaServer != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = mediaServer.Shutdown(shutdownCtx)
		shutdownCancel()
		mediaManager.Close()
	}
	cleanupErr := <-cleanupErrCh
	dispatchErr := <-dispatchErrCh
	if errors.Is(dispatchErr, context.Canceled) {
		dispatchErr = nil
	}

	if errors.Is(serveErr, context.Canceled) {
		serveErr = nil
	}
	if errors.Is(cleanupErr, context.Canceled) {
		cleanupErr = nil
	}

	var mediaErr error
	if mediaPort != 0 {
		mediaErr = <-mediaErrCh
		if errors.Is(mediaErr, http.ErrServerClosed) {
			mediaErr = nil
		}
	}
	return errors.Join(serveErr, cleanupErr, dispatchErr, mediaErr)
}
