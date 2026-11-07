//go:build darwin

// Core Audio process tap for stream sound; the Go side is loopback_darwin.go.

#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <stdio.h>
#include <unistd.h>
#include "loopback_darwin.h"

#import <AppKit/AppKit.h>
#import <CoreAudio/CoreAudio.h>
#import <CoreAudio/CATapDescription.h>
#import <CoreAudio/AudioHardwareTapping.h>

extern void srxLoopbackPush(uintptr_t handle, float *left, float *right, int frames, int stride);
extern void srxLoopbackRate(uintptr_t handle, double rate);
extern void srxLoopbackOutputChanged(uintptr_t id);

typedef struct {
	void *block;
	void *queue;
} srxWatch;

// srxWatchDefaultOutput calls srxLoopbackOutputChanged(id) when the default
// output changes: the aggregate's clock device may be gone, which stops the
// IO proc without an error.
void *srxWatchDefaultOutput(uintptr_t id) {
	srxWatch *watch = calloc(1, sizeof(srxWatch));
	if (watch == NULL) return NULL;
	dispatch_queue_t queue = dispatch_queue_create("app.sonoryx.stream-sound-output", DISPATCH_QUEUE_SERIAL);
	AudioObjectPropertyListenerBlock block = ^(UInt32 count, const AudioObjectPropertyAddress *addresses) {
		srxLoopbackOutputChanged(id);
	};
	AudioObjectPropertyAddress address = {kAudioHardwarePropertyDefaultOutputDevice, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
	if (AudioObjectAddPropertyListenerBlock(kAudioObjectSystemObject, &address, queue, block) != noErr) {
		free(watch);
		return NULL;
	}
	watch->block = (__bridge_retained void *)block;
	watch->queue = (__bridge_retained void *)queue;
	return watch;
}

void srxUnwatchDefaultOutput(void *token) {
	if (token == NULL) return;
	srxWatch *watch = token;
	AudioObjectPropertyListenerBlock block = (__bridge_transfer AudioObjectPropertyListenerBlock)watch->block;
	dispatch_queue_t queue = (__bridge_transfer dispatch_queue_t)watch->queue;
	AudioObjectPropertyAddress address = {kAudioHardwarePropertyDefaultOutputDevice, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
	AudioObjectRemovePropertyListenerBlock(kAudioObjectSystemObject, &address, queue, block);
	free(watch);
}

int srxTapAvailable(void) {
	if (@available(macOS 14.2, *)) return 1;
	return 0;
}

static void srxFail(srxTapResult *result, OSStatus status, const char *step) {
	result->status = status == 0 ? -1 : (int)status;
	snprintf(result->message, sizeof(result->message), "%s: OSStatus %d", step, (int)status);
}

static AudioObjectID srxProcessObject(pid_t pid) {
	AudioObjectPropertyAddress address = {kAudioHardwarePropertyTranslatePIDToProcessObject, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
	AudioObjectID object = kAudioObjectUnknown;
	UInt32 size = sizeof(object);
	if (AudioObjectGetPropertyData(kAudioObjectSystemObject, &address, sizeof(pid), &pid, &size, &object) != noErr) return kAudioObjectUnknown;
	return object;
}

// srxAppProcessObjects returns the audio process objects of an app and its
// helpers. Browsers and Electron apps play from helper processes whose
// bundle id extends the app's (com.google.Chrome.helper).
static NSArray<NSNumber *> *srxAppProcessObjects(pid_t pid, NSString *bundleID) {
	NSMutableArray<NSNumber *> *objects = [NSMutableArray array];
	AudioObjectPropertyAddress listAddress = {kAudioHardwarePropertyProcessObjectList, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
	UInt32 size = 0;
	if (AudioObjectGetPropertyDataSize(kAudioObjectSystemObject, &listAddress, 0, NULL, &size) != noErr || size == 0) return objects;
	UInt32 count = size / sizeof(AudioObjectID);
	AudioObjectID *list = calloc(count, sizeof(AudioObjectID));
	if (list == NULL) return objects;
	if (AudioObjectGetPropertyData(kAudioObjectSystemObject, &listAddress, 0, NULL, &size, list) == noErr) {
		count = size / sizeof(AudioObjectID);
		NSString *helperPrefix = bundleID.length ? [bundleID stringByAppendingString:@"."] : nil;
		for (UInt32 i = 0; i < count; i++) {
			pid_t processPID = -1;
			UInt32 pidSize = sizeof(processPID);
			AudioObjectPropertyAddress pidAddress = {kAudioProcessPropertyPID, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
			AudioObjectGetPropertyData(list[i], &pidAddress, 0, NULL, &pidSize, &processPID);
			BOOL match = processPID == pid;
			if (!match && helperPrefix != nil) {
				CFStringRef processBundle = NULL;
				UInt32 bundleSize = sizeof(processBundle);
				AudioObjectPropertyAddress bundleAddress = {kAudioProcessPropertyBundleID, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
				if (AudioObjectGetPropertyData(list[i], &bundleAddress, 0, NULL, &bundleSize, &processBundle) == noErr && processBundle != NULL) {
					NSString *value = (__bridge_transfer NSString *)processBundle;
					match = [value hasPrefix:helperPrefix];
				}
			}
			if (match) [objects addObject:@(list[i])];
		}
	}
	free(list);
	return objects;
}

// The aggregate is clocked by the output the tapped apps play to: the
// default output, not the alert-sound ("system output") device.
static const AudioObjectPropertyAddress srxDefaultOutputAddress = {kAudioHardwarePropertyDefaultOutputDevice, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};

static NSString *srxDefaultOutputUID(void) {
	AudioObjectPropertyAddress address = srxDefaultOutputAddress;
	AudioDeviceID device = kAudioObjectUnknown;
	UInt32 size = sizeof(device);
	if (AudioObjectGetPropertyData(kAudioObjectSystemObject, &address, 0, NULL, &size, &device) != noErr || device == kAudioObjectUnknown) return nil;
	AudioObjectPropertyAddress uidAddress = {kAudioDevicePropertyDeviceUID, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
	CFStringRef uid = NULL;
	size = sizeof(uid);
	if (AudioObjectGetPropertyData(device, &uidAddress, 0, NULL, &size, &uid) != noErr || uid == NULL) return nil;
	return (__bridge_transfer NSString *)uid;
}

void srxTapStop(uint32_t tap, uint32_t aggregate, void *proc) {
	if (@available(macOS 14.2, *)) {
		if (aggregate != kAudioObjectUnknown) {
			if (proc != NULL) {
				AudioDeviceStop(aggregate, (AudioDeviceIOProcID)proc);
				AudioDeviceDestroyIOProcID(aggregate, (AudioDeviceIOProcID)proc);
			}
			AudioHardwareDestroyAggregateDevice(aggregate);
		}
		if (tap != kAudioObjectUnknown) AudioHardwareDestroyProcessTap(tap);
	}
}

void srxTapStart(int32_t pid, int exclude, uintptr_t handle, srxTapResult *result) {
	memset(result, 0, sizeof(*result));
	if (@available(macOS 14.2, *)) {
		@autoreleasepool {
			CATapDescription *description = nil;
			if (exclude) {
				AudioObjectID self = srxProcessObject(pid);
				NSArray<NSNumber *> *excluded = self == kAudioObjectUnknown ? @[] : @[@(self)];
				description = [[CATapDescription alloc] initStereoGlobalTapButExcludeProcesses:excluded];
				if (@available(macOS 26.0, *)) {
					NSString *bundle = [[NSBundle mainBundle] bundleIdentifier];
					if (bundle.length) description.bundleIDs = @[bundle];
				}
			} else {
				NSRunningApplication *app = [NSRunningApplication runningApplicationWithProcessIdentifier:pid];
				NSString *bundle = app.bundleIdentifier;
				NSArray<NSNumber *> *included = srxAppProcessObjects(pid, bundle);
				description = [[CATapDescription alloc] initStereoMixdownOfProcesses:included];
				BOOL byBundle = NO;
				if (@available(macOS 26.0, *)) {
					// Also follows helpers that start playing later.
					if (bundle.length) {
						description.bundleIDs = @[bundle];
						description.processRestoreEnabled = YES;
						byBundle = YES;
					}
				}
				if (included.count == 0 && !byBundle) {
					result->status = -2;
					snprintf(result->message, sizeof(result->message), "application is not playing sound");
					return;
				}
			}
			description.name = @"Sonoryx stream sound";
			description.privateTap = YES;
			description.muteBehavior = CATapUnmuted;

			AudioObjectID tap = kAudioObjectUnknown;
			OSStatus status = AudioHardwareCreateProcessTap(description, &tap);
			if (status != noErr || tap == kAudioObjectUnknown) {
				srxFail(result, status, "create process tap");
				return;
			}
			AudioStreamBasicDescription format = {0};
			UInt32 size = sizeof(format);
			AudioObjectPropertyAddress formatAddress = {kAudioTapPropertyFormat, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
			status = AudioObjectGetPropertyData(tap, &formatAddress, 0, NULL, &size, &format);
			if (status != noErr || format.mSampleRate <= 0) {
				srxFail(result, status, "read tap format");
				srxTapStop(tap, kAudioObjectUnknown, NULL);
				return;
			}
			if (format.mFormatID != kAudioFormatLinearPCM || !(format.mFormatFlags & kAudioFormatFlagIsFloat) || format.mBitsPerChannel != 32) {
				srxFail(result, -3, "unexpected tap sample format");
				srxTapStop(tap, kAudioObjectUnknown, NULL);
				return;
			}

			// The private aggregate device carries the tap; the default output
			// is its clock, as Apple's process tap sample does.
			NSMutableDictionary *aggregateDescription = [@{
				@kAudioAggregateDeviceNameKey: @"Sonoryx stream sound",
				@kAudioAggregateDeviceUIDKey: [[NSUUID UUID] UUIDString],
				@kAudioAggregateDeviceIsPrivateKey: @YES,
				@kAudioAggregateDeviceIsStackedKey: @NO,
				@kAudioAggregateDeviceTapAutoStartKey: @YES,
				@kAudioAggregateDeviceTapListKey: @[@{
					@kAudioSubTapUIDKey: description.UUID.UUIDString,
					@kAudioSubTapDriftCompensationKey: @YES,
				}],
			} mutableCopy];
			NSString *output = srxDefaultOutputUID();
			if (output != nil) {
				aggregateDescription[@kAudioAggregateDeviceMainSubDeviceKey] = output;
				aggregateDescription[@kAudioAggregateDeviceSubDeviceListKey] = @[@{@kAudioSubDeviceUIDKey: output}];
			}
			AudioObjectID aggregate = kAudioObjectUnknown;
			status = AudioHardwareCreateAggregateDevice((__bridge CFDictionaryRef)aggregateDescription, &aggregate);
			if (status != noErr || aggregate == kAudioObjectUnknown) {
				srxFail(result, status, "create aggregate device");
				srxTapStop(tap, kAudioObjectUnknown, NULL);
				return;
			}

			srxLoopbackRate(handle, format.mSampleRate);
			BOOL interleaved = !(format.mFormatFlags & kAudioFormatFlagIsNonInterleaved);
			dispatch_queue_t queue = dispatch_queue_create("app.sonoryx.stream-sound", DISPATCH_QUEUE_SERIAL);
			AudioDeviceIOProcID proc = NULL;
			status = AudioDeviceCreateIOProcIDWithBlock(&proc, aggregate, queue,
				^(const AudioTimeStamp *now, const AudioBufferList *input, const AudioTimeStamp *inputTime, AudioBufferList *output, const AudioTimeStamp *outputTime) {
					if (input == NULL || input->mNumberBuffers == 0) return;
					const AudioBuffer *first = &input->mBuffers[0];
					if (first->mData == NULL || first->mDataByteSize == 0) return;
					if (interleaved) {
						int channels = first->mNumberChannels > 0 ? (int)first->mNumberChannels : 1;
						int frames = (int)(first->mDataByteSize / (sizeof(float) * channels));
						float *data = (float *)first->mData;
						srxLoopbackPush(handle, data, channels > 1 ? data + 1 : data, frames, channels);
					} else {
						int frames = (int)(first->mDataByteSize / sizeof(float));
						float *left = (float *)first->mData;
						float *right = left;
						if (input->mNumberBuffers > 1 && input->mBuffers[1].mData != NULL && input->mBuffers[1].mDataByteSize >= first->mDataByteSize) right = (float *)input->mBuffers[1].mData;
						srxLoopbackPush(handle, left, right, frames, 1);
					}
				});
			if (status != noErr || proc == NULL) {
				srxFail(result, status, "create IO proc");
				srxTapStop(tap, aggregate, NULL);
				return;
			}
			status = AudioDeviceStart(aggregate, proc);
			if (status != noErr) {
				srxFail(result, status, "start aggregate device");
				srxTapStop(tap, aggregate, proc);
				return;
			}
			result->tap = tap;
			result->aggregate = aggregate;
			result->proc = (void *)proc;
			result->rate = format.mSampleRate;
		}
		return;
	}
	result->status = -4;
	snprintf(result->message, sizeof(result->message), "process taps need macOS 14.2");
}

int srxListApps(srxApp *apps, int capacity) {
	int count = 0;
	@autoreleasepool {
		pid_t self = getpid();
		for (NSRunningApplication *app in [[NSWorkspace sharedWorkspace] runningApplications]) {
			if (count >= capacity) break;
			if (app.activationPolicy != NSApplicationActivationPolicyRegular || app.processIdentifier == self || app.terminated) continue;
			NSString *name = app.localizedName ?: app.bundleIdentifier ?: @"";
			if (name.length == 0) continue;
			apps[count].pid = app.processIdentifier;
			strlcpy(apps[count].name, name.UTF8String, sizeof(apps[count].name));
			count++;
		}
	}
	return count;
}
