package record

import (
	"testing"

	"github.com/waarispierre/recordo/internal/domain/frames"
	"github.com/waarispierre/recordo/internal/domain/telemetry"
)

func TestHealthMeasuresTheGapBetweenFramesAndCursorSamples(t *testing.T) {
	var fl []frames.Record
	var tel telemetry.Telemetry
	for i := range uint64(100) {
		ft := i * 16_666_666
		fl = append(fl, frames.Record{DisplayTimeNs: &ft})
		// Cursor samples land 2ms after each frame.
		tel.Events = append(tel.Events, telemetry.Event{TNs: ft + 2_000_000, Kind: telemetry.Move})
	}
	tel.Events = append(tel.Events, telemetry.Event{TNs: 5, Kind: telemetry.Down})
	h := health(&tel, fl)
	if h.Frames != 100 || h.CursorSamples != 100 || h.Clicks != 1 {
		t.Fatalf("counts %+v", h)
	}
	if h.MedianGapMs < 1.9 || h.MedianGapMs > 2.1 {
		t.Fatalf("median gap %.2fms, want ~2", h.MedianGapMs)
	}
}
