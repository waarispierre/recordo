package ffmpeg

import (
	"path/filepath"
	"testing"
)

func TestRejectsOptionLikePaths(t *testing.T) {
	if CheckNotOptionLike("output", "-f") == nil {
		t.Error("-f should be rejected")
	}
	for _, ok := range []string{"./-weird.mp4", "/tmp/fine.mp4"} {
		if err := CheckNotOptionLike("output", ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
}

func TestFindsACoreSystemBinaryByAbsolutePath(t *testing.T) {
	p, ok := Find("date")
	if !ok || !filepath.IsAbs(p) {
		t.Fatalf("Find(date) = %q, %v", p, ok)
	}
}
