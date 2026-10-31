package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"uniclog.io/sonoryx/internal/appversion"
	"uniclog.io/sonoryx/internal/filetransfer"
	"uniclog.io/sonoryx/internal/identity"
	"uniclog.io/sonoryx/internal/logging"
	"uniclog.io/sonoryx/internal/media"
	"uniclog.io/sonoryx/internal/persist"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/server"
	"uniclog.io/sonoryx/internal/transport/udp"
	"uniclog.io/sonoryx/internal/voice"
)

type voiceReserveConfig struct {
	Uplink                                                         bool
	RecipientKiB, GlobalKiB, ControlRecipientKiB, ControlGlobalKiB int
}

func (c voiceReserveConfig) validate() error {
	for _, value := range []int{c.RecipientKiB, c.GlobalKiB, c.ControlRecipientKiB, c.ControlGlobalKiB} {
		if value < 1 || value > 1048576 {
			return errors.New("voice reserve must be 1..1048576 KiB/s")
		}
	}
	return nil
}

func main() {
	if filetransfer.ThumbnailMode() {
		return
	}
	if len(os.Args) == 4 && os.Args[1] == "--snapshot-db" {
		if err := persist.SnapshotDatabase(context.Background(), os.Args[2], os.Args[3]); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) == 3 && os.Args[1] == "--check-db" {
		info, err := os.Stat(os.Args[2])
		if err != nil || !info.Mode().IsRegular() {
			log.Fatal("migration check requires an existing database copy")
		}
		db, err := persist.Open(os.Args[2])
		if err != nil {
			log.Fatal(err)
		}
		if err = db.Close(); err != nil {
			log.Fatal(err)
		}
		return
	}
	fileConfig := fileServerConfig{Storage: filetransfer.DefaultConfig("data/files")}
	flag.StringVar(&fileConfig.ReadyFile, "deploy-ready-file", "", "private readiness file for staged deploy")
	flag.StringVar(&fileConfig.ActivateFile, "deploy-activate-file", "", "private activation file for staged deploy")
	flag.IntVar(&fileConfig.Port, "file-port", 0, "separate HTTPS file port; 0 disables file transfer")
	flag.StringVar(&fileConfig.Identity, "file-identity", "sonoryx-files", "file TLS certificate/key path prefix")
	flag.StringVar(&fileConfig.Storage.Directory, "file-dir", fileConfig.Storage.Directory, "file storage directory")
	flag.Int64Var(&fileConfig.Storage.MaxFileBytes, "file-max-bytes", fileConfig.Storage.MaxFileBytes, "maximum file size in bytes")
	flag.Int64Var(&fileConfig.Storage.UserQuotaBytes, "file-user-quota", fileConfig.Storage.UserQuotaBytes, "logical bytes reserved per account")
	flag.Int64Var(&fileConfig.Storage.ServerQuotaBytes, "file-server-quota", fileConfig.Storage.ServerQuotaBytes, "logical bytes reserved on server")
	flag.IntVar(&fileConfig.Storage.UserBytesPerSecond, "file-user-rate", fileConfig.Storage.UserBytesPerSecond, "bytes per second per account and direction")
	flag.IntVar(&fileConfig.Storage.ServerBytesPerSecond, "file-server-rate", fileConfig.Storage.ServerBytesPerSecond, "bytes per second globally per direction")
	flag.IntVar(&fileConfig.Storage.MaxTransfers, "file-transfers", fileConfig.Storage.MaxTransfers, "maximum simultaneous file HTTP operations")
	flag.DurationVar(&fileConfig.Storage.UploadTTL, "file-upload-ttl", fileConfig.Storage.UploadTTL, "unfinished upload retention")
	flag.DurationVar(&fileConfig.Storage.Retention, "file-retention", 0, "attachment retention; 0 keeps until deletion")
	showVersion := flag.Bool("version", false, "print server version and exit")
	configPath := flag.String("config", "", "path to server JSON config")
	databasePath := flag.String("db", "sonoryx.db", "path to persistent SQLite database")
	logPath := flag.String("log-file", "logs/server.log", "base path for per-run server log files")
	port := flag.Int("port", 9000, "UDP listen port (1..65535)")
	mediaPort := flag.Int("media-port", -1, "HTTPS media signaling port; -1 uses voice port + 2, 0 disables screen sharing")
	mediaMinPort := flag.Int("media-min-port", 20000, "first UDP port used by WebRTC")
	mediaMaxPort := flag.Int("media-max-port", 20100, "last UDP port used by WebRTC")
	mediaAdvertisedIP := flag.String("media-advertised-ip", "", "public IP advertised by WebRTC; empty uses local interfaces")
	mediaIdentity := flag.String("media-identity", "sonoryx-media", "path prefix for generated media TLS certificate and key")
	mediaAdaptive := flag.Bool("media-adaptive-bitrate", false, "estimate each viewer's rate, cap the screen source with REMB and pace viewer RTP")
	voiceIdentity := flag.String("voice-identity", "sonoryx-voice.seed", "path to persistent server signing seed")
	voiceRedundancy := flag.Bool("voice-redundancy", false, "enable adaptive 2/3-frame downstream voice protection")
	voiceUplink := flag.Bool("voice-uplink-redundancy", false, "permit adaptive 2/3-frame upstream voice protection")
	voiceRecipientReserve := flag.Int("voice-recipient-reserve-kib", 128, "downstream reserve per recipient in KiB/s")
	voiceGlobalReserve := flag.Int("voice-global-reserve-kib", 8192, "global downstream reserve in KiB/s")
	voiceControlRecipient := flag.Int("voice-control-recipient-kib", 64, "VoiceState bytes per recipient in KiB/s")
	voiceControlGlobal := flag.Int("voice-control-global-kib", 4096, "global VoiceState bytes in KiB/s")
	publicStatus := flag.Bool("public-status", false, "allow unauthenticated UDP queries of the connected session count")
	maxSessions := flag.Int("max-sessions", voice.DefaultMaxSessions, "maximum number of online sessions")
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
	explicit := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	if !explicit["db"] {
		*databasePath = legacyPath(*databasePath, "govts.db", "")
	}
	if !explicit["voice-identity"] {
		*voiceIdentity = legacyPath(*voiceIdentity, "govts-voice.seed", "")
	}
	if !explicit["media-identity"] {
		*mediaIdentity = legacyPath(*mediaIdentity, "govts-media", ".key")
	}
	err = run(*configPath, *databasePath, *voiceIdentity, *port, *mediaPort, *mediaMinPort, *mediaMaxPort, *mediaAdvertisedIP, *mediaIdentity, *mediaAdaptive, *voiceRedundancy, *publicStatus, *maxSessions, voiceReserveConfig{Uplink: *voiceUplink, RecipientKiB: *voiceRecipientReserve, GlobalKiB: *voiceGlobalReserve, ControlRecipientKiB: *voiceControlRecipient, ControlGlobalKiB: *voiceControlGlobal}, fileConfig)
	if err != nil {
		log.Printf("server stopped with error: %v", err)
	}
	stopLogging()
	if err != nil {
		os.Exit(1)
	}
}

