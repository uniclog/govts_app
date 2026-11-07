//go:build darwin

// Package appnap keeps macOS from napping the client. A voice client must
// keep its 20 ms mixer and capture clocks while its window is hidden in the
// menu bar or another app is in front; App Nap and timer coalescing would
// otherwise stretch those ticks and cut voice and app sounds.
package appnap

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation
#import <Foundation/Foundation.h>

static id srxActivity = nil;

static int srxDisableAppNap(void) {
	if (srxActivity != nil) return 1;
	NSActivityOptions options = NSActivityUserInitiatedAllowingIdleSystemSleep | NSActivityLatencyCritical;
	srxActivity = [[NSProcessInfo processInfo] beginActivityWithOptions:options reason:@"Voice chat audio"];
	return srxActivity != nil;
}
*/
import "C"

import "errors"

// Disable holds a latency-critical activity for the rest of the process.
// The system may still sleep when idle; the client is never napped.
func Disable() error {
	if C.srxDisableAppNap() == 0 {
		return errors.New("NSProcessInfo refused the activity")
	}
	return nil
}
