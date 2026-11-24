// Package render is the render slice: a recording's capture and sidecars in, a finished
// export.mp4 out. Frames are decoded by ffmpeg, composited on the GPU and encoded by
// ffmpeg again, with the three stages overlapping (see pipeline.go).
package render

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	_ "golang.org/x/image/webp"

	"github.com/waarispierre/recordo/internal/domain/camera"
	"github.com/waarispierre/recordo/internal/domain/config"
	"github.com/waarispierre/recordo/internal/domain/frames"
	"github.com/waarispierre/recordo/internal/domain/pauses"
	"github.com/waarispierre/recordo/internal/domain/pip"
	"github.com/waarispierre/recordo/internal/domain/session"
	"github.com/waarispierre/recordo/internal/domain/telemetry"
	"github.com/waarispierre/recordo/internal/platform/clock"
	"github.com/waarispierre/recordo/internal/platform/ffmpeg"
	"github.com/waarispierre/recordo/internal/platform/gpu"
)

// FPS is the fixed output frame rate.
const FPS = 60

// CropSource is how the browser page region was determined, if at all.
type CropSource int

const (
	// CropNone: the whole capture was used.
	CropNone CropSource = iota
	// CropAccessibility: exact page bounds from the accessibility tree.
	CropAccessibility
	// CropFixedGuess: the fixed browser_crop_top guess, because the real bounds were
	// unavailable.
	CropFixedGuess
)

// Report is what a render did, for the caller to present.
type Report struct {
	Frames        int
	VideoSeconds  float64
	RenderSeconds float64
	OutW, OutH    uint32
	Chrome        config.Chrome
	CropSource    CropSource
	AppName       string
	// Audio reports whether a voice-over track was carried through into the export.
	Audio bool
	// Webcam reports whether the webcam overlay was composited into the export.
	Webcam bool
}

// RealtimeRatio is video seconds rendered per wall-clock second.
func (r Report) RealtimeRatio() float64 {
	if r.RenderSeconds > 0 {
		return r.VideoSeconds / r.RenderSeconds
	}
	return 0
}

// FPS is frames rendered per wall-clock second.
func (r Report) FPS() float64 {
	if r.RenderSeconds > 0 {
		return float64(r.Frames) / r.RenderSeconds
	}
	return 0
}

// Options tune one render.
type Options struct {
	// ZoomPercent, when non-nil, overrides the configured zoom for this render only.
	ZoomPercent *float64
	// Progress, when non-nil, is called once per rendered frame with (done, total).
	Progress func(done, total int)
	// Webcam, when non-nil, overrides the config's webcam.enabled for this render, so a
	// recording made with a one-off -webcam gets its overlay without editing the config.
	Webcam *bool
}

