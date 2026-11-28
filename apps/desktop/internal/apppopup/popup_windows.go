//go:build windows

package apppopup

import (
	"bytes"
	"errors"
	"image"
	"image/draw"
	"image/png"
	"log"
	"runtime"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/w32"
	"golang.org/x/sys/windows"
)

const (
	wmPopupUpdate   = w32.WM_APP + 1
	popupTimerID    = 1
	defaultLifetime = 2800 * time.Millisecond
	// BLENDFUNCTION{AC_SRC_OVER, 0, 255, AC_SRC_ALPHA} passed by value.
	blendSourceAlpha = 0x01ff0000
)

var (
	popupClassName = windows.StringToUTF16Ptr("SonoryxAppPopup")
	popupWndProc   = windows.NewCallback(popupWindowProc)
	popupWindows   sync.Map // HWND -> *window; no Go pointers in Win32 userdata.
	popupClassOnce sync.Once
	popupClassErr  error
	alphaBlend     = windows.NewLazySystemDLL("msimg32.dll").NewProc("AlphaBlend")
)

// window owns the native window and its message-loop thread.
type window struct {
	mu      sync.Mutex
	closed  bool
	pending bool
	latest  Message
	look    Look
	hwnd    w32.HWND
	done    chan struct{}

	// The following fields belong exclusively to the popup thread.
	current  Message
	dpi      int
	deadline time.Time
	source   *image.RGBA // decoded application mark, premultiplied
	icon     w32.HBITMAP // source scaled to iconSize
	iconSize int
}

func newBackend(icon []byte) (backend, error) {
	p := &window{done: make(chan struct{}), dpi: 96, look: ParseLook(nil), source: decodeIcon(icon)}
	ready := make(chan error, 1)
	go p.run(ready)
	if err := <-ready; err != nil {
		<-p.done
		return nil, err
	}
	return p, nil
}

func (p *window) run(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(p.done)
	instance := w32.GetModuleHandle("")
	popupClassOnce.Do(func() {
		class := w32.WNDCLASSEX{
			Size:    uint32(unsafe.Sizeof(w32.WNDCLASSEX{})),
			WndProc: popupWndProc, Instance: instance, ClassName: popupClassName,
			Cursor: w32.LoadCursorWithResourceID(0, w32.IDC_ARROW),
		}
		if w32.RegisterClassEx(&class) == 0 {
			popupClassErr = errors.New("register app popup window class failed")
		}
	})
	if popupClassErr != nil {
		ready <- popupClassErr
		return
	}
	p.hwnd = w32.CreateWindowEx(
		w32.WS_EX_NOACTIVATE|w32.WS_EX_TOOLWINDOW|w32.WS_EX_TOPMOST,
		popupClassName, windows.StringToUTF16Ptr("Sonoryx"), w32.WS_POPUP,
		0, 0, 320, 80, 0, 0, instance, nil,
	)
	if p.hwnd == 0 {
		ready <- errors.New("create app popup window failed")
		return
	}
	popupWindows.Store(p.hwnd, p)
	defer popupWindows.Delete(p.hwnd)
	defer w32.DestroyWindow(p.hwnd)
	defer p.releaseIcon()
	ready <- nil
	var msg w32.MSG
	for {
		// GetMessage returns a signed 32-bit BOOL, even in a 64-bit process.
		result := int32(w32.GetMessage(&msg, 0, 0, 0))
		if result <= 0 {
			if result < 0 {
				log.Printf("app popup message loop failed")
			}
			return
		}
		w32.TranslateMessage(&msg)
		w32.DispatchMessage(&msg)
	}
}

func (p *window) show(message Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.done:
		return errors.New("app popup stopped")
	default:
	}
	if p.closed {
		return nil
	}
	p.latest = message
	if !p.pending {
		if !w32.PostMessage(p.hwnd, wmPopupUpdate, 0, 0) {
			return errors.New("post app popup update failed")
		}
		p.pending = true
	}
	return nil
}

func (p *window) setLook(look Look) {
	p.mu.Lock()
	p.look = look
	p.mu.Unlock()
}

func (p *window) close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	posted := w32.PostMessage(p.hwnd, w32.WM_CLOSE, 0, 0)
	p.mu.Unlock()
	if posted {
		<-p.done
	}
}

