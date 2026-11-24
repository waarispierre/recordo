package render

import (
	"errors"
	"fmt"
	"io"
	"syscall"
)

// pipelineDepth is how many frames each stage may have in flight. Three lets the decoder
// read ahead, the GPU render and the encoder write all at once, without holding much
// memory: at 4K a frame is ~33 MB.
const pipelineDepth = 3

// inFrame is one decoded capture frame and, when the webcam is on, its matching camera
// frame.
type inFrame struct {
	src []byte
	cam []byte // nil when there is no camera frame for this screen frame
	t   uint64 // capture time, mach ns
}

// reader decodes frames on its own goroutine, so ffmpeg's pipe is drained while the GPU
// is busy with the previous frame.
type reader struct {
	frames <-chan *inFrame
	free   chan<- *inFrame
	stop   chan struct{}
	err    error // valid once frames is closed
}

// startReader reads screen frames (and, in lockstep, webcam frames) until either stream
// ends or stop is closed.
//
// Camera frames are read one per screen frame, offset by the webcam's frameOffset: if
// the camera started later, compositing waits until the screen frame count catches up to
// it; if it started earlier, that many of its leading frames are discarded up front so
// the two streams are in lockstep for the rest of the render. Paused frames are still
// read here, so both streams stay aligned across a cut.
func startReader(dec io.Reader, cam *webcamSetup, srcBytes int, baseT uint64) *reader {
	frames := make(chan *inFrame, pipelineDepth)
	free := make(chan *inFrame, pipelineDepth)
	r := &reader{frames: frames, free: free, stop: make(chan struct{})}

	camBytes := 0
	camStartAt := -1
	if cam != nil {
		camBytes = int(cam.geometry.Width * cam.geometry.Height * 4)
		camStartAt = int(max(cam.frameOffset, 0))
	}
	for range pipelineDepth {
		f := &inFrame{src: make([]byte, srcBytes)}
		if camBytes > 0 {
			f.cam = make([]byte, camBytes)
		}
		free <- f
	}

	go func() {
		defer close(frames)
		camEnded := cam == nil
		if cam != nil {
			discard := make([]byte, camBytes)
			for range max(-cam.frameOffset, 0) {
				if _, err := io.ReadFull(cam.stdout, discard); err != nil {
					camEnded = true
					break
				}
			}
		}
		for decoded := 0; ; decoded++ {
			var f *inFrame
			select {
			case f = <-free:
			case <-r.stop:
				return
			}
			if _, err := io.ReadFull(dec, f.src); err != nil {
				// A short or empty final read just means the stream ended.
				if !isEOF(err) {
					r.err = fmt.Errorf("read decoded frame: %w", err)
				}
				return
			}
			f.t = baseT + uint64(decoded)*1_000_000_000/FPS

			hasCam := false
			if !camEnded && decoded >= camStartAt {
				switch _, err := io.ReadFull(cam.stdout, f.cam[:camBytes]); {
				case err == nil:
					hasCam = true
				case isEOF(err):
					camEnded = true
				default:
					r.err = fmt.Errorf("read decoded webcam frame: %w", err)
					return
				}
			}
			if hasCam {
				f.cam = f.cam[:camBytes]
			} else if f.cam != nil {
				f.cam = f.cam[:0]
			}
			// Never blocks: frames has room for every buffer in the pool.
			frames <- f
		}
	}()
	return r
}

// camFrame is the webcam frame to composite, or nil for none.
func (f *inFrame) camFrame() []byte {
	if len(f.cam) == 0 {
		return nil
	}
	return f.cam
}

// release hands a frame's buffers back to the reader. The renderer copies a frame into
// GPU memory before Render returns, so this is safe straight after rendering.
func (r *reader) release(f *inFrame) {
	f.cam = f.cam[:cap(f.cam)]
	r.free <- f
}

// writer feeds composited frames to the encoder on its own goroutine.
type writer struct {
	frames chan<- []byte
	free   <-chan []byte
	done   chan struct{}
	err    error // valid once done is closed
}

func startWriter(enc io.Writer, frameBytes int) *writer {
	frames := make(chan []byte, pipelineDepth)
	free := make(chan []byte, pipelineDepth)
	for range pipelineDepth {
		free <- make([]byte, 0, frameBytes)
	}
	w := &writer{frames: frames, free: free, done: make(chan struct{})}
	go func() {
		defer close(w.done)
		for buf := range frames {
			if _, err := enc.Write(buf); err != nil {
				// -shortest (used when a voice-over track is present) can end the encoder
				// as soon as the audio input finishes decoding, which races the video side:
				// the container's duration includes the audio, so it can be a few tens of
				// milliseconds longer than the video stream's own, and frames are solved for
				// the container duration. A closed pipe means the encoder already has
				// everything it needs, not that anything failed.
				if !errors.Is(err, syscall.EPIPE) {
					w.err = fmt.Errorf("write frame to encoder: %w", err)
				}
				return
			}
			free <- buf
		}
	}()
	return w
}

// buffer returns an empty output buffer, or false once the writer has stopped.
func (w *writer) buffer() ([]byte, bool) {
	// Checked first: select picks at random among ready cases, and a spare buffer must not
	// win over a stopped writer, or the caller renders a frame nobody will encode.
	select {
	case <-w.done:
		return nil, false
	default:
	}
	select {
	case b := <-w.free:
		return b[:0], true
	case <-w.done:
		return nil, false
	}
}

// send queues a composited frame, returning false once the writer has stopped.
func (w *writer) send(b []byte) bool {
	select {
	case w.frames <- b:
		return true
	case <-w.done:
		return false
	}
}

func isEOF(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}
