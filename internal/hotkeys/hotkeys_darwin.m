//go:build darwin

// Carbon hot keys for the macOS backend; the Go side is hotkeys_darwin.go.
// RegisterEventHotKey needs no Accessibility or Input Monitoring permission,
// matches physical key codes (kVK_*) in every layout and swallows the chord
// system-wide, like the Windows low-level hook.

#include <Carbon/Carbon.h>
#include <dispatch/dispatch.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdlib.h>
#include "hotkeys_darwin.h"

extern void srxHotkeyPressed(uint32_t id);

static const OSType srxSignature = 'SRXH';
static EventHandlerRef srxHandler = NULL;
static EventHotKeyRef srxRefs[SRX_MAX_HOTKEYS];
static int srxCount = 0;

static OSStatus srxOnHotKey(EventHandlerCallRef next, EventRef event, void *data) {
	EventHotKeyID hotKey;
	if (GetEventParameter(event, kEventParamDirectObject, typeEventHotKeyID, NULL, sizeof(hotKey), NULL, &hotKey) != noErr) {
		return eventNotHandledErr;
	}
	if (hotKey.signature != srxSignature) return eventNotHandledErr;
	srxHotkeyPressed(hotKey.id);
	return noErr;
}

// A job runs at most once and only while its caller still waits for it:
// pending → running → done, or pending → abandoned when the wait timed out.
// An abandoned job never registers anything, so a timeout reported to the
// caller stays true and a later apply cannot be overwritten by it.
enum { srxPending = 0, srxRunning = 1, srxDone = 2, srxAbandoned = 3 };

typedef struct {
	uint32_t codes[SRX_MAX_HOTKEYS];
	uint32_t modifiers[SRX_MAX_HOTKEYS];
	int32_t status[SRX_MAX_HOTKEYS];
	int count;
	atomic_int state;
} srxJob;

// srxApplyOnMain replaces every registration. It runs on the main thread,
// where Carbon delivers the hot key events.
static void srxApplyOnMain(srxJob *job) {
	for (int i = 0; i < srxCount; i++) {
		if (srxRefs[i] != NULL) UnregisterEventHotKey(srxRefs[i]);
		srxRefs[i] = NULL;
	}
	srxCount = 0;
	if (job->count > 0 && srxHandler == NULL) {
		EventTypeSpec spec = {kEventClassKeyboard, kEventHotKeyPressed};
		OSStatus status = InstallApplicationEventHandler(NewEventHandlerUPP(srxOnHotKey), 1, &spec, NULL, &srxHandler);
		if (status != noErr) {
			for (int i = 0; i < job->count; i++) job->status[i] = (int32_t)status;
			return;
		}
	}
	for (int i = 0; i < job->count; i++) {
		EventHotKeyID id = {srxSignature, (UInt32)(i + 1)};
		EventHotKeyRef ref = NULL;
		// macOS 15+ rejects chords whose only modifiers are Option or
		// Option+Shift; that status reaches the UI as a failed binding.
		OSStatus status = RegisterEventHotKey(job->codes[i], job->modifiers[i], id, GetApplicationEventTarget(), 0, &ref);
		job->status[i] = (int32_t)status;
		srxRefs[i] = status == noErr ? ref : NULL;
	}
	srxCount = job->count;
}

int srxHotkeysApply(const uint32_t *codes, const uint32_t *modifiers, int count, int32_t *status, int timeoutMS) {
	if (count < 0 || count > SRX_MAX_HOTKEYS) return -2;
	srxJob *job = calloc(1, sizeof(srxJob));
	if (job == NULL) return -3;
	job->count = count;
	for (int i = 0; i < count; i++) {
		job->codes[i] = codes[i];
		job->modifiers[i] = modifiers[i];
	}
	if (pthread_main_np()) {
		srxApplyOnMain(job);
		for (int i = 0; i < count; i++) status[i] = job->status[i];
		free(job);
		return 0;
	}
	// Before NSApplication runs, or while it shuts down, the main queue may
	// never drain. Waiting with a timeout keeps the caller from hanging.
	dispatch_semaphore_t done = dispatch_semaphore_create(0);
	dispatch_async(dispatch_get_main_queue(), ^{
		int expected = srxPending;
		if (!atomic_compare_exchange_strong(&job->state, &expected, srxRunning)) {
			free(job); // abandoned by a caller that timed out
			return;
		}
		srxApplyOnMain(job);
		atomic_store(&job->state, srxDone);
		dispatch_semaphore_signal(done);
	});
	if (dispatch_semaphore_wait(done, dispatch_time(DISPATCH_TIME_NOW, (int64_t)timeoutMS * NSEC_PER_MSEC)) != 0) {
		int expected = srxPending;
		if (atomic_compare_exchange_strong(&job->state, &expected, srxAbandoned)) {
			return -1;
		}
		// Already running on the main thread: it finishes promptly.
		dispatch_semaphore_wait(done, DISPATCH_TIME_FOREVER);
	}
	for (int i = 0; i < count; i++) status[i] = job->status[i];
	free(job);
	return 0;
}