func popupWindowProc(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
	handle := w32.HWND(hwnd)
	value, ok := popupWindows.Load(handle)
	if !ok {
		return w32.DefWindowProc(handle, msg, wp, lp)
	}
	p := value.(*window)
	switch msg {
	case wmPopupUpdate:
		p.mu.Lock()
		p.current, p.pending = p.latest, false
		closed := p.closed
		p.mu.Unlock()
		if !closed {
			p.display()
		}
		return 0
	case w32.WM_TIMER:
		if wp == popupTimerID && !time.Now().Before(p.deadline) {
			p.hide()
		}
		return 0
	case w32.WM_LBUTTONUP:
		p.hide()
		if click := p.current.OnClick; click != nil {
			go click()
		}
		return 0
	case w32.WM_MOUSEACTIVATE:
		return 3 // MA_NOACTIVATE: clicking dismisses without stealing focus.
	case w32.WM_PAINT:
		p.paint()
		return 0
	case w32.WM_ERASEBKGND:
		return 1 // paint fills the complete client area.
	case w32.WM_CLOSE:
		p.hide()
		w32.PostQuitMessage(0)
		return 0
	}
	return w32.DefWindowProc(handle, msg, wp, lp)
}

func (p *window) px(value int) int { return (value*p.dpi + 48) / 96 }

func (p *window) display() {
	// Follow the monitor the user is working on; respect taskbars on any edge.
	foreground := w32.GetForegroundWindow()
	monitor := w32.MonitorFromWindow(foreground, w32.MONITOR_DEFAULTTOPRIMARY)
	info := w32.MONITORINFO{CbSize: uint32(unsafe.Sizeof(w32.MONITORINFO{}))}
	if !w32.GetMonitorInfo(monitor, &info) {
		return
	}
	p.dpi = int(w32.GetDpiForWindow(foreground))
	if p.dpi == 0 {
		p.dpi = 96
	}
	width := min(p.px(320), int(info.RcWork.Right-info.RcWork.Left))
	height := min(p.px(80), int(info.RcWork.Bottom-info.RcWork.Top))
	margin := p.px(20)
	x := max(int(info.RcWork.Left), int(info.RcWork.Right)-width-margin)
	y := max(int(info.RcWork.Top), int(info.RcWork.Bottom)-height-margin)
	w32.SetWindowPos(p.hwnd, w32.HWND_TOPMOST, x, y, width, height, w32.SWP_NOACTIVATE)
	p.prepareIcon(p.px(48))
	lifetime := p.current.Lifetime
	if lifetime <= 0 {
		lifetime = defaultLifetime
	}
	p.deadline = time.Now().Add(lifetime)
	if w32.SetTimer(p.hwnd, popupTimerID, uint32(lifetime/time.Millisecond), 0) == 0 {
		// Never leave an undismissed popup if the timer could not be created.
		p.hide()
		log.Printf("app popup timer failed")
		return
	}
	w32.InvalidateRect(p.hwnd, nil, false)
	w32.ShowWindow(p.hwnd, w32.SW_SHOWNOACTIVATE)
	w32.UpdateWindow(p.hwnd)
}

func (p *window) hide() {
	w32.KillTimer(p.hwnd, popupTimerID)
	w32.ShowWindow(p.hwnd, w32.SW_HIDE)
}

func (p *window) paint() {
	var ps w32.PAINTSTRUCT
	dc := w32.BeginPaint(p.hwnd, &ps)
	defer w32.EndPaint(p.hwnd, &ps)
	if dc == 0 {
		return
	}
	p.mu.Lock()
	look := p.look
	p.mu.Unlock()
	rect := w32.GetClientRect(p.hwnd)
	border := w32.CreateSolidBrush(colorRef(look.Line))
	background := w32.CreateSolidBrush(colorRef(look.Card))
	defer w32.DeleteObject(w32.HGDIOBJ(border))
	defer w32.DeleteObject(w32.HGDIOBJ(background))
	w32.FillRect(dc, rect, border)
	inner := w32.RECT{Left: 1, Top: 1, Right: rect.Right - 1, Bottom: rect.Bottom - 1}
	w32.FillRect(dc, &inner, background)
	w32.SetBkMode(dc, w32.TRANSPARENT)

	pad := int32(p.px(14))
	icon := int32(p.px(48))
	p.drawIcon(dc, pad, (rect.Bottom-icon)/2)

	// Decorative: a click anywhere on the card dismisses it.
	closeSize, closeInset := int32(p.px(20)), int32(p.px(8))
	closeMark := w32.RECT{
		Left: rect.Right - closeInset - closeSize, Top: closeInset,
		Right: rect.Right - closeInset, Bottom: closeInset + closeSize,
	}
	p.drawText(dc, "×", 13, w32.FW_NORMAL, look.Muted, closeMark, w32.DT_CENTER|w32.DT_VCENTER|w32.DT_SINGLELINE)

	left, right := pad+icon+int32(p.px(12)), closeMark.Left-int32(p.px(8))
	p.drawText(dc, p.current.Title, 16, w32.FW_SEMIBOLD, look.Text,
		w32.RECT{Left: left, Top: int32(p.px(16)), Right: right, Bottom: int32(p.px(40))}, w32.DT_SINGLELINE)
	p.drawText(dc, p.current.Body, 12, w32.FW_NORMAL, look.Muted,
		w32.RECT{Left: left, Top: int32(p.px(42)), Right: right, Bottom: int32(p.px(60))}, w32.DT_SINGLELINE)
}

