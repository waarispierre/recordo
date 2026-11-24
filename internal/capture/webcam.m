// Webcam capture via AVFoundation, run alongside the ScreenCaptureKit stream.
//
// Mirrors the screen side deliberately: AVCaptureMovieFileOutput writes camera.mp4
// directly, so the recorded pixels never pass through this process. A second output —
// AVCaptureVideoDataOutput — exists purely to log each frame's presentation timestamp. It
// is on the host clock, the same timebase mach_absolute_time reads, which is what lets the
// render line the webcam up against the screen instead of guessing an offset.
//
// A caller that asks for a preview gets a third output, which does read pixels — heavily
// downsampled and throttled — on its own queue, so the frame timing the render depends on
// is never delayed by a downsample.
#import <AVFoundation/AVFoundation.h>
#import <CoreVideo/CoreVideo.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdlib.h>
#include <string.h>
#include "bridge.h"
#include "trace.h"

// Longest edge of a preview frame. Sized for a terminal drawing six samples per cell.
static const uint32_t kPreviewSize = 160;
// Every Nth sample becomes a preview frame: ~5 fps from a 30 fps camera, plenty for a
// framing check.
static const uint32_t kPreviewEveryN = 6;

// Every AVCaptureSession call runs on the process's main thread.
//
// AVFoundation delivers part of a capture session's work through the main dispatch queue:
// the movie file's "finished" callback, and something stopRunning waits on. A Go program
// never services that queue on its own, so the callback never arrives and stopRunning
// blocks for good. capture.Main makes the main thread run the main run loop, and
// on_camera_thread hands blocks to it.
static atomic_bool main_loop_running;

void rc_main_loop(void) {
	CFRunLoopSourceContext ctx = {0};
	CFRunLoopSourceRef keepalive = CFRunLoopSourceCreate(NULL, 0, &ctx);
	CFRunLoopAddSource(CFRunLoopGetMain(), keepalive, kCFRunLoopCommonModes);
	atomic_store(&main_loop_running, true);
	for (;;) {
		@autoreleasepool {
			CFRunLoopRunInMode(kCFRunLoopDefaultMode, 1e10, false);
		}
	}
}

// wait_pumping waits for sema while keeping this thread's run loop turning, so a callback
// AVFoundation delivers through it can still arrive. Returns false on timeout.
static bool wait_pumping(dispatch_semaphore_t sema, double seconds) {
	CFAbsoluteTime deadline = CFAbsoluteTimeGetCurrent() + seconds;
	while (dispatch_semaphore_wait(sema, DISPATCH_TIME_NOW) != 0) {
		if (CFAbsoluteTimeGetCurrent() > deadline) return false;
		CFRunLoopRunInMode(kCFRunLoopDefaultMode, 0.02, false);
	}
	return true;
}

static bool on_camera_thread(void (^block)(void)) {
	if (!atomic_load(&main_loop_running)) return false;
	if (pthread_main_np()) {
		block();
		return true;
	}
	dispatch_semaphore_t done = dispatch_semaphore_create(0);
	CFRunLoopRef loop = CFRunLoopGetMain();
	CFRunLoopPerformBlock(loop, kCFRunLoopCommonModes, ^{
		RC_TRACE("main thread: running block");
		@autoreleasepool {
			block();
		}
		dispatch_semaphore_signal(done);
	});
	CFRunLoopWakeUp(loop);
	RC_TRACE("on_camera_thread: waiting for main thread");
	dispatch_semaphore_wait(done, DISPATCH_TIME_FOREVER);
	return true;
}

@interface RCTimestamps : NSObject <AVCaptureVideoDataOutputSampleBufferDelegate>
@property(nonatomic) NSMutableData *pts;
@end

@implementation RCTimestamps
- (instancetype)init {
	if ((self = [super init])) _pts = [NSMutableData data];
	return self;
}
- (void)captureOutput:(AVCaptureOutput *)o didOutputSampleBuffer:(CMSampleBufferRef)sample fromConnection:(AVCaptureConnection *)c {
	double seconds = CMTimeGetSeconds(CMSampleBufferGetPresentationTimeStamp(sample));
	if (!isfinite(seconds)) return;
	uint64_t ns = (uint64_t)(seconds * 1e9);
	@synchronized(self) {
		[self.pts appendBytes:&ns length:sizeof ns];
	}
}
@end

@interface RCPreview : NSObject <AVCaptureVideoDataOutputSampleBufferDelegate> {
  @public
	pthread_mutex_t mu;
	uint8_t *rgb;
	uint32_t w, h;
	uint64_t seq;
	uint32_t counter;
}
@end

