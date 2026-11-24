// C interface to the Objective-C half of the capture package. Everything crossing this
// boundary is plain C data: strings are malloc'd UTF-8 that Go frees, arrays are
// malloc'd and freed by Go, and no Objective-C object outlives a call.
#ifndef RECORDO_BRIDGE_H
#define RECORDO_BRIDGE_H

#include <stdbool.h>
#include <stdint.h>

typedef struct {
	double x, y, w, h;
} rc_rect;

typedef struct {
	uint32_t id;
	int64_t layer;
	bool on_screen;
	rc_rect frame;
	int32_t pid;
	char *bundle_id; // may be NULL
	char *app_name;  // may be NULL
	char *title;     // may be NULL
} rc_window;

typedef struct {
	uint32_t id;
	uint32_t width, height; // points
	rc_rect frame;          // global points, same space as window frames
} rc_display;

typedef struct {
	rc_window *windows;
	int n_windows;
	rc_display *displays;
	int n_displays;
	char *error; // non-NULL on failure
} rc_content;

// Fetches what ScreenCaptureKit can capture. Blocks until it answers.
rc_content rc_shareable_content(void);
void rc_content_free(rc_content c);

// Point-to-pixel scale of the display, or of the window on it when window_id != 0.
// Returns 0 on failure and sets *error.
double rc_point_pixel_scale(uint32_t display_id, uint32_t window_id, char **error);

// AVFoundation devices. kind: 0 = microphone, 1 = camera.
typedef struct {
	char *id;
	char *name;
} rc_device;
int rc_devices(int kind, rc_device **out);
void rc_devices_free(rc_device *d, int n);
// 0 granted, 1 not asked, 2 denied, 3 unknown. Never prompts.
int rc_access(int kind);

// Accessibility.
bool rc_ax_trusted(void);
// Largest AXWebArea inside the window of pid whose frame matches window. Returns false
// when there is none.
bool rc_ax_web_area(int32_t pid, rc_rect window, rc_rect *out);

// Cursor and click telemetry. Times are raw mach absolute ticks; Go converts them.
typedef struct {
	uint64_t t_mach;
	int kind; // 0 move, 1 down, 2 up, 3 scroll
	double x, y;
} rc_event;
typedef struct {
	rc_event *events;
	int n_events;
	bool tap_installed;
	int tap_reenabled; // times the system disabled the tap and it was re-armed
} rc_telemetry;
void rc_telemetry_start(uint32_t poll_hz);
rc_telemetry rc_telemetry_stop(void);
void rc_telemetry_free(rc_telemetry t);

// Screen recording. One recording at a time.
typedef struct {
	uint64_t pts_ns;
	uint64_t display_mach;
	bool has_pts, has_display;
} rc_frame;
typedef struct {
	uint32_t display_id, window_id; // window_id 0 records the whole display
	bool crop;                      // apply source_rect
	rc_rect source_rect;
	uint32_t width, height;         // output pixels
	bool microphone;
	const char *microphone_id;      // NULL for the system default
	const char *path;
} rc_record_opts;
// Starts recording. Returns NULL on success, or an error string Go frees.
char *rc_record_start(rc_record_opts opts);
// Stops recording, waits for the file to be finalised, and returns the frame log.
char *rc_record_stop(rc_frame **frames, int *n_frames);

// Webcam. device_id NULL picks the system default camera; path NULL opens it for the
// preview only, writing nothing. Returns an error string Go frees, or NULL and sets *out
// and *name (Go frees name).
typedef struct rc_webcam rc_webcam;
char *rc_webcam_start(const char *device_id, const char *path, bool preview, rc_webcam **out, char **name);
// Stops and frees w. For a recording, waits for camera.mp4 to be finalised and returns
// the frame log (pts in host-clock nanoseconds).
char *rc_webcam_stop(rc_webcam *w, uint64_t **pts_ns, int *n);
// Copies the newest preview frame (RGB, 3 bytes per pixel) into dst if it holds cap
// bytes. Returns the frame's sequence number, 0 when there is none yet.
uint64_t rc_webcam_preview(rc_webcam *w, uint8_t *dst, int cap, uint32_t *width, uint32_t *height);

// Runs the main run loop forever. Must be called on the process's main thread.
void rc_main_loop(void);

// Default microphone's name, or NULL.
char *rc_default_microphone(void);

#endif