func (p *window) drawText(dc w32.HDC, text string, size, weight int, colour uint32, rect w32.RECT, flags uint32) {
	fontSpec := w32.LOGFONT{Height: -int32(p.px(size)), Weight: int32(weight), Quality: w32.CLEARTYPE_QUALITY}
	copy(fontSpec.FaceName[:], utf16.Encode([]rune("Segoe UI")))
	font := w32.CreateFontIndirect(&fontSpec)
	if font != 0 {
		previous := w32.SelectObject(dc, w32.HGDIOBJ(font))
		defer w32.DeleteObject(w32.HGDIOBJ(font))
		defer w32.SelectObject(dc, previous)
	}
	w32.SetTextColor(dc, colorRef(colour))
	// Text goes directly to GDI, without an HTML template or script interpreter.
	encoded := utf16.Encode([]rune(text))
	if len(encoded) != 0 {
		w32.DrawText(dc, encoded, len(encoded), &rect, flags|w32.DT_NOPREFIX|w32.DT_END_ELLIPSIS)
	}
}

// colorRef converts 0xRRGGBB to a GDI COLORREF (0x00BBGGRR).
func colorRef(rgb uint32) w32.COLORREF {
	return w32.COLORREF(w32.RGB(byte(rgb>>16), byte(rgb>>8), byte(rgb)))
}

// drawIcon blends the prepared icon with its alpha channel, so the transparent
// corners of the mark show the card colour.
func (p *window) drawIcon(dc w32.HDC, x, y int32) {
	if p.icon == 0 {
		return
	}
	mem := w32.CreateCompatibleDC(dc)
	if mem == 0 {
		return
	}
	previous := w32.SelectObject(mem, w32.HGDIOBJ(p.icon))
	size := uintptr(p.iconSize)
	// A failed blend only leaves the card without its mark.
	_, _, _ = alphaBlend.Call(uintptr(dc), uintptr(x), uintptr(y), size, size, uintptr(mem), 0, 0, size, size, blendSourceAlpha)
	w32.SelectObject(mem, previous)
	w32.DeleteDC(mem)
}

// prepareIcon scales the mark once per DPI instead of stretching it on every paint.
func (p *window) prepareIcon(size int) {
	if p.source == nil || size <= 0 || size == p.iconSize {
		return
	}
	p.releaseIcon()
	bitmap, err := w32.CreateHBITMAPFromImage(scaleIcon(p.source, size))
	if err != nil || bitmap == 0 {
		log.Printf("app popup icon bitmap failed")
		return
	}
	p.icon, p.iconSize = bitmap, size
}

func (p *window) releaseIcon() {
	if p.icon != 0 {
		w32.DeleteObject(w32.HGDIOBJ(p.icon))
		p.icon, p.iconSize = 0, 0
	}
}

func decodeIcon(data []byte) *image.RGBA {
	if len(data) == 0 {
		return nil
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		log.Printf("app popup icon: %v", err)
		return nil
	}
	bounds := decoded.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(rgba, rgba.Bounds(), decoded, bounds.Min, draw.Src)
	return rgba
}

// scaleIcon averages premultiplied source pixels into a size×size image.
func scaleIcon(src *image.RGBA, size int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	for y := 0; y < size; y++ {
		y0 := y * h / size
		y1 := max((y+1)*h/size, y0+1)
		for x := 0; x < size; x++ {
			x0 := x * w / size
			x1 := max((x+1)*w/size, x0+1)
			var sum [4]int
			for sy := y0; sy < y1; sy++ {
				row := src.Pix[src.PixOffset(x0, sy):src.PixOffset(x1, sy)]
				for i := 0; i < len(row); i += 4 {
					sum[0] += int(row[i])
					sum[1] += int(row[i+1])
					sum[2] += int(row[i+2])
					sum[3] += int(row[i+3])
				}
			}
			n := (y1 - y0) * (x1 - x0)
			o := dst.PixOffset(x, y)
			for c := range sum {
				dst.Pix[o+c] = uint8(sum[c] / n)
			}
		}
	}
	return dst
}
