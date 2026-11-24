// Screen recording through ScreenCaptureKit.
//
// SCRecordingOutput writes capture.mp4 directly, so video never passes through this
// process while recording — that is why capture is cheap. A second, screen-type stream
// output exists only to log each frame's display time, the mach timestamp that lines
// frames up with cursor telemetry; it never touches the pixels.
#import <AVFoundation/AVFoundation.h>
#import <CoreMedia/CoreMedia.h>
#import <CoreVideo/CoreVideo.h>
#import <ScreenCaptureKit/ScreenCaptureKit.h>
#include <stdlib.h>
#include <string.h>
#include "bridge.h"
#include "trace.h"

SCContentFilter *rc_filter(SCShareableContent *content, uint32_t display_id, uint32_t window_id, NSString **error);

static const int64_t kTimeout = 10 * NSEC_PER_SEC;

static char *err_string(NSString *prefix, NSError *e) {
	NSString *msg = e ? [NSString stringWithFormat:@"%@: %@", prefix, e.localizedDescription] : prefix;
	return strdup(msg.UTF8String);
}

// Frame log: one entry per screen sample, appended on the sample queue.
@interface RCFrameLog : NSObject <SCStreamOutput>
@property(nonatomic) NSMutableData *frames;
@end

@implementation RCFrameLog
- (instancetype)init {
	if ((self = [super init])) _frames = [NSMutableData data];
	return self;
}

- (void)stream:(SCStream *)stream didOutputSampleBuffer:(CMSampleBufferRef)sample ofType:(SCStreamOutputType)type {
	if (type != SCStreamOutputTypeScreen) return;
	rc_frame f = {0};
	CMTime pts = CMSampleBufferGetPresentationTimeStamp(sample);
	if (CMTIME_IS_NUMERIC(pts)) {
		f.has_pts = true;
		f.pts_ns = (uint64_t)(CMTimeGetSeconds(pts) * 1e9);
	}
	CFArrayRef attachments = CMSampleBufferGetSampleAttachmentsArray(sample, false);
	if (attachments != NULL && CFArrayGetCount(attachments) > 0) {
		NSDictionary *info = (__bridge NSDictionary *)CFArrayGetValueAtIndex(attachments, 0);
		NSNumber *display = info[SCStreamFrameInfoDisplayTime];
		if (display != nil) {
			f.has_display = true;
			f.display_mach = display.unsignedLongLongValue;
		}
	}
	@synchronized(self) {
		[self.frames appendBytes:&f length:sizeof f];
	}
}
@end

// Signals when SCRecordingOutput has started and when it has finished the file.
@interface RCRecordingDelegate : NSObject <SCRecordingOutputDelegate>
@property(nonatomic) dispatch_semaphore_t started, finished;
@property(nonatomic) NSError *error;
@end

@implementation RCRecordingDelegate
- (instancetype)init {
	if ((self = [super init])) {
		_started = dispatch_semaphore_create(0);
		_finished = dispatch_semaphore_create(0);
	}
	return self;
}
- (void)recordingOutputDidStartRecording:(SCRecordingOutput *)o {
	dispatch_semaphore_signal(self.started);
}
- (void)recordingOutput:(SCRecordingOutput *)o didFailWithError:(NSError *)error {
	self.error = error;
	dispatch_semaphore_signal(self.started);
	dispatch_semaphore_signal(self.finished);
}
- (void)recordingOutputDidFinishRecording:(SCRecordingOutput *)o {
	dispatch_semaphore_signal(self.finished);
}
@end

static SCStream *stream;
static RCFrameLog *frame_log;
static RCRecordingDelegate *delegate;
static SCRecordingOutput *recording;
static dispatch_queue_t sample_queue;

static NSError *run_sync(void (^call)(void (^)(NSError *))) {
	__block NSError *result = nil;
	dispatch_semaphore_t done = dispatch_semaphore_create(0);
	call(^(NSError *e) {
		result = e;
		dispatch_semaphore_signal(done);
	});
	if (dispatch_semaphore_wait(done, dispatch_time(DISPATCH_TIME_NOW, kTimeout)) != 0) {
		return [NSError errorWithDomain:@"recordo" code:1 userInfo:@{NSLocalizedDescriptionKey: @"timed out"}];
	}
	return result;
}

