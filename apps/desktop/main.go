package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"
	"uniclog.io/sonoryx/apps/desktop/internal/nativeaudio"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/updater"
	"uniclog.io/sonoryx/apps/desktop/internal/apppopup"
	"uniclog.io/sonoryx/apps/desktop/internal/clientupdate"
	"uniclog.io/sonoryx/apps/desktop/internal/desktopwindow"
	"uniclog.io/sonoryx/apps/desktop/internal/settings"
	wailsui "uniclog.io/sonoryx/apps/desktop/internal/wails"
	"uniclog.io/sonoryx/internal/audio"
	clientapp "uniclog.io/sonoryx/internal/clientcore"
	"uniclog.io/sonoryx/internal/logging"
)

func init() {
	application.RegisterEvent[bool]("client-state-changed")
	application.RegisterEvent[bool]("client-event-log-changed")
	application.RegisterEvent[bool]("server-status-changed")
	application.RegisterEvent[wailsui.AudioMeterDTO]("audio-meter")
	application.RegisterEvent[bool]("tray-screen-share")
	application.RegisterEvent[bool]("main-window-shown")
}

func main() {
	clientupdate.HandleRecoveryMode()
	updater.HandleHelperMode()
	configDir, err := os.UserConfigDir()
	if err != nil {
		log.Fatalf("resolve application data directory: %v", err)
	}
	webviewDataPath := filepath.Join(configDir, "Sonoryx", "WebView2")
	stopLogging, err := logging.Start(filepath.Join(configDir, "Sonoryx", "logs", "client.log"))
	if err != nil {
		log.Printf("file logging unavailable; continuing with console logging: %v", err)
	}
	defer stopLogging()
	log.Printf("client starting: version=%s", applicationVersion())
	if slices.Contains(os.Args[1:], audio.SoundLogFlag) {
		audio.SetSoundLog(true)
		log.Printf("sound cue trace enabled: flag=%s", audio.SoundLogFlag)
	}
	if err := clientsettings.MigrateLegacyProfile(configDir); err != nil {
		log.Printf("migrate legacy profile: %v", err)
	}

	desktopAudio := nativeaudio.NewController(log.Default())
	client := clientapp.New(clientapp.Options{Logger: log.Default(), Audio: desktopAudio, OnConnectionEnded: desktopAudio.SyncPreview})
	desktopAudio.AttachPipeline(client)
	service := wailsui.NewService(client)
	wailsui.ConfigureAudio(service, desktopAudio)
	wailsui.ConfigureFileQueue(service, filepath.Join(configDir, "Sonoryx"))
	updates := clientupdate.New(filepath.Join(configDir, "Sonoryx"), func() bool { return string(client.Snapshot().ConnectionStatus) == "connected" })
	if err := wailsui.EnableDefaultSettings(service); err != nil {
		log.Printf("load settings: %v", err)
	}
	var window desktopwindow.Controller
	var popup atomic.Pointer[apppopup.Popup]
	var popupLifecycle sync.Mutex
	popupStopped := false // guarded by popupLifecycle
	app := application.New(application.Options{
		Name:        "Sonoryx",
		Description: "Голосовой клиент Sonoryx",
		Icon:        applicationIcon,
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "app.sonoryx.desktop",
			OnSecondInstanceLaunch: func(application.SecondInstanceData) {
				window.Show()
			},
		},
		Services: []application.Service{
			application.NewService(service),
			application.NewService(updates),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
		Windows: application.WindowsOptions{
			WebviewUserDataPath: webviewDataPath,
			// Screen viewer windows open without a click inside them; their
			// stream sound must start like the voice chat does.
			AdditionalBrowserArgs: []string{"--autoplay-policy=no-user-gesture-required"},
		},
	})
	// Wait until Wails has configured process DPI awareness before creating HWNDs.
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		popupLifecycle.Lock()
		defer popupLifecycle.Unlock()
		if popupStopped || popup.Load() != nil {
			return
		}
		window, err := apppopup.New(applicationIcon)
		if err != nil {
			log.Printf("app popup unavailable: %v", err)
			return
		}
		popup.Store(window)
	})
	clientupdate.RecordRecoveryStartup(applicationVersion())
	if err := clientupdate.Initialize(updates, app, applicationVersion(), rawUpdatePublicKey); err != nil {
		log.Printf("initialize updater: %v", err)
	}

	bridgeCtx, cancelBridge := context.WithCancel(context.Background())
	stopBridge := wailsui.StartEventBridge(bridgeCtx, app, client)
	stopServerStatus := wailsui.ConfigureServerStatuses(app, service)
	wailsui.StartHotkeys(service)
	var shutdownOnce sync.Once
	shutdown := func() {
		shutdownOnce.Do(func() {
			window.Close()
			wailsui.FlushParticipantVolumes(service)
			wailsui.StopHotkeys(service)
			popupLifecycle.Lock()
			popupStopped = true
			popupWindow := popup.Swap(nil)
			popupLifecycle.Unlock()
			if popupWindow != nil {
				popupWindow.Close()
			}
			clientupdate.Stop(updates)
			cancelBridge()
			stopBridge()
			stopServerStatus()
			desktopAudio.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := wailsui.CloseFileQueue(service, ctx); err != nil {
				log.Printf("close file queue: %v", err)
			}
			if err := client.Close(ctx); err != nil {
				log.Printf("close client: %v", err)
			}
		})
	}
	app.OnShutdown(shutdown)

	window.Install(app, client, service, fmt.Sprintf("SRX %s", applicationVersion()), applicationIcon)
	wailsui.ConfigureHotkeyNotifications(service,
		window.Hidden,
		func(title, body string) error {
			window := popup.Load()
			if window == nil {
				return nil
			}
			window.SetLook(apppopup.ParseLook(service.PopupLook()))
			return window.Show(title, body)
		})
	// A message popup stays a little longer and opens the window on a click.
	wailsui.ConfigureMessagePopups(service, func(title, body string) error {
		popupWindow := popup.Load()
		if popupWindow == nil {
			return nil
		}
		popupWindow.SetLook(apppopup.ParseLook(service.PopupLook()))
		return popupWindow.ShowMessage(apppopup.Message{Title: title, Body: body, Lifetime: messagePopupLifetime,
			OnClick: window.Show})
	})

	if err := app.Run(); err != nil {
		shutdown()
		log.Printf("client stopped with error: %v", err)
		stopLogging()
		os.Exit(1)
	}
	shutdown()
}

const messagePopupLifetime = 5 * time.Second
