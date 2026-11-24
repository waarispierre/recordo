// Cursor and click capture.
//
// ScreenCaptureKit exposes no per-frame cursor metadata, so cursor state is captured out
// of band here and correlated with frames by mach timestamp. Two sources, deliberately:
//   - Position is polled at a fixed rate. This needs no TCC permission, yields evenly
//     spaced samples (much easier for the camera model to smooth than irregular event
//     arrivals), and keeps producing samples while the mouse is stationary.
//   - Clicks come from a CGEventTap, which is the only way to see them and requires Input
//     Monitoring permission. Its mask has no keyboard event type, so it cannot observe
//     keystrokes.
// The tap failing degrades the recording to "pan but never click-zoom" rather than losing
// cursor tracking entirely.
#import <ApplicationServices/ApplicationServices.h>
#include <mach/mach_time.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include "bridge.h"
#include "trace.h"

static pthread_mutex_t mu = PTHREAD_MUTEX_INITIALIZER;
static rc_event *events;
static int n_events, cap_events;
static atomic_bool stopping;
static atomic_bool tap_installed;
static atomic_int tap_reenabled;
static uint32_t poll_hz;
static pthread_t poller, tapper;
static CFMachPortRef tap;

static void push(uint64_t t, int kind, double x, double y) {
	pthread_mutex_lock(&mu);
	if (n_events == cap_events) {
		cap_events = cap_events ? cap_events * 2 : 4096;
		events = realloc(events, (size_t)cap_events * sizeof(rc_event));
	}
	events[n_events++] = (rc_event){t, kind, x, y};
	pthread_mutex_unlock(&mu);
}

static void *poll_loop(void *arg) {
	(void)arg;
	useconds_t interval = 1000000 / poll_hz;
	bool have_last = false;
	CGPoint last = {0};
	while (!atomic_load(&stopping)) {
		@autoreleasepool {
			// Reading the location from a fresh event needs no permission at all.
			CGEventRef e = CGEventCreate(NULL);
			if (e != NULL) {
				CGPoint p = CGEventGetLocation(e);
				CFRelease(e);
				// Skip duplicates while the cursor is parked: the camera holds the previous
				// sample, and idle recordings stay small. A gap means "stationary".
				if (!have_last || p.x != last.x || p.y != last.y) {
					have_last = true;
					last = p;
					push(mach_absolute_time(), 0, p.x, p.y);
				}
			}
		}
		usleep(interval);
	}
	return NULL;
}

static CGEventRef on_event(CGEventTapProxy proxy, CGEventType type, CGEventRef event, void *info) {
	(void)proxy;
	(void)info;
	int kind;
	switch (type) {
	case kCGEventLeftMouseDown:
	case kCGEventRightMouseDown:
	case kCGEventOtherMouseDown:
		kind = 1;
		break;
	case kCGEventLeftMouseUp:
	case kCGEventRightMouseUp:
	case kCGEventOtherMouseUp:
		kind = 2;
		break;
	case kCGEventScrollWheel:
		kind = 3;
		break;
	case kCGEventTapDisabledByTimeout:
	case kCGEventTapDisabledByUserInput:
		// A disabled tap stops delivering until re-armed, which would silently truncate
		// the clicks. Re-arm it and count it so the caller can say so.
		CGEventTapEnable(tap, true);
		atomic_fetch_add(&tap_reenabled, 1);
		return event;
	default:
		return event;
	}
	// The event's own hardware timestamp is more accurate than reading the clock whenever
	// this callback happens to run.
	uint64_t t = CGEventGetTimestamp(event);
	if (t == 0) t = mach_absolute_time();
	CGPoint p = CGEventGetLocation(event);
	push(t, kind, p.x, p.y);
	return event;
}

static void *tap_loop(void *arg) {
	(void)arg;
	CGEventMask mask = CGEventMaskBit(kCGEventLeftMouseDown) | CGEventMaskBit(kCGEventRightMouseDown) |
	                   CGEventMaskBit(kCGEventOtherMouseDown) | CGEventMaskBit(kCGEventLeftMouseUp) |
	                   CGEventMaskBit(kCGEventRightMouseUp) | CGEventMaskBit(kCGEventOtherMouseUp) |
	                   CGEventMaskBit(kCGEventScrollWheel);
	// ListenOnly, so it can never delay or swallow the user's real input.
	tap = CGEventTapCreate(kCGHIDEventTap, kCGHeadInsertEventTap, kCGEventTapOptionListenOnly, mask, on_event, NULL);
	if (tap == NULL) return NULL;
	CFRunLoopSourceRef source = CFMachPortCreateRunLoopSource(NULL, tap, 0);
	if (source == NULL) {
		CFRelease(tap);
		tap = NULL;
		return NULL;
	}
	CFRunLoopAddSource(CFRunLoopGetCurrent(), source, kCFRunLoopCommonModes);
	CGEventTapEnable(tap, true);
	atomic_store(&tap_installed, true);
	// Run in slices rather than forever, so the stop flag is noticed promptly.
	while (!atomic_load(&stopping)) {
		CFRunLoopRunInMode(kCFRunLoopDefaultMode, 0.1, false);
	}
	CGEventTapEnable(tap, false);
	CFRunLoopRemoveSource(CFRunLoopGetCurrent(), source, kCFRunLoopCommonModes);
	CFRelease(source);
	CFRelease(tap);
	tap = NULL;
	return NULL;
}

void rc_telemetry_start(uint32_t hz) {
	pthread_mutex_lock(&mu);
	free(events);
	events = NULL;
	n_events = cap_events = 0;
	pthread_mutex_unlock(&mu);
	poll_hz = hz ? hz : 120;
	atomic_store(&stopping, false);
	atomic_store(&tap_installed, false);
	atomic_store(&tap_reenabled, 0);
	pthread_create(&poller, NULL, poll_loop, NULL);
	pthread_create(&tapper, NULL, tap_loop, NULL);
}

rc_telemetry rc_telemetry_stop(void) {
	atomic_store(&stopping, true);
	RC_TRACE("telemetry_stop: joining poller");
	pthread_join(poller, NULL);
	RC_TRACE("telemetry_stop: joining tap");
	pthread_join(tapper, NULL);
	RC_TRACE("telemetry_stop: done");
	pthread_mutex_lock(&mu);
	rc_telemetry out = {events, n_events, atomic_load(&tap_installed), atomic_load(&tap_reenabled)};
	events = NULL;
	n_events = cap_events = 0;
	pthread_mutex_unlock(&mu);
	return out;
}

void rc_telemetry_free(rc_telemetry t) { free(t.events); }
