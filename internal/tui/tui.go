// Package tui is the full-screen terminal app, in the spirit of lazygit.
//
// Nothing leaves the alternate screen any more: a job draws its own progress here rather
// than suspending the app for line-oriented output.
//
// Everything slow runs off the Bubble Tea event loop. Rendering and compressing are
// ffmpeg and WebGPU work on a goroutine, reporting through counters the 100ms tick polls.
// Recording also runs on a goroutine: unlike the Rust version, which had to keep the
// capture on the main thread because tearing an AVCaptureSession down from a spawned
// thread hung forever, the Go capture package marshals camera work onto the main run loop
// itself (macos.Main), so the TUI can treat a recording like any other background job.
// Its Stop callback blocks on a channel the TUI closes, and services pause toggles sent
// over another, so the pause log is only ever touched from the recorder's own goroutine.
//
// `/usr/bin/open` is cheap and one-shot enough (it launches a separate GUI app and returns
// almost immediately) to start inline rather than routing it through a job.
package tui

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/waarispierre/recordo/internal/domain/config"
	"github.com/waarispierre/recordo/internal/domain/session"
	"github.com/waarispierre/recordo/internal/features/compress"
	"github.com/waarispierre/recordo/internal/features/record"
	"github.com/waarispierre/recordo/internal/features/render"
	"github.com/waarispierre/recordo/internal/platform/macos"
)

// Overrides are per-run settings that would otherwise come from CLI flags, so a bare
// `recordo --zoom 30` still honours --zoom when a recording is started from the Record
// tab.
type Overrides struct {
	Zoom   *float64
	Mic    *bool
	Webcam *bool
	// Seconds stops a recording after this much recorded time; 0 means no limit.
	Seconds  uint
	NoRender bool
	NoOpen   bool
}

// previewCamera is an open camera the app can show thumbnails from: the pre-flight
// framing check, or the recording's own camera.
type previewCamera interface {
	// Preview returns the newest thumbnail, ok=false when there is none yet or the camera
	// has stopped. Safe from any goroutine.
	Preview() (macos.PreviewFrame, bool)
}

// framingCamera is a preview-only camera the app opened itself and has to release.
type framingCamera interface {
	previewCamera
	Name() string
	// Stop releases the camera. Calling it twice is harmless.
	Stop()
}

// deps are the side effects the model performs, injected so tests can run the real
// state machine against fakes: the sandbox the tests run in has no screen recording,
// camera or GPU.
type deps struct {
	windows    func() []macos.Window
	record     func(*session.Session, record.Target, config.Config, record.RecordOptions) error
	health     func(*session.Session) (record.Health, error)
	render     func(src, dst string, opts render.Options) (render.Report, error)
	compress   func(src, dst string, maxBytes uint64) (compress.CompressReport, error)
	openCamera func(device string) (framingCamera, error)
	trash      func(dir string) error
	open       func(path string)
}

// webcam adapts macos.Webcam to framingCamera.
type webcam struct{ *macos.Webcam }

func (w webcam) Name() string { return w.Webcam.Name }
func (w webcam) Stop()        { _, _ = w.Webcam.Stop() }

func realDeps() deps {
	return deps{
		windows: func() []macos.Window {
			content, err := macos.ShareableContent()
			if err != nil {
				return nil
			}
			return record.Recordable(content)
		},
		record:   record.Record,
		health:   record.CheckHealth,
		render:   render.Run,
		compress: compress.CompressToSize,
		openCamera: func(device string) (framingCamera, error) {
			cam, err := macos.StartWebcam(device, "", true)
			if err != nil {
				return nil, err
			}
			return webcam{cam}, nil
		},
		trash: func(dir string) error { return (&session.Session{Dir: dir}).Trash() },
		open:  func(path string) { _ = exec.Command("/usr/bin/open", path).Start() },
	}
}

// Run shows the app until the user quits. It must run under macos.Main when the webcam
// may be used.
func Run(o Overrides) error {
	configPath, err := session.ConfigPath()
	if err != nil {
		return err
	}
	// Before the alternate screen, so the "wrote default config" note on a first run is
	// printed where it can be read.
	if _, err := config.LoadOrCreate(configPath); err != nil {
		return err
	}
	m, err := newModel(o, configPath, realDeps())
	if err != nil {
		return err
	}
	final, runErr := tea.NewProgram(m).Run()
	if fm, ok := final.(*Model); ok && fm != nil {
		m = fm
	}
	// The program can end with work still in flight (an interrupt from outside, say);
	// a recording must still be finalised rather than abandoned.
	m.shutdown()
	if runErr != nil {
		return runErr
	}
	if m.fatal != nil {
		return m.fatal
	}
	// Quitting mid-recording still finalises the capture, so say where it went — the
	// popup that would normally carry that never gets a frame to be drawn in.
	if m.farewell != "" {
		for _, line := range strings.Split(m.farewell, "\n") {
			fmt.Printf("  %s\n", line)
		}
	}
	return nil
}

// cameraSlot serialises everything that opens or closes a camera. A camera tolerates one
// session, so the framing check has to be fully released before a recording opens its
// own; holding one lock across both makes that ordering structural rather than timed.
type cameraSlot struct {
	mu sync.Mutex
	// open is the framing camera, guarded by mu.
	open framingCamera
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
