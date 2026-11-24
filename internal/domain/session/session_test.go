package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCivilDaysKnownDates(t *testing.T) {
	// The Rust version hand-rolled Hinnant's civil-date conversions; these are the dates
	// its tests pinned, now checked against the time-package implementation.
	tests := []struct {
		y, m, d int64
		want    int64
	}{
		{1970, 1, 1, 0},
		{2024, 1, 1, 19_723},
		// A leap day, the case most likely to be off by one.
		{2024, 2, 29, 19_782},
		{2024, 3, 1, 19_783},
		{1969, 12, 31, -1},
		// Out-of-range fields roll over rather than failing, as before.
		{2023, 13, 1, 19_723},
	}
	for _, tt := range tests {
		if got := civilDays(tt.y, tt.m, tt.d); got != tt.want {
			t.Errorf("civilDays(%d, %d, %d) = %d, want %d", tt.y, tt.m, tt.d, got, tt.want)
		}
	}
}

func TestAgeDaysUsesLocalCalendarDays(t *testing.T) {
	zone := time.FixedZone("UTC+10", 10*3600)
	s := &Session{Dir: "/tmp/2024-02-28_23-59-00_demo"}
	tests := []struct {
		now  time.Time
		want int64
	}{
		{time.Date(2024, 2, 28, 23, 59, 59, 0, zone), 0},
		{time.Date(2024, 2, 29, 0, 0, 0, 0, zone), 1},
		{time.Date(2024, 3, 1, 9, 0, 0, 0, zone), 2},
		// A folder from the future (clock change, copied in) is age 0, not negative.
		{time.Date(2024, 1, 1, 0, 0, 0, 0, zone), 0},
	}
	for _, tt := range tests {
		got, ok := s.ageDaysAt(tt.now)
		if !ok || got != tt.want {
			t.Errorf("age at %v = %d, %v; want %d", tt.now, got, ok, tt.want)
		}
	}
	if _, ok := (&Session{Dir: "/tmp/not-a-date"}).AgeDays(); ok {
		t.Error("a non-date folder name should have no age")
	}
}

func TestTimestampLayoutMatchesFolderNames(t *testing.T) {
	got := time.Date(2026, 9, 28, 11, 6, 17, 0, time.Local).Format(stampLayout)
	if got != "2026-09-28_11-06-17" {
		t.Errorf("stamp = %q", got)
	}
}

func TestSanitizeLabel(t *testing.T) {
	tests := []struct{ in, want string }{
		// Only characters a folder name can carry.
		{"onboarding demo", "onboarding-demo"},
		{"  spaced  out  ", "spaced-out"},
		{"v2.1 (final)", "v2-1-final"},
		{"café", "caf"},
		// The label is pasted straight into a path, so separators and dot segments are
		// the cases that matter.
		{"../../etc/passwd", "etc-passwd"},
		{"/absolute", "absolute"},
		{"..", ""},
		{"///", ""},
	}
	for _, tt := range tests {
		if got := sanitizeLabel(tt.in); got != tt.want {
			t.Errorf("sanitizeLabel(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestALabelledFolderStillReportsItsTimestampAndLabel(t *testing.T) {
	plain := &Session{Dir: "/tmp/2026-09-28_11-06-17"}
	if l, ok := plain.Label(); ok {
		t.Errorf("plain folder has label %q", l)
	}

	labelled := &Session{Dir: "/tmp/2026-09-28_11-06-17_onboarding-demo"}
	if l, ok := labelled.Label(); !ok || l != "onboarding-demo" {
		t.Errorf("label = %q, %v", l, ok)
	}
	// Age is read off the folder name, so labelling must not break it.
	if _, ok := labelled.AgeDays(); !ok {
		t.Error("labelled folder has no age")
	}
}

func TestSetLabelRenamesAndKeepsTheStamp(t *testing.T) {
	root := t.TempDir()
	s := &Session{Dir: filepath.Join(root, "2026-09-28_11-06-17")}
	if err := os.Mkdir(s.Dir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := s.SetLabel("Onboarding demo!"); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "2026-09-28_11-06-17_Onboarding-demo"); s.Dir != want {
		t.Errorf("dir = %s, want %s", s.Dir, want)
	}
	if !exists(s.Dir) {
		t.Error("renamed folder missing")
	}

	// Relabelling to the same name is a no-op, an empty label removes it.
	if err := s.SetLabel("Onboarding demo!"); err != nil {
		t.Errorf("no-op relabel: %v", err)
	}
	if err := s.SetLabel("   "); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "2026-09-28_11-06-17"); s.Dir != want {
		t.Errorf("dir = %s, want %s", s.Dir, want)
	}

	// Never clobber another recording.
	if err := os.Mkdir(filepath.Join(root, "2026-09-28_11-06-17_taken"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLabel("taken"); err == nil {
		t.Error("expected an error renaming onto an existing folder")
	}
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func TestPrivateFilesAndDirs(t *testing.T) {
	root := t.TempDir()

	dir := filepath.Join(root, "a", "b")
	if err := createPrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	if m := mode(t, dir); m != 0o700 {
		t.Errorf("new dir mode = %o", m)
	}
	// An existing laxer directory is tightened.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := createPrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	if m := mode(t, dir); m != 0o700 {
		t.Errorf("existing dir mode = %o", m)
	}

	file := filepath.Join(dir, "meta.json")
	if err := os.WriteFile(file, []byte("old contents"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivate(file, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if m := mode(t, file); m != 0o600 {
		t.Errorf("file mode = %o", m)
	}
	if b, _ := os.ReadFile(file); string(b) != "{}" {
		t.Errorf("contents = %q", b)
	}

	other := filepath.Join(dir, "export.mp4")
	if err := os.WriteFile(other, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Restrict(other); err != nil {
		t.Fatal(err)
	}
	if m := mode(t, other); m != 0o600 {
		t.Errorf("restricted mode = %o", m)
	}
	if err := Restrict(filepath.Join(dir, "missing")); err != nil {
		t.Errorf("restricting a missing file: %v", err)
	}
}

func TestDropRawKeepsTheExport(t *testing.T) {
	s := &Session{Dir: t.TempDir()}
	for _, p := range []string{s.Capture(), s.Telemetry(), s.Camera(), s.Export(), s.Meta()} {
		if err := os.WriteFile(p, []byte("12345"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.RawBytes(); got != 15 {
		t.Errorf("RawBytes = %d, want 15", got)
	}
	if got := s.Bytes(); got != 25 {
		t.Errorf("Bytes = %d, want 25", got)
	}
	if err := s.DropRaw(); err != nil {
		t.Fatal(err)
	}
	// The camera recording is the most personal file there; it must go too.
	for _, p := range []string{s.Capture(), s.Telemetry(), s.Camera()} {
		if exists(p) {
			t.Errorf("%s survived DropRaw", filepath.Base(p))
		}
	}
	if !s.HasExport() || !exists(s.Meta()) {
		t.Error("DropRaw removed the export or meta")
	}
	if _, err := Open(s.Dir); err == nil {
		t.Error("Open should reject a folder without a capture")
	}
}
