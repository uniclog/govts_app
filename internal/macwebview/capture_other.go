//go:build !darwin

package macwebview

import "unsafe"

// EnableScreenCapture is only needed for WKWebView; WebView2 exposes capture.
func EnableScreenCapture(unsafe.Pointer) error { return nil }