// legacyPath keeps a server deployed before the Sonoryx rename on its data:
// when current+suffix is missing and legacy+suffix exists, it returns legacy.
// A fresh database or identity would lose accounts and change the key that
// clients pinned. suffix names the file checked for a path prefix.
func legacyPath(current, legacy, suffix string) string {
	if _, err := os.Stat(current + suffix); !errors.Is(err, os.ErrNotExist) {
		return current
	}
	if _, err := os.Stat(legacy + suffix); err != nil {
		return current
	}
	log.Printf("using pre-rename server file: %s", legacy)
	return legacy
}

func run(configPath, databasePath, voiceIdentityPath string, port, mediaPort, mediaMinPort, mediaMaxPort int, mediaAdvertisedIP, mediaIdentity string, mediaAdaptive, voiceRedundancy, publicStatus bool, maxSessions int, voiceReserves voiceReserveConfig, fileConfigs ...fileServerConfig) error {
	if err := voiceReserves.validate(); err != nil {
		return err
	}
	if maxSessions < 1 {
		return errors.New("max-sessions must be positive")
	}
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
	hub.SetVoiceUplink(voiceReserves.Uplink)
	hub.SetVoiceReserves(voiceReserves.RecipientKiB*1024, voiceReserves.GlobalKiB*1024)
	hub.SetVoiceControlRates(voiceReserves.ControlRecipientKiB*1024, voiceReserves.ControlGlobalKiB*1024)

	hub.SetPublicStatus(publicStatus)
	hub.SetMaxSessions(maxSessions)
	policyGate := &sync.Mutex{}
	activation := make(chan struct{})
	stopFiles := func() error { return nil }
	if len(fileConfigs) > 0 {
		fileConfigs[0].Activation = activation
		stopFiles, err = startFileServer(ctx, cancel, store, hub, fileConfigs[0], policyGate)
		if err != nil {
			return fmt.Errorf("files: %w", err)
		}
		defer func() { cancel(); _ = stopFiles() }()
	}
	if mediaPort != 0 && (mediaPort < 1 || mediaPort > 65535) {
		return errors.New("invalid media signaling port")
	}
	hub.SetMediaPort(uint16(mediaPort))
	signer, err := identity.LoadOrCreate(voiceIdentityPath)
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
		mediaManager, err = media.NewManagerWithConfig(hub, media.Config{MinUDPPort: uint16(mediaMinPort), MaxUDPPort: uint16(mediaMaxPort), AdvertisedIP: mediaAdvertisedIP, AdaptiveBitrate: mediaAdaptive})
		if err != nil {
			return fmt.Errorf("create media manager: %w", err)
		}
		handler, handlerErr := media.NewHTTPHandler(hub, mediaManager, policyGate)
		if handlerErr != nil {
			return handlerErr
		}
		mediaServer = &http.Server{Addr: fmt.Sprintf(":%d", mediaPort), Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 30 * time.Second}
		log.Printf("media signaling configured on https://0.0.0.0:%d fingerprint=%s udp=%d-%d advertised_ip=%q adaptive_bitrate=%t", mediaPort, identity.Fingerprint, mediaMinPort, mediaMaxPort, mediaAdvertisedIP, mediaAdaptive)
		if mediaAdvertisedIP == "" {
			log.Printf("media warning: advertised IP is empty; public-IP deployments must set -media-advertised-ip")
		}
		mediaListener, err := net.Listen("tcp", mediaServer.Addr)
		if err != nil {
			return fmt.Errorf("listen media: %w", err)
		}
		go func() {
			mediaErrCh <- mediaServer.ServeTLS(mediaListener, identity.CertPath, identity.KeyPath)
			cancel()
		}()
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
	if len(fileConfigs) > 0 {
		if err := waitDeployActivation(ctx, store, fileConfigs[0].ReadyFile, fileConfigs[0].ActivateFile); err != nil {
			return err
		}
	}
	close(activation)
	console := server.NewConsole(hub, os.Stdin, os.Stdout, server.ConsoleInfo{
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
	return errors.Join(serveErr, cleanupErr, dispatchErr, mediaErr, stopFiles())
}
