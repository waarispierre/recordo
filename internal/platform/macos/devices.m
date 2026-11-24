// Cameras and microphones: what macOS will let us use.
//
// Permission is read with authorizationStatusForMediaType:, which reports the current
// grant without prompting. That matters for `doctor`: a diagnostic command that pops a
// system dialog is a diagnostic command nobody runs twice.
#import <AVFoundation/AVFoundation.h>
#include <stdlib.h>
#include <string.h>
#include "bridge.h"

static AVMediaType media_for(int kind) {
	return kind == 0 ? AVMediaTypeAudio : AVMediaTypeVideo;
}

int rc_access(int kind) {
	switch ([AVCaptureDevice authorizationStatusForMediaType:media_for(kind)]) {
	case AVAuthorizationStatusAuthorized:
		return 0;
	case AVAuthorizationStatusNotDetermined:
		return 1;
	case AVAuthorizationStatusDenied:
	case AVAuthorizationStatusRestricted:
		return 2;
	default:
		return 3;
	}
}

int rc_devices(int kind, rc_device **out) {
	@autoreleasepool {
		NSArray<AVCaptureDeviceType> *types;
		if (kind == 0) {
			types = @[AVCaptureDeviceTypeMicrophone];
		} else {
			types = @[
				AVCaptureDeviceTypeBuiltInWideAngleCamera,
				AVCaptureDeviceTypeContinuityCamera,
				AVCaptureDeviceTypeDeskViewCamera,
				AVCaptureDeviceTypeExternal,
			];
		}
		// A discovery session only enumerates connected hardware; it does not open,
		// activate, or request permission for anything.
		AVCaptureDeviceDiscoverySession *session =
			[AVCaptureDeviceDiscoverySession discoverySessionWithDeviceTypes:types
			                                                       mediaType:media_for(kind)
			                                                        position:AVCaptureDevicePositionUnspecified];
		NSArray<AVCaptureDevice *> *devices = session.devices;
		*out = calloc(devices.count ?: 1, sizeof(rc_device));
		for (NSUInteger i = 0; i < devices.count; i++) {
			(*out)[i].id = strdup(devices[i].uniqueID.UTF8String);
			(*out)[i].name = strdup(devices[i].localizedName.UTF8String);
		}
		return (int)devices.count;
	}
}

void rc_devices_free(rc_device *d, int n) {
	for (int i = 0; i < n; i++) {
		free(d[i].id);
		free(d[i].name);
	}
	free(d);
}