char *rc_record_start(rc_record_opts o) {
	@autoreleasepool {
		if (stream != nil) return strdup("a recording is already running");

		__block SCShareableContent *content = nil;
		NSError *err = run_sync(^(void (^done)(NSError *)) {
			[SCShareableContent getShareableContentExcludingDesktopWindows:YES onScreenWindowsOnly:NO
			                                             completionHandler:^(SCShareableContent *c, NSError *e) {
				content = c;
				done(e);
			}];
		});
		if (content == nil) return err_string(@"could not read shareable content — grant Screen Recording permission", err);

		NSString *msg = nil;
		SCContentFilter *filter = rc_filter(content, o.display_id, o.window_id, &msg);
		if (filter == nil) return strdup(msg.UTF8String);

		SCStreamConfiguration *cfg = [[SCStreamConfiguration alloc] init];
		cfg.width = o.width;
		cfg.height = o.height;
		cfg.minimumFrameInterval = CMTimeMake(1, 60);
		cfg.showsCursor = YES;
		// Pin full-resolution BGRA. On recent macOS Apple's default became 420v (4:2:0
		// YUV, video range), which halves colour resolution in both directions before the
		// recorder encodes anything — text and coloured edges come out visibly soft. The
		// Rust screencapturekit crate pins BGRA for the same reason.
		cfg.pixelFormat = kCVPixelFormatType_32BGRA;
		// The filter is display-scoped, so the output is display-sized; cropping to the
		// window's frame is what keeps the menu bar, dock, desktop and every other app out
		// of the video — a privacy property as much as a framing one.
		if (o.crop) {
			cfg.sourceRect = CGRectMake(o.source_rect.x, o.source_rect.y, o.source_rect.w, o.source_rect.h);
		}
		if (o.microphone) {
			// Microphone only. capturesAudio — system audio — stays off, so a notification
			// chime or whatever else is playing never lands in the recording.
			cfg.captureMicrophone = YES;
			if (o.microphone_id != NULL) cfg.microphoneCaptureDeviceID = @(o.microphone_id);
		}

		NSURL *url = [NSURL fileURLWithPath:@(o.path)];
		// SCRecordingOutput refuses to start if the destination already exists.
		[[NSFileManager defaultManager] removeItemAtURL:url error:nil];

		stream = [[SCStream alloc] initWithFilter:filter configuration:cfg delegate:nil];
		frame_log = [[RCFrameLog alloc] init];
		sample_queue = dispatch_queue_create("recordo.frames", DISPATCH_QUEUE_SERIAL);
		NSError *e = nil;
		if (![stream addStreamOutput:frame_log type:SCStreamOutputTypeScreen sampleHandlerQueue:sample_queue error:&e]) {
			stream = nil;
			return err_string(@"attach frame log", e);
		}

		SCRecordingOutputConfiguration *rc = [[SCRecordingOutputConfiguration alloc] init];
		rc.outputURL = url;
		rc.videoCodecType = AVVideoCodecTypeH264;
		rc.outputFileType = AVFileTypeMPEG4;
		delegate = [[RCRecordingDelegate alloc] init];
		recording = [[SCRecordingOutput alloc] initWithConfiguration:rc delegate:delegate];
		if (![stream addRecordingOutput:recording error:&e]) {
			stream = nil;
			return err_string(@"attach recording output", e);
		}

		err = run_sync(^(void (^done)(NSError *)) {
			[stream startCaptureWithCompletionHandler:done];
		});
		if (err != nil) {
			stream = nil;
			return err_string(@"start capture", err);
		}
		// Recording starts a moment after capture; wait so that "recording" on screen is
		// true, and so a failure surfaces here rather than as an empty file at the end.
		if (dispatch_semaphore_wait(delegate.started, dispatch_time(DISPATCH_TIME_NOW, kTimeout)) != 0 || delegate.error) {
			NSError *re = delegate.error;
			[stream stopCaptureWithCompletionHandler:^(NSError *x) {}];
			stream = nil;
			return err_string(@"start recording output", re);
		}
		return NULL;
	}
}

char *rc_record_stop(rc_frame **frames, int *n_frames) {
	@autoreleasepool {
		*frames = NULL;
		*n_frames = 0;
		if (stream == nil) return strdup("no recording is running");
		RC_TRACE("record_stop: stopCapture");
		NSError *err = run_sync(^(void (^done)(NSError *)) {
			[stream stopCaptureWithCompletionHandler:done];
		});
		RC_TRACE("record_stop: capture stopped (%s), waiting for file", err ? "error" : "ok");
		// Stopping capture ends the recording, but the file is only complete once the
		// output says so; reading it before then finds a truncated MP4.
		long waited = dispatch_semaphore_wait(delegate.finished, dispatch_time(DISPATCH_TIME_NOW, kTimeout));
		RC_TRACE("record_stop: file %s, draining frames", waited == 0 ? "finished" : "TIMED OUT");
		// Drain the sample queue so no frame is appended after the copy below.
		dispatch_sync(sample_queue, ^{});

		NSData *data;
		@synchronized(frame_log) {
			data = [frame_log.frames copy];
		}
		*n_frames = (int)(data.length / sizeof(rc_frame));
		*frames = malloc(data.length ?: 1);
		memcpy(*frames, data.bytes, data.length);

		RC_TRACE("record_stop: done");
		NSError *recErr = delegate.error;
		stream = nil;
		frame_log = nil;
		recording = nil;
		delegate = nil;
		sample_queue = nil;
		if (err != nil) return err_string(@"stop capture", err);
		if (recErr != nil) return err_string(@"recording failed", recErr);
		if (waited != 0) return strdup("timed out waiting for the recording to finish writing");
		return NULL;
	}
}

char *rc_default_microphone(void) {
	@autoreleasepool {
		AVCaptureDevice *d = [AVCaptureDevice defaultDeviceWithMediaType:AVMediaTypeAudio];
		return d ? strdup(d.localizedName.UTF8String) : NULL;
	}
}
