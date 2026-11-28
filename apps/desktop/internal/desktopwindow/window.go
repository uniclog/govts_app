package desktopwindow

import (
	"sync"
	"sync/atomic"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	clientapp "uniclog.io/sonoryx/internal/clientcore"
)

type windowService interface {
	CloseToTray() bool
	SetHotkeyWindowInactive(bool)
	SetMuted(bool)
	SetDeafened(bool) error
}

type Controller struct {
	window   atomic.Pointer[application.WebviewWindow]
	quitting atomic.Bool
	// The hotkey handler reads this without asking the UI thread, which may
	// itself be waiting for the handler during shutdown.
	hidden atomic.Bool
}

func (c *Controller) Hidden() bool { return c.hidden.Load() }
func (c *Controller) Close()       { c.quitting.Store(true) }

func (c *Controller) Install(app *application.App, client *clientapp.App, service windowService, title string, icon []byte) {
	mainWindow := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            title,
		Frameless:        true,
		Width:            1180,
		Height:           760,
		MinWidth:         900,
		MinHeight:        680,
		BackgroundColour: application.NewRGB(28, 37, 57),
		URL:              "/",
		// Without this the Wails runtime answers every external file drag with "no drop".
		EnableFileDrop: true,
	})
	c.window.Store(mainWindow)
	// Cancelling the close keeps the process alive. The default listener otherwise
	// destroys the window and quits the client.
	mainWindow.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		if c.quitting.Load() {
			return
		}
		if service.CloseToTray() {
			event.Cancel()
			mainWindow.Hide()
			c.hidden.Store(true)
			return
		}
		c.quitting.Store(true)
		app.Quit()
	})
	c.installTray(app, client, service, icon)
	c.trackVisibility(mainWindow, service, func() { app.Event.Emit("main-window-shown", true) })
	fitWindowToScreen(mainWindow)
}

// fitWindowToScreen keeps the default window size inside the work area of a
// small screen, such as 1280x720 without the taskbar.
func fitWindowToScreen(window *application.WebviewWindow) {
	var once sync.Once
	window.OnWindowEvent(events.Common.WindowRuntimeReady, func(*application.WindowEvent) {
		once.Do(func() {
			screen, err := window.GetScreen()
			if err != nil || screen == nil {
				return
			}
			width, height := window.Size()
			fitWidth, fitHeight := min(width, screen.WorkArea.Width), min(height, screen.WorkArea.Height)
			if fitWidth != width || fitHeight != height {
				window.SetSize(fitWidth, fitHeight)
				window.Center()
			}
		})
	})
}

// shown runs when the window comes back from the tray or the taskbar, or a
// second launch from the Start menu brings it forward; the interface then
// refreshes the saved server statuses (at most once a minute).
func (c *Controller) trackVisibility(window *application.WebviewWindow, service windowService, shown func()) {
	hide := func(*application.WindowEvent) {
		c.hidden.Store(true)
		service.SetHotkeyWindowInactive(true)
	}
	show := func(*application.WindowEvent) {
		c.hidden.Store(false)
		// The hook runs on the UI thread; the event must not wait for it.
		go shown()
	}
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

func (c *Controller) Show() {
	window := c.window.Load()
	if window == nil {
		return
	}
	window.UnMinimise()
	window.Show()
	window.Focus()
	c.hidden.Store(false)
}
