package macos

/*
#include <stdlib.h>
#include "bridge.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"unsafe"

	"github.com/waarispierre/recordo/internal/domain/frames"
)

// PreviewFrame is a small downsampled camera frame for a live thumbnail.
type PreviewFrame struct {
	// RGB is row-major, top to bottom, 3 bytes per pixel.
	RGB  []byte
	W, H uint32
	// Seq increases with every new frame, so a caller can skip redrawing an old one.
	Seq uint64
}

// previewBytes fits the largest preview the Objective-C side produces (160x160 RGB).
const previewBytes = 160 * 160 * 3

// Webcam is an open camera: recording to camera.mp4, or opened only for its preview.
// Stop must be called; dropping it leaves the camera on and camera.mp4 unfinished.
type Webcam struct {
	mu     sync.Mutex
	handle *C.rc_webcam // nil once stopped
	Name   string
	buf    []byte
}

// findCamera resolves a camera by name, the same case-insensitive substring match --app
// and the microphone setting use. An empty want means the system default, returned as "".
func findCamera(want string) (string, error) {
	want = strings.TrimSpace(want)
	if want == "" {
		return "", nil
	}
	cams := Cameras()
	var known []string
	for _, d := range cams {
		if strings.Contains(strings.ToLower(d.Name), strings.ToLower(want)) {
			return d.ID, nil
		}
		known = append(known, d.Name)
	}
	if len(known) == 0 {
		known = []string{"none"}
	}
	return "", fmt.Errorf("no camera matching %q — available: %s", want, strings.Join(known, ", "))
}

// StartWebcam opens the camera named device ("" for the system default). With a path it
// records there; with path "" it only feeds the preview. preview asks for live thumbnails.
//
// A failure here is meant to be recoverable: a camera that is missing, in use, or denied
// should degrade a recording to screen-only, not fail it.
func StartWebcam(device, path string, preview bool) (*Webcam, error) {
	id, err := findCamera(device)
	if err != nil {
		return nil, err
	}
	var cid, cpath *C.char
	if id != "" {
		cid = C.CString(id)
		defer C.free(unsafe.Pointer(cid))
	}
	if path != "" {
		cpath = C.CString(path)
		defer C.free(unsafe.Pointer(cpath))
	}
	var handle *C.rc_webcam
	var name *C.char
	if e := C.rc_webcam_start(cid, cpath, C.bool(preview), &handle, &name); e != nil {
		defer C.free(unsafe.Pointer(e))
		return nil, errors.New(C.GoString(e))
	}
	defer C.free(unsafe.Pointer(name))
	return &Webcam{handle: handle, Name: C.GoString(name), buf: make([]byte, previewBytes)}, nil
}

// Preview returns the newest thumbnail, or ok=false when there is none yet or the camera
// has stopped. Safe to call from any goroutine.
func (w *Webcam) Preview() (PreviewFrame, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.handle == nil {
		return PreviewFrame{}, false
	}
	var width, height C.uint32_t
	seq := C.rc_webcam_preview(w.handle, (*C.uint8_t)(unsafe.Pointer(&w.buf[0])), C.int(len(w.buf)), &width, &height)
	if seq == 0 {
		return PreviewFrame{}, false
	}
	n := int(width) * int(height) * 3
	return PreviewFrame{RGB: append([]byte(nil), w.buf[:n]...), W: uint32(width), H: uint32(height), Seq: uint64(seq)}, true
}

// Stop releases the camera. For a recording it waits for camera.mp4 to be finalised and
// returns the camera's frame log. Calling it twice is harmless.
func (w *Webcam) Stop() ([]frames.Record, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.handle == nil {
		return nil, nil
	}
	var pts *C.uint64_t
	var n C.int
	e := C.rc_webcam_stop(w.handle, &pts, &n)
	w.handle = nil
	defer C.free(unsafe.Pointer(pts))
	// Same schema as the screen's frames.json, so the render treats both streams alike;
	// the camera's presentation time is already host-clock nanoseconds.
	out := make([]frames.Record, 0, int(n))
	for _, v := range unsafe.Slice(pts, int(n)) {
		t := uint64(v)
		out = append(out, frames.Record{PtsNs: &t, DisplayTimeNs: &t})
	}
	if e != nil {
		defer C.free(unsafe.Pointer(e))
		return out, errors.New(C.GoString(e))
	}
	return out, nil
}
