package record

import (
	"sync"

	"github.com/waarispierre/recordo/internal/platform/macos"
)

// PreviewCamera is an open camera the record tab can show thumbnails from: the
// pre-flight framing check, or the recording's own camera.
type PreviewCamera interface {
	// Preview returns the newest thumbnail, ok=false when there is none yet or the camera
	// has stopped. Safe from any goroutine.
	Preview() (macos.PreviewFrame, bool)
}

// FramingCamera is a preview-only camera the tab opened itself and has to release.
type FramingCamera interface {
	PreviewCamera
	Name() string
	// Stop releases the camera. Calling it twice is harmless.
	Stop()
}

// webcam adapts macos.Webcam to FramingCamera.
type webcam struct{ *macos.Webcam }

func (w webcam) Name() string { return w.Webcam.Name }
func (w webcam) Stop()        { _, _ = w.Webcam.Stop() }

// cameraSlot serialises everything that opens or closes a camera. A camera tolerates one
// session, so the framing check has to be fully released before a recording opens its
// own; holding one lock across both makes that ordering structural rather than timed.
type cameraSlot struct {
	mu sync.Mutex
	// open is the framing camera, guarded by mu.
	open FramingCamera
	// want is bumped by the UI whenever the framing camera it wants changes, so an open
	// that finishes after it stopped being wanted releases the camera straight away.
	want uint64
	// wantMu guards want; never held while a camera call is in flight, so the UI can
	// always take it without waiting on the camera.
	wantMu sync.Mutex
}

func (c *cameraSlot) bump() uint64 {
	c.wantMu.Lock()
	defer c.wantMu.Unlock()
	c.want++
	return c.want
}

func (c *cameraSlot) wanted(id uint64) bool {
	c.wantMu.Lock()
	defer c.wantMu.Unlock()
	return c.want == id
}

// release stops the framing camera if one is open. Call with mu held.
func (c *cameraSlot) release() {
	if c.open != nil {
		c.open.Stop()
		c.open = nil
	}
}
