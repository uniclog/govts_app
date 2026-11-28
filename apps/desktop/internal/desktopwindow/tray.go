package desktopwindow

import (
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"uniclog.io/sonoryx/apps/desktop/internal/nativeaudio"
	clientapp "uniclog.io/sonoryx/internal/clientcore"
)

func (c *Controller) installTray(app *application.App, client *clientapp.App, service windowService, icon []byte) {
	menu := app.NewMenu()
	menu.Add("Открыть").OnClick(func(*application.Context) {
		c.Show()
	})
	menu.AddSeparator()
	microphone := menu.Add("Выключить микрофон")
	speaker := menu.Add("Заглушить динамик")
	screen := menu.Add("Демонстрация экрана")
	var hasCapture atomic.Bool
	refreshCapture := func() {
		devices, err := nativeaudio.ListDevices()
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
			c.Show()
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
			if c.quitting.Load() {
				return
			}
			refreshCapture()
			syncMenu()
		}
	}()
	menu.AddSeparator()
	menu.Add("Выход").OnClick(func(*application.Context) {
		c.quitting.Store(true)
		app.Quit()
	})

	tray := app.SystemTray.New()
	tray.SetIcon(icon)
	tray.SetTooltip("Sonoryx")
	tray.SetMenu(menu)
	tray.OnClick(func() { c.Show() })
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

func screenShareReady(view clientapp.ClientViewState) bool {
	return view.ConnectionStatus == clientapp.ConnectionConnected && view.ChannelID != 0
}

func screenShareActive(view clientapp.ClientViewState) bool {
	for _, stream := range view.ScreenStreams {
		if stream.OwnerSessionID == view.SessionID {
			return true
		}
	}
	return false
}
