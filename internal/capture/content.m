#import <ScreenCaptureKit/ScreenCaptureKit.h>
#include <stdlib.h>
#include <string.h>
#include "bridge.h"

static char *rc_strdup(NSString *s) {
	if (s == nil) return NULL;
	return strdup(s.UTF8String);
}

// ScreenCaptureKit answers on a private queue; a CLI has no run loop to wait on, so the
// call is made synchronous with a semaphore. The timeout keeps a wedged
// replayd from hanging the tool forever.
static SCShareableContent *fetch(NSError **error) {
	__block SCShareableContent *result = nil;
	__block NSError *err = nil;
	dispatch_semaphore_t done = dispatch_semaphore_create(0);
	[SCShareableContent getShareableContentExcludingDesktopWindows:YES
	                                           onScreenWindowsOnly:NO
	                                             completionHandler:^(SCShareableContent *c, NSError *e) {
		result = c;
		err = e;
		dispatch_semaphore_signal(done);
	}];
	if (dispatch_semaphore_wait(done, dispatch_time(DISPATCH_TIME_NOW, 10 * NSEC_PER_SEC)) != 0) {
		*error = [NSError errorWithDomain:@"recordo" code:1
		                         userInfo:@{NSLocalizedDescriptionKey: @"timed out waiting for ScreenCaptureKit"}];
		return nil;
	}
	*error = err;
	return result;
}

rc_content rc_shareable_content(void) {
	rc_content out = {0};
	@autoreleasepool {
		NSError *error = nil;
		SCShareableContent *content = fetch(&error);
		if (content == nil) {
			out.error = rc_strdup(error.localizedDescription ?: @"no shareable content");
			return out;
		}
		NSArray<SCWindow *> *windows = content.windows;
		out.n_windows = (int)windows.count;
		out.windows = calloc(windows.count ?: 1, sizeof(rc_window));
		for (NSUInteger i = 0; i < windows.count; i++) {
			SCWindow *w = windows[i];
			rc_window *o = &out.windows[i];
			o->id = w.windowID;
			o->layer = w.windowLayer;
			o->on_screen = w.onScreen;
			o->frame = (rc_rect){w.frame.origin.x, w.frame.origin.y, w.frame.size.width, w.frame.size.height};
			o->title = rc_strdup(w.title);
			SCRunningApplication *app = w.owningApplication;
			if (app != nil) {
				o->pid = app.processID;
				o->bundle_id = rc_strdup(app.bundleIdentifier);
				o->app_name = rc_strdup(app.applicationName);
			}
		}
		NSArray<SCDisplay *> *displays = content.displays;
		out.n_displays = (int)displays.count;
		out.displays = calloc(displays.count ?: 1, sizeof(rc_display));
		for (NSUInteger i = 0; i < displays.count; i++) {
			CGRect f = displays[i].frame;
			out.displays[i] = (rc_display){displays[i].displayID, (uint32_t)displays[i].width, (uint32_t)displays[i].height,
			                               {f.origin.x, f.origin.y, f.size.width, f.size.height}};
		}
	}
	return out;
}

void rc_content_free(rc_content c) {
	for (int i = 0; i < c.n_windows; i++) {
		free(c.windows[i].bundle_id);
		free(c.windows[i].app_name);
		free(c.windows[i].title);
	}
	free(c.windows);
	free(c.displays);
	free(c.error);
}

// rc_filter builds the content filter recording uses: display-scoped with one window
// included, rather than desktopIndependentWindow, which trips a CGS_REQUIRE_INIT assertion
// in a plain CLI process with no window-server connection.
SCContentFilter *rc_filter(SCShareableContent *content, uint32_t display_id, uint32_t window_id, NSString **error) {
	SCDisplay *display = nil;
	for (SCDisplay *d in content.displays) {
		if (d.displayID == display_id) display = d;
	}
	if (display == nil) {
		*error = @"display not found";
		return nil;
	}
	if (window_id == 0) {
		return [[SCContentFilter alloc] initWithDisplay:display excludingWindows:@[]];
	}
	for (SCWindow *w in content.windows) {
		if (w.windowID == window_id) {
			return [[SCContentFilter alloc] initWithDisplay:display includingWindows:@[w]];
		}
	}
	*error = @"window not found — it may have closed";
	return nil;
}

double rc_point_pixel_scale(uint32_t display_id, uint32_t window_id, char **error) {
	@autoreleasepool {
		NSError *err = nil;
		SCShareableContent *content = fetch(&err);
		if (content == nil) {
			*error = rc_strdup(err.localizedDescription ?: @"no shareable content");
			return 0;
		}
		NSString *msg = nil;
		SCContentFilter *filter = rc_filter(content, display_id, window_id, &msg);
		if (filter == nil) {
			*error = rc_strdup(msg);
			return 0;
		}
		return filter.pointPixelScale;
	}
}