// meta is the subset of meta.json the render reads. Every field is optional: older
// recordings lack some of them.
type meta struct {
	SourceW  *float64       `json:"source_w"`
	DisplayW *float64       `json:"display_w"`
	OriginX  float64        `json:"origin_x"`
	OriginY  float64        `json:"origin_y"`
	BundleID string         `json:"bundle_id"`
	AppName  string         `json:"app_name"`
	Pauses   []pauses.Pause `json:"pauses"`
	WebRect  *struct {
		X, Y, W, H float64
	} `json:"web_rect"`
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

// Run composites src into dst using the sidecars written alongside the recording.
func Run(src, dst string, opts Options) (rep Report, err error) {
	// ffmpeg has no `--` terminator, so a path starting with '-' becomes an option.
	if err := ffmpeg.CheckNotOptionLike("input", src); err != nil {
		return rep, err
	}
	if err := ffmpeg.CheckNotOptionLike("output", dst); err != nil {
		return rep, err
	}

	// Sidecars live beside the capture, so a recording stays self-contained wherever it
	// is and the tool works from any working directory.
	dir := filepath.Dir(src)
	sidecar := func(name string) string { return filepath.Join(dir, name) }

	var tel telemetry.Telemetry
	if err := readJSON(sidecar("telemetry.json"), &tel); err != nil {
		return rep, err
	}
	var frameLog []frames.Record
	if err := readJSON(sidecar("frames.json"), &frameLog); err != nil {
		return rep, err
	}

	w, h, durationS, err := ffmpeg.ProbeVideo(src)
	if err != nil {
		return rep, err
	}

	// Decode at a fixed cadence so frame i is exactly i/FPS into the recording. The
	// capture's own frame timing is variable, so this resampling is what lets the camera
	// solve line up with what ffmpeg actually emits.
	var baseT uint64
	found := false
	for _, f := range frameLog {
		if f.DisplayTimeNs != nil {
			baseT, found = *f.DisplayTimeNs, true
			break
		}
	}
	if !found {
		return rep, errors.New("no frame carried a display_time")
	}

	// Telemetry is recorded in logical points; scale it into the captured pixel grid.
	// A missing or unreadable meta.json just means defaults, as in older recordings.
	var m meta
	if b, err := os.ReadFile(sidecar("meta.json")); err == nil {
		var parsed meta
		if json.Unmarshal(b, &parsed) == nil {
			m = parsed
		}
	}
	sourceW := float64(w)
	if m.SourceW != nil {
		sourceW = *m.SourceW
	} else if m.DisplayW != nil {
		sourceW = *m.DisplayW
	}
	pointScale := float64(w) / sourceW

	// Solve for exactly as many frames as the fps filter will emit, so the camera does not
	// freeze on the tail of the clip. The camera runs on recorded time, so a pause neither
	// ends a click zoom early nor lets the follow drift: the frame after a resume
	// continues from the one before it.
	nFrames := uint64(math.Round(durationS*FPS)) + 1
	times := make([]uint64, 0, nFrames)
	for i := range nFrames {
		if t, ok := pauses.RecordedTime(m.Pauses, baseT+i*1_000_000_000/FPS); ok {
			times = append(times, t)
		}
	}
	tel.Events = remapEvents(tel.Events, m.Pauses)

	// Cursor telemetry is global and in points; the capture may be a window somewhere on
	// screen, recorded at Retina scale. Translate, then scale.
	if m.OriginX != 0 || m.OriginY != 0 || math.Abs(pointScale-1) > 1e-6 {
		for i := range tel.Events {
			tel.Events[i].X = (tel.Events[i].X - m.OriginX) * pointScale
			tel.Events[i].Y = (tel.Events[i].Y - m.OriginY) * pointScale
		}
	}

	cfgPath, err := session.ConfigPath()
	if err != nil {
		return rep, err
	}
	cfg, err := config.LoadOrCreate(cfgPath)
	if err != nil {
		return rep, err
	}
	if opts.ZoomPercent != nil {
		cfg.Camera.ZoomPercent = *opts.ZoomPercent
	}
	appName := m.AppName
	if appName == "" {
		appName = "display"
	}
	chromeMode, cropTopPt := cfg.ResolveChrome(m.BundleID)
	style := cfg.RenderStyle(m.BundleID)

	// For browsers, exclude the real tab/address strip so the drawn frame replaces it —
	// that is what removes tabs, bookmarks, the profile avatar and URL history.
	//
	// Prefer the page bounds the browser itself reported at capture time; a fixed crop is
	// only a fallback, since it breaks when a bookmarks bar is toggled and differs per
	// browser.
	cropSource := CropNone
	content := contentRect{W: float64(w), H: float64(h)}
	if chromeMode == config.ChromeBrowser {
		if r := m.WebRect; r != nil {
			// The web rect is in screen points; convert to capture pixels.
			content = contentRect{
				X: math.Max((r.X-m.OriginX)*pointScale, 0),
				Y: math.Max((r.Y-m.OriginY)*pointScale, 0),
				W: r.W * pointScale,
				H: r.H * pointScale,
			}
			cropSource = CropAccessibility
		} else {
			inset := math.Max(math.Min(float64(cropTopPt)*pointScale, float64(h)-1), 0)
			content = contentRect{Y: inset, W: float64(w), H: float64(h) - inset}
			cropSource = CropFixedGuess
		}
	}
	content = content.clamped(float64(w), float64(h))

	// Solve in content space.
	for i := range tel.Events {
		tel.Events[i].X -= content.X
		tel.Events[i].Y -= content.Y
	}
	camCfg := cfg.CameraConfig()
	crops := camera.Solve(&tel, times, content.W, content.H, camCfg)

	var background *gpu.Image
	if path, err := cfg.BackgroundPath(); err != nil {
		return rep, err
	} else if path != "" {
		if background, err = loadImage(path); err != nil {
			return rep, err
		}
	}

	// Style is authored in points; scale it to the captured pixel grid.
	style = gpu.Scaled(style, float32(pointScale))

	contentWPx, contentHPx := uint32(math.Round(content.W)), uint32(math.Round(content.H))
	outW, outH := gpu.OutputSize(contentWPx, contentHPx, style)

	// Fixed content rect within the full captured frame, normalized. The click-zoom effect
	// never resamples the source — it only scales and pans the rendered window — so this
	// is the same window content every frame regardless of zoom.
	contentUV := [4]float32{
		float32(content.X / float64(w)), float32(content.Y / float64(h)),
		float32(content.W / float64(w)), float32(content.H / float64(h)),
	}
	zooms := make([]gpu.WindowZoom, len(crops))
	for i, c := range crops {
		zooms[i] = gpu.ZoomFor(c, content.W, content.H, camCfg.MaxZoom, outW, outH, style.Padding)
	}
	if len(zooms) == 0 {
		return rep, errors.New("camera produced no crops")
	}

	var cam *webcamSetup
	if opts.Webcam != nil {
		cfg.Webcam.Enabled = *opts.Webcam
	}
	webcamCfg := cfg.ClampedWebcam()
	if webcamCfg.Enabled && fileExists(sidecar("camera.mp4")) {
		cam, err = startWebcam(webcamCfg, sidecar("camera.mp4"), sidecar("camera_frames.json"),
			outW, outH, float32(pointScale), baseT)
		if err != nil {
			return rep, err
		}
	}
	var camGeo *gpu.WebcamGeometry
	if cam != nil {
		camGeo = &cam.geometry
		// Every exit path below must reap the webcam decoder.
		defer cam.close()
	}

	renderer, err := gpu.NewRenderer(w, h, outW, outH, style, background, camGeo, contentUV)
	if err != nil {
		return rep, err
	}
	defer renderer.Release()

	bin, err := ffmpeg.Require("ffmpeg")
	if err != nil {
		return rep, err
	}
	decoder := exec.Command(bin,
		"-v", "error",
		// Never follow a reference out of the input file: ffmpeg's hls/concat/dash
		// demuxers will otherwise fetch remote URLs named inside a file, which is the one
		// way data here could reach the network. `fd` and `pipe` are the local plumbing
		// between these processes, not network protocols.
		"-nostdin", "-protocol_whitelist", "file,pipe,fd",
		"-i", src,
		"-vf", fmt.Sprintf("fps=%d", FPS),
		"-f", "rawvideo", "-pix_fmt", "rgba", "-")
	decoder.Stderr = os.Stderr
	decOut, err := decoder.StdoutPipe()
	if err != nil {
		return rep, fmt.Errorf("ffmpeg decoder pipe: %w", err)
	}
	if err := decoder.Start(); err != nil {
		return rep, fmt.Errorf("spawn ffmpeg decoder: %w", err)
	}
	defer func() {
		// Killing is harmless once the decoder has exited, and essential when we stop
		// early: it would otherwise block writing into a pipe nobody reads, hanging Wait.
		_ = decoder.Process.Kill()
		_ = decoder.Wait()
	}()

	// Composited frames arrive on stdin; the voice over is copied across from the capture
	// as a second input. Its timestamps are ScreenCaptureKit's own, on the same timeline
	// as the video it was recorded with, so no offset has to be applied here — the same
	// goes for cutting out the pauses, measured from the first frame.
	audio, err := ffmpeg.HasAudio(src)
	if err != nil {
		return rep, err
	}
	encArgs := []string{
		"-v", "error", "-protocol_whitelist", "file,pipe,fd", "-y",
		"-f", "rawvideo", "-pix_fmt", "rgba",
		"-s", fmt.Sprintf("%dx%d", outW, outH), "-r", fmt.Sprint(FPS),
		"-i", "-",
	}
	if audio {
		encArgs = append(encArgs, "-i", src, "-map", "0:v:0", "-map", "1:a:0")
	}
	encArgs = append(encArgs, "-c:v", "h264_videotoolbox", "-b:v", "12M", "-pix_fmt", "yuv420p")
	if audio {
		if cut := pauses.AudioSelect(m.Pauses, baseT); cut != "" {
			encArgs = append(encArgs, "-af", cut)
		}
		// The capture runs a little past the last composited frame, so without -shortest
		// the export ends on a still image with the audio still playing.
		encArgs = append(encArgs, "-c:a", "aac", "-b:a", "128k", "-shortest")
	}
	encoder := exec.Command(bin, append(encArgs, dst)...)
	encoder.Stderr = os.Stderr
	encIn, err := encoder.StdinPipe()
	if err != nil {
		return rep, fmt.Errorf("ffmpeg encoder pipe: %w", err)
	}
	if err := encoder.Start(); err != nil {
		return rep, fmt.Errorf("spawn ffmpeg encoder: %w", err)
	}
	encoderWaited := false
	defer func() {
		if !encoderWaited {
			encIn.Close()
			_ = encoder.Process.Kill()
			_ = encoder.Wait()
		}
	}()

	// Decoding, compositing and encoding overlap: the reader and writer goroutines keep
	// ffmpeg's pipes moving while the GPU works on the frame in between. The renderer
	// itself stays on this goroutine.
	rd := startReader(decOut, cam, int(w*h*4), baseT)
	defer close(rd.stop)
	wr := startWriter(encIn, renderer.OutFrameBytes())

	start := clock.NowNanos()
	// rendered counts what survived the pauses into the export, which the zooms were
	// solved for.
	rendered := 0
	var renderErr error
	stoppedEarly := false
	for f := range rd.frames {
		if pauses.IsPausedAt(m.Pauses, f.t) {
			rd.release(f)
			continue
		}
		out, ok := wr.buffer()
		if !ok {
			rd.release(f)
			stoppedEarly = true
			break
		}
		// Hold the last solved zoom if ffmpeg emits more frames than we solved for.
		win := zooms[min(rendered, len(zooms)-1)]
		out, renderErr = renderer.Render(f.src, win, f.camFrame(), out)
		rd.release(f)
		if renderErr != nil || !wr.send(out) {
			stoppedEarly = true
			break
		}
		rendered++
		if opts.Progress != nil {
			opts.Progress(rendered, len(zooms))
		}
	}
	close(wr.frames)
	<-wr.done
	if renderErr != nil {
		return rep, renderErr
	}
	if wr.err != nil {
		return rep, wr.err
	}
	// rd.err is only safe to read, and only meaningful, once the reader has closed
	// rd.frames on its own, which is exactly when the loop ran to completion.
	if !stoppedEarly && rd.err != nil {
		return rep, rd.err
	}

	encIn.Close()
	elapsedNs := clock.NowNanos() - start
	encoderWaited = true
	if err := encoder.Wait(); err != nil {
		return rep, fmt.Errorf("ffmpeg encoder: %w", err)
	}
	// ffmpeg creates the output with the default umask.
	if err := session.Restrict(dst); err != nil {
		return rep, err
	}

	return Report{
		Frames:        rendered,
		VideoSeconds:  float64(rendered) / FPS,
		RenderSeconds: float64(elapsedNs) / 1e9,
		OutW:          outW,
		OutH:          outH,
		Chrome:        chromeMode,
		CropSource:    cropSource,
		AppName:       appName,
		Audio:         audio,
		Webcam:        cam != nil,
	}, nil
}

// remapEvents moves telemetry onto the recorded timeline, the one with pauses cut out.
func remapEvents(events []telemetry.Event, ps []pauses.Pause) []telemetry.Event {
	out := events[:0]
	for _, e := range events {
		t, ok := pauses.RecordedTime(ps, e.TNs)
		if !ok {
			// A click while paused is not part of the video. Movement still counts: the
			// poller only logs changes, so dropping it would leave the camera aimed
			// wherever the cursor was before the pause. It lands at the resume.
			if e.Kind != telemetry.Move {
				continue
			}
			for _, p := range ps {
				if p.StartNs <= e.TNs && e.TNs < p.EndNs {
					t, ok = pauses.RecordedTime(ps, p.EndNs)
					break
				}
			}
			if !ok {
				continue
			}
		}
		e.TNs = t
		out = append(out, e)
	}
	return out
}

// webcamSetup is everything the render loop needs to composite the webcam: where to
// place it, a decoder already producing frames scaled to exactly that size, and how many
// screen frames to skip or wait for before the two streams line up.
type webcamSetup struct {
	geometry    gpu.WebcamGeometry
	decoder     *exec.Cmd
	stdout      io.ReadCloser
	frameOffset int64
}

func (c *webcamSetup) close() {
	_ = c.decoder.Process.Kill()
	_ = c.decoder.Wait()
}

// startWebcam resolves webcam placement and starts its decoder, or returns nil when the
// camera track cannot be used — a missing or zero-duration camera.mp4, or a corrupt
// camera_frames.json. Those are treated as "no webcam this render" rather than a hard
// failure, the same way a failed camera open at record time degrades to screen-only.
func startWebcam(cfg config.WebcamSettings, cameraPath, cameraFramesPath string,
	outW, outH uint32, pointScale float32, screenBaseNs uint64) (*webcamSetup, error) {
	camW, camH, camDurationS, err := ffmpeg.ProbeVideo(cameraPath)
	if err != nil || camW == 0 || camH == 0 || camDurationS <= 0 {
		return nil, nil
	}
	var camFrames []frames.Record
	if readJSON(cameraFramesPath, &camFrames) != nil {
		return nil, nil
	}
	var camFirstNs uint64
	found := false
	for _, f := range camFrames {
		if f.DisplayTimeNs != nil {
			camFirstNs, found = *f.DisplayTimeNs, true
		} else if f.PtsNs != nil {
			camFirstNs, found = *f.PtsNs, true
		}
		if found {
			break
		}
	}
	if !found {
		return nil, nil
	}

	isCircle := strings.EqualFold(cfg.Shape, "circle")
	aspect := float32(1)
	if !isCircle {
		aspect = float32(camW) / float32(camH)
	}
	rect := pip.PlaceRect(float32(outW), float32(outH), pip.ParseCorner(cfg.Position),
		cfg.Inset*pointScale, cfg.Offset[0]*pointScale, cfg.Offset[1]*pointScale,
		cfg.SizePercent, aspect)
	geoW := uint32(max(float32(math.Round(float64(rect.W))), 1))
	geoH := uint32(max(float32(math.Round(float64(rect.H))), 1))
	radius := float32(min(geoW, geoH)) / 2
	if !isCircle {
		radius = min(cfg.CornerRadius*pointScale, float32(geoW)/2, float32(geoH)/2)
	}

	// ffmpeg does the scaling and, for a circle, the centre crop to square, so the GPU only
	// ever uploads a texture already at the overlay's own pixel size.
	filters := []string{fmt.Sprintf("fps=%d", FPS)}
	if cfg.Mirror {
		filters = append(filters, "hflip")
	}
	if isCircle {
		filters = append(filters, "crop=ih:ih")
	}
	filters = append(filters, fmt.Sprintf("scale=%d:%d", geoW, geoH))

	bin, err := ffmpeg.Require("ffmpeg")
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin,
		"-v", "error", "-nostdin", "-protocol_whitelist", "file,pipe,fd",
		"-i", cameraPath,
		"-vf", strings.Join(filters, ","),
		"-f", "rawvideo", "-pix_fmt", "rgba", "-")
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg webcam decoder pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("spawn ffmpeg webcam decoder: %w", err)
	}
	return &webcamSetup{
		geometry: gpu.WebcamGeometry{
			Width: geoW, Height: geoH,
			Rect:   [4]float32{rect.X, rect.Y, rect.W, rect.H},
			Radius: radius,
		},
		decoder:     cmd,
		stdout:      stdout,
		frameOffset: pip.FrameOffset(screenBaseNs, camFirstNs, FPS),
	}, nil
}

// loadImage decodes a PNG, JPEG or WebP background into straight-alpha RGBA.
func loadImage(path string) (*gpu.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open background image %s: %w", path, err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode background image %s: %w", path, err)
	}
	b := img.Bounds()
	rgba := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), img, b.Min, draw.Src)
	return &gpu.Image{RGBA: rgba.Pix, Width: uint32(b.Dx()), Height: uint32(b.Dy())}, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// contentRect is the region of the captured frame worth showing, in capture pixels.
type contentRect struct{ X, Y, W, H float64 }

// clamped never lets a stale rect run past the captured frame.
func (r contentRect) clamped(maxW, maxH float64) contentRect {
	x := clamp(r.X, 0, math.Max(maxW-1, 0))
	y := clamp(r.Y, 0, math.Max(maxH-1, 0))
	return contentRect{
		X: x,
		Y: y,
		W: math.Max(math.Min(r.W, maxW-x), 1),
		H: math.Max(math.Min(r.H, maxH-y), 1),
	}
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(v, hi)) }
