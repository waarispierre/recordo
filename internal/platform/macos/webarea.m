// Finds a browser's web content area via the Accessibility API.
//
// Cropping a fixed number of points off the top of a browser window is a guess: it breaks
// when the bookmarks bar is toggled, differs per browser, and leaves a sliver of chrome or
// eats page content. The accessibility tree reports the web view's exact bounds instead.
#import <ApplicationServices/ApplicationServices.h>
#include <math.h>
#include <unistd.h>
#include "bridge.h"

bool rc_ax_trusted(void) { return AXIsProcessTrusted(); }

static CFTypeRef copy_attr(AXUIElementRef el, CFStringRef attr) {
	CFTypeRef value = NULL;
	if (AXUIElementCopyAttributeValue(el, attr, &value) != kAXErrorSuccess) return NULL;
	return value;
}

// copy_array returns the attribute only if it really is a CFArray. The Accessibility API
// returns whatever the target app chose to expose, and treating a non-array as one aborts
// inside CoreFoundation — reachable from any third-party app this tool is pointed at.
static CFArrayRef copy_array(AXUIElementRef el, CFStringRef attr) {
	CFTypeRef v = copy_attr(el, attr);
	if (v == NULL) return NULL;
	if (CFGetTypeID(v) != CFArrayGetTypeID()) {
		CFRelease(v);
		return NULL;
	}
	return (CFArrayRef)v;
}

static bool is_role(AXUIElementRef el, CFStringRef role) {
	CFTypeRef v = copy_attr(el, kAXRoleAttribute);
	if (v == NULL) return false;
	bool match = CFGetTypeID(v) == CFStringGetTypeID() && CFStringCompare((CFStringRef)v, role, 0) == kCFCompareEqualTo;
	CFRelease(v);
	return match;
}

static bool frame_of(AXUIElementRef el, rc_rect *out) {
	CFTypeRef pos = copy_attr(el, kAXPositionAttribute);
	CFTypeRef size = copy_attr(el, kAXSizeAttribute);
	CGPoint p = {0};
	CGSize s = {0};
	bool ok = pos && size
		&& CFGetTypeID(pos) == AXValueGetTypeID() && CFGetTypeID(size) == AXValueGetTypeID()
		&& AXValueGetValue((AXValueRef)pos, kAXValueCGPointType, &p)
		&& AXValueGetValue((AXValueRef)size, kAXValueCGSizeType, &s)
		&& s.width > 0 && s.height > 0;
	if (pos) CFRelease(pos);
	if (size) CFRelease(size);
	if (ok) *out = (rc_rect){p.x, p.y, s.width, s.height};
	return ok;
}

// Depth-first search for the largest AXWebArea, which is the rendered page.
static void find_web_area(AXUIElementRef el, int depth, rc_rect *best, bool *found) {
	// Browser hierarchies are shallow; a bound stops a pathological tree from hanging.
	if (depth > 12) return;
	if (is_role(el, CFSTR("AXWebArea"))) {
		rc_rect r;
		if (frame_of(el, &r) && (!*found || r.w * r.h > best->w * best->h)) {
			*best = r;
			*found = true;
		}
		// A web area can contain nested frames; the outermost is the one we want.
		return;
	}
	CFArrayRef children = copy_array(el, kAXChildrenAttribute);
	if (children == NULL) return;
	for (CFIndex i = 0; i < CFArrayGetCount(children); i++) {
		find_web_area((AXUIElementRef)CFArrayGetValueAtIndex(children, i), depth + 1, best, found);
	}
	CFRelease(children);
}

bool rc_ax_web_area(int32_t pid, rc_rect window, rc_rect *out) {
	if (!AXIsProcessTrusted()) return false;
	AXUIElementRef app = AXUIElementCreateApplication(pid);
	if (app == NULL) return false;
	// Chromium-based browsers expose only a stub accessibility tree until an assistive
	// client asks for the full one. AXManualAccessibility is the documented opt-in.
	AXUIElementSetAttributeValue(app, CFSTR("AXManualAccessibility"), kCFBooleanTrue);

	// Chromium builds its tree asynchronously once asked, so the first read usually finds
	// nothing. Safari answers on the first attempt.
	rc_rect best = {0};
	bool found = false;
	for (int attempt = 0; attempt < 8 && !found; attempt++) {
		if (attempt > 0) usleep(120 * 1000);
		CFArrayRef windows = copy_array(app, kAXWindowsAttribute);
		if (windows == NULL) continue;
		for (CFIndex i = 0; i < CFArrayGetCount(windows); i++) {
			AXUIElementRef w = (AXUIElementRef)CFArrayGetValueAtIndex(windows, i);
			rc_rect f;
			// Match on geometry — AX exposes no window id to join on.
			if (!frame_of(w, &f)) continue;
			if (fabs(f.x - window.x) > 2 || fabs(f.y - window.y) > 2 ||
			    fabs(f.w - window.w) > 2 || fabs(f.h - window.h) > 2) continue;
			find_web_area(w, 0, &best, &found);
			break;
		}
		CFRelease(windows);
	}
	CFRelease(app);
	if (found) *out = best;
	return found;
}
