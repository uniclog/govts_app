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

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
	"github.com/wailsapp/wails/v3/pkg/updater"
	"uniclog.io/sonoryx/internal/audio"
	voiceclient "uniclog.io/sonoryx/internal/client"
	"uniclog.io/sonoryx/internal/clientapp"
	"uniclog.io/sonoryx/internal/clientsettings"
	"uniclog.io/sonoryx/internal/clientupdate"
	"uniclog.io/sonoryx/internal/logging"
	wailsui "uniclog.io/sonoryx/internal/ui/wails"
)

func init() {
	application.RegisterEvent[bool]("client-state-changed")
	application.RegisterEvent[bool]("client-event-log-changed")
	application.RegisterEvent[bool]("server-status-changed")
	application.RegisterEvent[wailsui.AudioMeterDTO]("audio-meter")
	application.RegisterEvent[bool]("tray-screen-share")
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

	client := clientapp.New(clientapp.Options{Logger: log.Default()})
	service := wailsui.NewService(client)
	updates := clientupdate.New(filepath.Join(configDir, "Sonoryx"), func() bool { return string(client.Snapshot().ConnectionStatus) == "connected" })
	if err := wailsui.EnableDefaultSettings(service); err != nil {
		log.Printf("load settings: %v", err)
	}
	var mainWindow *application.WebviewWindow
	var quitting atomic.Bool
	// A failure inside the notification service used to abort app.Run. The
	// wrapper keeps the client up and simply sends no toasts.
	toasts := &notificationService{inner: notifications.New()}
	app := application.New(application.Options{
		Name:        "Sonoryx",
		Description: "Голосовой клиент Sonoryx",
		Icon:        applicationIcon,
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "app.sonoryx.desktop",
			OnSecondInstanceLaunch: func(application.SecondInstanceData) {
				if mainWindow != nil {
					showMainWindow(mainWindow)
				}
			},
		},
		Services: []application.Service{
			application.NewService(service),
			application.NewService(updates),
			application.NewService(toasts),
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
			wailsui.StopHotkeys(service)
			clientupdate.Stop(updates)
			cancelBridge()
			stopBridge()
			stopServerStatus()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := client.Close(ctx); err != nil {
				log.Printf("close client: %v", err)
			}
		})
	}
	app.OnShutdown(shutdown)

	mainWindow = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            fmt.Sprintf("SRX %s", applicationVersion()),
		Width:            1180,
		Height:           760,
		MinWidth:         900,
		MinHeight:        620,
		BackgroundColour: application.NewRGB(28, 37, 57),
		URL:              "/",
	})
	// Cancelling the close keeps the process alive. The default listener otherwise
	// destroys the window and quits the client.
	mainWindow.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		if quitting.Load() {
			return
		}
		if service.CloseToTray() {
			event.Cancel()
			mainWindow.Hide()
			mainWindowHidden.Store(true)
			return
		}
		quitting.Store(true)
		app.Quit()
	})
	installTray(app, mainWindow, client, service, &quitting)
	trackMainWindowVisibility(mainWindow)
	wailsui.ConfigureHotkeyNotifications(service,
		func() bool { return mainWindowHidden.Load() },
		func(id, title, body string) error {
			if !toasts.ready.Load() {
				return nil
			}
			return toasts.inner.SendNotification(notifications.NotificationOptions{
				ID:    id,
				Title: title,
				Body:  body,
				// The hotkey already plays its own cue.
				Sound: &notifications.NotificationSound{Silent: true},
			})
		})

	if err := app.Run(); err != nil {
		shutdown()
		log.Printf("client stopped with error: %v", err)
		stopLogging()
		os.Exit(1)
	}
	shutdown()
}

// mainWindowHidden mirrors the main window without asking it. The hotkey
// handler reads it while shutdown, which runs on the main thread, may be
// waiting for that handler; a window call would wait for the main thread too.
var mainWindowHidden atomic.Bool

func trackMainWindowVisibility(window *application.WebviewWindow) {
	hide := func(*application.WindowEvent) { mainWindowHidden.Store(true) }
	show := func(*application.WindowEvent) { mainWindowHidden.Store(false) }
	window.RegisterHook(events.Common.WindowHide, hide)
	window.RegisterHook(events.Common.WindowMinimise, hide)
	window.RegisterHook(events.Common.WindowShow, show)
	window.RegisterHook(events.Common.WindowRestore, show)
	window.RegisterHook(events.Common.WindowUnMinimise, show)
}