@implementation RCPreview
- (instancetype)init {
	if ((self = [super init])) {
		pthread_mutex_init(&mu, NULL);
		rgb = malloc(kPreviewSize * kPreviewSize * 3);
	}
	return self;
}
- (void)dealloc {
	free(rgb);
	pthread_mutex_destroy(&mu);
}
- (void)captureOutput:(AVCaptureOutput *)o didOutputSampleBuffer:(CMSampleBufferRef)sample fromConnection:(AVCaptureConnection *)c {
	if (counter++ % kPreviewEveryN != 0) return;
	CVImageBufferRef image = CMSampleBufferGetImageBuffer(sample);
	if (image == NULL || CVPixelBufferLockBaseAddress(image, kCVPixelBufferLock_ReadOnly) != kCVReturnSuccess) return;
	size_t sw = CVPixelBufferGetWidth(image), sh = CVPixelBufferGetHeight(image);
	size_t stride = CVPixelBufferGetBytesPerRow(image);
	const uint8_t *base = CVPixelBufferGetBaseAddress(image);
	if (base != NULL && sw > 0 && sh > 0) {
		uint32_t ow, oh;
		if (sw >= sh) {
			ow = kPreviewSize;
			oh = (uint32_t)(sh * ow / sw) ?: 1;
		} else {
			oh = kPreviewSize;
			ow = (uint32_t)(sw * oh / sh) ?: 1;
		}
		pthread_mutex_lock(&mu);
		// Nearest-neighbour; BGRA in memory (forced by the output settings) -> RGB out.
		for (uint32_t y = 0; y < oh; y++) {
			const uint8_t *row = base + (y * sh / oh) * stride;
			for (uint32_t x = 0; x < ow; x++) {
				const uint8_t *p = row + (x * sw / ow) * 4;
				uint8_t *q = rgb + (y * ow + x) * 3;
				q[0] = p[2];
				q[1] = p[1];
				q[2] = p[0];
			}
		}
		w = ow;
		h = oh;
		seq++;
		pthread_mutex_unlock(&mu);
	}
	CVPixelBufferUnlockBaseAddress(image, kCVPixelBufferLock_ReadOnly);
}
@end

@interface RCMovieDelegate : NSObject <AVCaptureFileOutputRecordingDelegate>
@property(nonatomic) dispatch_semaphore_t finished;
@property(nonatomic) NSError *error;
@end

@implementation RCMovieDelegate
- (instancetype)init {
	if ((self = [super init])) _finished = dispatch_semaphore_create(0);
	return self;
}
- (void)captureOutput:(AVCaptureFileOutput *)o didFinishRecordingToOutputFileAtURL:(NSURL *)url
      fromConnections:(NSArray *)connections error:(NSError *)error {
	self.error = error;
	dispatch_semaphore_signal(self.finished);
}
@end

@interface RCWebcam : NSObject
@property(nonatomic) AVCaptureSession *session;
@property(nonatomic) AVCaptureMovieFileOutput *movie;
@property(nonatomic) RCMovieDelegate *movieDelegate;
@property(nonatomic) RCTimestamps *timestamps;
@property(nonatomic) RCPreview *preview;
@property(nonatomic) dispatch_queue_t tsQueue, previewQueue;
@end
@implementation RCWebcam
@end

static NSDictionary *bgra(void) {
	return @{(id)kCVPixelBufferPixelFormatTypeKey: @(kCVPixelFormatType_32BGRA)};
}

