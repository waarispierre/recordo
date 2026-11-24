package render

import (
	"bytes"
	"github.com/waarispierre/recordo/internal/platform/gpu"
	"io"
	"syscall"
	"testing"
)

// stream is n frames of size bytes each, frame i filled with byte i.
func stream(n, size int) io.Reader {
	var b bytes.Buffer
	for i := range n {
		b.Write(bytes.Repeat([]byte{byte(i)}, size))
	}
	return &b
}

func collect(r *reader) (srcIDs, camIDs []int) {
	for f := range r.frames {
		srcIDs = append(srcIDs, int(f.src[0]))
		if c := f.camFrame(); c != nil {
			camIDs = append(camIDs, int(c[0]))
		} else {
			camIDs = append(camIDs, -1)
		}
		r.release(f)
	}
	return
}

func webcam(offset int64, frames int) *webcamSetup {
	return &webcamSetup{
		geometry:    gpu.WebcamGeometry{Width: 1, Height: 1},
		stdout:      io.NopCloser(stream(frames, 4)),
		frameOffset: offset,
	}
}

func TestReaderEndsCleanlyOnAShortFinalFrame(t *testing.T) {
	r := startReader(io.MultiReader(stream(3, 8), bytes.NewReader([]byte{1, 2})), nil, 8, 0)
	src, _ := collect(r)
	if len(src) != 3 || r.err != nil {
		t.Fatalf("got %v frames, err %v", src, r.err)
	}
}

func TestReaderTimestampsFramesOnTheFixedGrid(t *testing.T) {
	r := startReader(stream(3, 8), nil, 8, 1000)
	var ts []uint64
	for f := range r.frames {
		ts = append(ts, f.t)
		r.release(f)
	}
	if ts[0] != 1000 || ts[2] != 1000+2*1_000_000_000/FPS {
		t.Fatalf("timestamps %v", ts)
	}
}

func TestWebcamThatStartedLateWaitsForTheScreen(t *testing.T) {
	_, cam := collect(startReader(stream(5, 8), webcam(2, 10), 8, 0))
	want := []int{-1, -1, 0, 1, 2}
	for i := range want {
		if cam[i] != want[i] {
			t.Fatalf("camera frames %v, want %v", cam, want)
		}
	}
}

func TestWebcamThatStartedEarlyDropsItsLeadingFrames(t *testing.T) {
	_, cam := collect(startReader(stream(3, 8), webcam(-2, 10), 8, 0))
	if cam[0] != 2 || cam[2] != 4 {
		t.Fatalf("camera frames %v, want to start at 2", cam)
	}
}

func TestOverlayStopsRatherThanFreezesWhenTheWebcamRunsOut(t *testing.T) {
	_, cam := collect(startReader(stream(4, 8), webcam(0, 2), 8, 0))
	if cam[1] != 1 || cam[2] != -1 || cam[3] != -1 {
		t.Fatalf("camera frames %v", cam)
	}
}

type closedPipe struct{}

func (closedPipe) Write([]byte) (int, error) { return 0, syscall.EPIPE }

func TestWriterTreatsAClosedEncoderAsDoneNotFailed(t *testing.T) {
	w := startWriter(closedPipe{}, 4)
	buf, _ := w.buffer()
	w.send(append(buf, 1, 2, 3, 4))
	<-w.done
	if w.err != nil {
		t.Fatalf("EPIPE should not be an error: %v", w.err)
	}
	if _, ok := w.buffer(); ok {
		t.Fatal("a stopped writer should hand out no more buffers")
	}
}

func TestWriterPreservesFrameOrder(t *testing.T) {
	var got bytes.Buffer
	w := startWriter(&got, 1)
	for i := range 10 {
		b, _ := w.buffer()
		w.send(append(b, byte(i)))
	}
	close(w.frames)
	<-w.done
	if !bytes.Equal(got.Bytes(), []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}) {
		t.Fatalf("got %v", got.Bytes())
	}
}
