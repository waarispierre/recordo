package capture

/*
#include "bridge.h"
*/
import "C"

import (
	"os"
	"runtime"
)

// Lock the main goroutine to the main thread before main() runs, so Main can hand the
// main thread to macOS.
func init() { runtime.LockOSThread() }

// Main runs app on a goroutine while the main thread runs macOS's main run loop, then
// exits with app's return code. It must be called from main() and never returns.
//
// AVFoundation delivers part of a camera session's work through the main dispatch queue,
// which a Go program otherwise never services: without this, stopping a webcam recording
// blocks forever. Anything that does not touch the webcam works without it.
func Main(app func() int) {
	go func() { os.Exit(app()) }()
	C.rc_main_loop()
}