static char *webcam_start(const char *device_id, const char *path, bool want_preview, rc_webcam **out, char **name) {
	{
		AVCaptureDevice *dev = device_id
			? [AVCaptureDevice deviceWithUniqueID:@(device_id)]
			: [AVCaptureDevice defaultDeviceWithMediaType:AVMediaTypeVideo];
		if (dev == nil) return strdup("no camera available");
		NSError *e = nil;
		AVCaptureDeviceInput *input = [AVCaptureDeviceInput deviceInputWithDevice:dev error:&e];
		if (input == nil) return strdup([NSString stringWithFormat:@"could not open camera: %@", e.localizedDescription].UTF8String);

		RCWebcam *cam = [[RCWebcam alloc] init];
		cam.session = [[AVCaptureSession alloc] init];
		// Recording: High matches the camera's own capabilities, and the render scales it.
		// Preview only: nothing is kept, so ask for the cheapest stream the camera gives.
		cam.session.sessionPreset = path ? AVCaptureSessionPresetHigh : AVCaptureSessionPresetLow;
		if (![cam.session canAddInput:input]) return strdup("camera input rejected by capture session");
		[cam.session addInput:input];

		if (path) {
			cam.movie = [[AVCaptureMovieFileOutput alloc] init];
			if (![cam.session canAddOutput:cam.movie]) return strdup("movie file output rejected by capture session");
			[cam.session addOutput:cam.movie];

			// A missing timestamp stream degrades sync, not the recording.
			AVCaptureVideoDataOutput *ts = [[AVCaptureVideoDataOutput alloc] init];
			cam.timestamps = [[RCTimestamps alloc] init];
			cam.tsQueue = dispatch_queue_create("recordo.webcam.timestamps", DISPATCH_QUEUE_SERIAL);
			[ts setSampleBufferDelegate:cam.timestamps queue:cam.tsQueue];
			if ([cam.session canAddOutput:ts]) [cam.session addOutput:ts];
		}
		if (want_preview) {
			AVCaptureVideoDataOutput *pv = [[AVCaptureVideoDataOutput alloc] init];
			pv.videoSettings = bgra();
			cam.preview = [[RCPreview alloc] init];
			cam.previewQueue = dispatch_queue_create("recordo.webcam.preview", DISPATCH_QUEUE_SERIAL);
			[pv setSampleBufferDelegate:cam.preview queue:cam.previewQueue];
			if ([cam.session canAddOutput:pv]) {
				[cam.session addOutput:pv];
			} else if (!path) {
				return strdup("preview output rejected by capture session");
			} else {
				cam.preview = nil;
			}
		}

		RC_TRACE("webcam_start: startRunning");
		[cam.session startRunning];
		RC_TRACE("webcam_start: running");
		if (path) {
			NSURL *url = [NSURL fileURLWithPath:@(path)];
			[[NSFileManager defaultManager] removeItemAtURL:url error:nil];
			cam.movieDelegate = [[RCMovieDelegate alloc] init];
			[cam.movie startRecordingToOutputFileURL:url recordingDelegate:cam.movieDelegate];
		}
		*name = strdup(dev.localizedName.UTF8String);
		*out = (rc_webcam *)CFBridgingRetain(cam);
		return NULL;
	}
}

static char *webcam_stop(rc_webcam *handle, uint64_t **pts_ns, int *n) {
	{
		RCWebcam *cam = CFBridgingRelease(handle);
		*pts_ns = NULL;
		*n = 0;
		char *err = NULL;
		RC_TRACE("webcam_stop: begin");
		if (cam.movie) {
			RC_TRACE("webcam_stop: stopRecording");
			[cam.movie stopRecording];
			RC_TRACE("webcam_stop: waiting for file");
			// The file is only complete once the delegate says so; the trailing samples are
			// written asynchronously after stopRecording returns.
			if (!wait_pumping(cam.movieDelegate.finished, 5)) {
				err = strdup("timed out waiting for camera.mp4 to finish writing");
			} else if (cam.movieDelegate.error != nil &&
			           ![cam.movieDelegate.error.userInfo[AVErrorRecordingSuccessfullyFinishedKey] boolValue]) {
				err = strdup([NSString stringWithFormat:@"camera recording: %@", cam.movieDelegate.error.localizedDescription].UTF8String);
			}
		}
		RC_TRACE("webcam_stop: stopRunning");
		[cam.session stopRunning];
		RC_TRACE("webcam_stop: stopped");
		if (cam.timestamps) {
			RC_TRACE("webcam_stop: draining timestamps");
			dispatch_sync(cam.tsQueue, ^{});
			NSData *d;
			@synchronized(cam.timestamps) {
				d = [cam.timestamps.pts copy];
			}
			*n = (int)(d.length / sizeof(uint64_t));
			*pts_ns = malloc(d.length ?: 1);
			memcpy(*pts_ns, d.bytes, d.length);
		}
		if (cam.previewQueue) dispatch_sync(cam.previewQueue, ^{});
		RC_TRACE("webcam_stop: done");
		return err;
	}
}

static const char *kNoMainLoop = "the camera needs capture.Main running on the main thread";

char *rc_webcam_start(const char *device_id, const char *path, bool want_preview, rc_webcam **out, char **name) {
	__block char *err = NULL;
	if (!on_camera_thread(^{
		    err = webcam_start(device_id, path, want_preview, out, name);
	    }))
		return strdup(kNoMainLoop);
	return err;
}

char *rc_webcam_stop(rc_webcam *handle, uint64_t **pts_ns, int *n) {
	__block char *err = NULL;
	if (!on_camera_thread(^{
		    err = webcam_stop(handle, pts_ns, n);
	    }))
		return strdup(kNoMainLoop);
	return err;
}

uint64_t rc_webcam_preview(rc_webcam *handle, uint8_t *dst, int cap, uint32_t *width, uint32_t *height) {
	RCWebcam *cam = (__bridge RCWebcam *)handle;
	RCPreview *p = cam.preview;
	if (p == nil) return 0;
	pthread_mutex_lock(&p->mu);
	uint64_t seq = p->seq;
	size_t need = (size_t)p->w * p->h * 3;
	if (seq != 0 && need <= (size_t)cap) {
		memcpy(dst, p->rgb, need);
		*width = p->w;
		*height = p->h;
	} else {
		seq = 0;
	}
	pthread_mutex_unlock(&p->mu);
	return seq;
}
