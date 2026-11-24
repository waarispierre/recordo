package capture

/*
#include "bridge.h"
*/
import "C"

import "unsafe"

// Access is whether a permission has been granted, refused, or not yet asked for.
type Access int

const (
	// Granted: capture will work.
	Granted Access = iota
	// NotAsked: macOS will prompt the first time.
	NotAsked
	// Denied: refused, or blocked by policy. Capture will produce nothing.
	Denied
	// Unknown: AVFoundation did not answer. Saying "denied" when the question was never
	// answered would be a lie.
	Unknown
)

// Explain is what the access state means for a recording, for doctor to print.
func (a Access) Explain() string {
	switch a {
	case Granted:
		return "granted"
	case NotAsked:
		return "not asked yet — macOS will prompt on the first recording"
	case Denied:
		return "denied — grant it in System Settings › Privacy & Security"
	default:
		return "could not be determined"
	}
}

const (
	kindMicrophone = 0
	kindCamera     = 1
)

// MicrophoneAccess reports the microphone grant without prompting.
func MicrophoneAccess() Access { return Access(C.rc_access(kindMicrophone)) }

// CameraAccess reports the camera grant without prompting.
func CameraAccess() Access { return Access(C.rc_access(kindCamera)) }

// Device is one camera or microphone as AVFoundation reports it.
type Device struct {
	// ID is stable, and what would be handed to AVFoundation to open the device.
	ID string
	// Name is what `recordo devices` prints and what config matches against,
	// case-insensitively and by substring.
	Name string
}

func devices(kind int) []Device {
	var list *C.rc_device
	n := C.rc_devices(C.int(kind), &list)
	defer C.rc_devices_free(list, n)
	var out []Device
	for _, d := range unsafe.Slice(list, int(n)) {
		out = append(out, Device{ID: C.GoString(d.id), Name: C.GoString(d.name)})
	}
	return out
}

// Microphones is every microphone AVFoundation can see.
func Microphones() []Device { return devices(kindMicrophone) }

// Cameras is every camera AVFoundation can see: built-in, Continuity Camera, Desk View,
// and anything plugged in externally.
func Cameras() []Device { return devices(kindCamera) }