func showMainWindow(window *application.WebviewWindow) {
	window.UnMinimise()
	window.Show()
	window.Focus()
	mainWindowHidden.Store(false)
}

// notificationService starts the Windows toast service without letting a
// registry or COM failure stop the client.
type notificationService struct {
	inner *notifications.NotificationService
	ready atomic.Bool
}

func (n *notificationService) ServiceStartup(ctx context.Context, options application.ServiceOptions) error {
	if err := n.inner.ServiceStartup(ctx, options); err != nil {
		log.Printf("system notifications unavailable: %v", err)
		return nil
	}
	n.ready.Store(true)
	return nil
}

func (n *notificationService) ServiceShutdown() error {
	if !n.ready.Load() {
		return nil
	}
	return n.inner.ServiceShutdown()
}

func installTray(app *application.App, window *application.WebviewWindow, client *clientapp.App, service *wailsui.Service, quitting *atomic.Bool) {
	menu := app.NewMenu()
	menu.Add("Открыть").OnClick(func(*application.Context) {
		showMainWindow(window)
	})
	menu.AddSeparator()
	microphone := menu.Add("Выключить микрофон")
	speaker := menu.Add("Заглушить динамик")
	screen := menu.Add("Демонстрация экрана")
	var hasCapture atomic.Bool
	refreshCapture := func() {
		devices, err := audio.ListDevices()
		hasCapture.Store(err == nil && len(devices.Capture) > 0)
	}
	refreshCapture()
	var menuSync sync.Mutex
	syncMenu := func() {
		menuSync.Lock()
		defer menuSync.Unlock()
		syncTrayMenu(client, hasCapture.Load(), microphone, speaker, screen)
	}
	syncMenu()
	microphone.OnClick(func(*application.Context) {
		refreshCapture()
		view := client.Snapshot()
		if hasCapture.Load() && view.CaptureAvailable {
			service.SetMuted(!view.Muted)
		}
		syncMenu()
	})
	speaker.OnClick(func(*application.Context) {
		view := client.Snapshot()
		if err := service.SetDeafened(!view.Deafened); err != nil {
			log.Printf("tray deafen failed: %v", err)
		}
		syncMenu()
	})
	screen.OnClick(func(*application.Context) {
		view := client.Snapshot()
		if !screenShareReady(view) {
			syncMenu()
			return
		}
		if !screenShareActive(view) {
			showMainWindow(window)
		}
		app.Event.Emit("tray-screen-share", true)
	})
	app.Event.On("client-state-changed", func(*application.CustomEvent) {
		syncMenu()
	})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if quitting.Load() {
				return
			}
			refreshCapture()
			syncMenu()
		}
	}()
	menu.AddSeparator()
	menu.Add("Выход").OnClick(func(*application.Context) {
		quitting.Store(true)
		app.Quit()
	})

	tray := app.SystemTray.New()
	tray.SetIcon(applicationIcon)
	tray.SetTooltip("Sonoryx")
	tray.SetMenu(menu)
	tray.OnClick(func() { showMainWindow(window) })
}

func syncTrayMenu(client *clientapp.App, hasCapture bool, microphone, speaker, screen *application.MenuItem) {
	view := client.Snapshot()
	if hasCapture && view.CaptureAvailable {
		microphone.SetEnabled(true)
		if view.Muted {
			microphone.SetLabel("Включить микрофон")
		} else {
			microphone.SetLabel("Выключить микрофон")
		}
	} else {
		microphone.SetLabel("Нет микрофона")
		microphone.SetEnabled(false)
	}
	if view.Deafened {
		speaker.SetLabel("Включить динамик")
	} else {
		speaker.SetLabel("Заглушить динамик")
	}
	if !screenShareReady(view) {
		screen.SetLabel("Демонстрация экрана")
		screen.SetEnabled(false)
		return
	}
	screen.SetEnabled(true)
	if screenShareActive(view) {
		screen.SetLabel("Завершить демонстрацию")
	} else {
		screen.SetLabel("Демонстрация экрана")
	}
}

func screenShareReady(view voiceclient.ClientViewState) bool {
	return view.ConnectionStatus == voiceclient.ConnectionConnected && view.ChannelID != 0
}

func screenShareActive(view voiceclient.ClientViewState) bool {
	for _, stream := range view.ScreenStreams {
		if stream.OwnerSessionID == view.SessionID {
			return true
		}
	}
	return false
}
