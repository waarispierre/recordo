package recordings

import (
	"testing"

	"github.com/waarispierre/recordo/internal/features/compress"
)

func TestSummarizeCompress(t *testing.T) {
	got := summarizeCompress(compress.CompressReport{
		InputBytes: 90_000_000, OutputBytes: 30_000_000, MaxBytes: 25_000_000, Reencoded: true, VideoBitrateBps: 4_000_000,
	}, 25, "/x/export-compressed.mp4")
	want := "90.0 MB → 30.0 MB · 4.0 Mbps video\nstill 30.0 MB, over the 25 MB target — try a lower size\nsaved to /x/export-compressed.mp4"
	if got != want {
		t.Fatalf("got %q", got)
	}
}
