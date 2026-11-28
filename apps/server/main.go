package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"uniclog.io/sonoryx/apps/server/internal/filetransfer"
	"uniclog.io/sonoryx/apps/server/internal/persist"
	"uniclog.io/sonoryx/apps/server/internal/server"
	"uniclog.io/sonoryx/apps/server/internal/voice"
	"uniclog.io/sonoryx/internal/logging"
)

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
	fileConfig := server.FileServerConfig{Storage: filetransfer.DefaultConfig("data/files")}
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
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err = server.Run(ctx, server.RunConfig{
		Version:           serverVersion(),
		BootstrapPath:     *configPath,
		DatabasePath:      *databasePath,
		VoiceIdentityPath: *voiceIdentity,
		VoicePort:         *port,
		MediaPort:         *mediaPort,
		MediaMinPort:      *mediaMinPort,
		MediaMaxPort:      *mediaMaxPort,
		MediaAdvertisedIP: *mediaAdvertisedIP,
		MediaIdentity:     *mediaIdentity,
		MediaAdaptive:     *mediaAdaptive,
		VoiceRedundancy:   *voiceRedundancy,
		PublicStatus:      *publicStatus,
		MaxSessions:       *maxSessions,
		VoiceReserves: server.VoiceReserveConfig{
			Uplink:              *voiceUplink,
			RecipientKiB:        *voiceRecipientReserve,
			GlobalKiB:           *voiceGlobalReserve,
			ControlRecipientKiB: *voiceControlRecipient,
			ControlGlobalKiB:    *voiceControlGlobal,
		},
		Files: fileConfig,
	})
	cancel()
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
