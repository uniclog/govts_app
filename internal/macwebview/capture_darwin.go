//go:build darwin

// Package macwebview adjusts the WKWebView that Wails creates on macOS.
package macwebview

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>

// WKWebView hides navigator.mediaDevices unless the app opts in, so
// getDisplayMedia is undefined. These WebKit preference keys have no public
// setter; KVC on WKPreferences reaches them, takes effect on the loaded page
// and is ignored (caught) by a WebKit that no longer knows a key.
static int sonoryxEnableCapture(void *nsWindow) {
	NSWindow *window = (__bridge NSWindow *)nsWindow;
	WKWebView *webView = nil;
	@try {
		id value = [window valueForKey:@"webView"];
		if ([value isKindOfClass:[WKWebView class]]) webView = value;
	} @catch (NSException *e) {}
	if (webView == nil) return -1;
	int applied = 0;
	for (NSString *key in @[@"mediaDevicesEnabled", @"screenCaptureEnabled"]) {
		@try {
			[webView.configuration.preferences setValue:@YES forKey:key];
			applied++;
		} @catch (NSException *e) {}
	}
	return applied;
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// EnableScreenCapture exposes navigator.mediaDevices (getDisplayMedia) in the
// WKWebView of a Wails NSWindow. It must run on the main thread.
func EnableScreenCapture(nsWindow unsafe.Pointer) error {
	if nsWindow == nil {
		return fmt.Errorf("native window is not ready")
	}
	switch applied := C.sonoryxEnableCapture(nsWindow); {
	case applied < 0:
		return fmt.Errorf("window has no WKWebView")
	case applied < 2:
		return fmt.Errorf("WebKit accepted %d of 2 capture preferences", int(applied))
	}
	return nil
}
