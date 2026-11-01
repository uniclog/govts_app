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
	"github.com/wailsapp/wails/v3/pkg/updater"
	"uniclog.io/sonoryx/internal/apppopup"
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
				if mainWindow != nil {
					showMainWindow(mainWindow)
				}
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
		Frameless:        true,
		Width:            1180,
		Height:           760,
		MinWidth:         900,
		MinHeight:        720,
		BackgroundColour: application.NewRGB(28, 37, 57),
		URL:              "/",
		// Without this the Wails runtime answers every external file drag with "no drop".
		EnableFileDrop: true,
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
	trackMainWindowVisibility(mainWindow, service)
	wailsui.ConfigureHotkeyNotifications(service,
		func() bool { return mainWindowHidden.Load() },
		func(title, body string) error {
			window := popup.Load()
			if window == nil {
				return nil
			}
			window.SetLook(apppopup.ParseLook(service.PopupLook()))
			return window.Show(title, body)
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

func trackMainWindowVisibility(window *application.WebviewWindow, service *wailsui.Service) {
	hide := func(*application.WindowEvent) {
		mainWindowHidden.Store(true)
		service.SetHotkeyWindowInactive(true)
	}
	show := func(*application.WindowEvent) { mainWindowHidden.Store(false) }
	window.RegisterHook(events.Common.WindowHide, hide)
	window.RegisterHook(events.Common.WindowMinimise, hide)
	window.RegisterHook(events.Common.WindowShow, show)
	window.RegisterHook(events.Common.WindowRestore, show)
	window.RegisterHook(events.Common.WindowUnMinimise, show)
	window.RegisterHook(events.Common.WindowLostFocus, func(*application.WindowEvent) {
		service.SetHotkeyWindowInactive(true)
	})
	window.RegisterHook(events.Common.WindowFocus, func(*application.WindowEvent) {
		service.SetHotkeyWindowInactive(false)
	})
}

func showMainWindow(window *application.WebviewWindow) {
	window.UnMinimise()
	window.Show()
	window.Focus()
	mainWindowHidden.Store(false)
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
