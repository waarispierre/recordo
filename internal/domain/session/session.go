// Package session decides where recordings and settings live.
//
// A tool on PATH gets run from arbitrary directories, so nothing is written relative to
// the working directory. Each recording gets its own timestamped folder holding the raw
// capture, its sidecars and the finished video, which keeps them from drifting out of
// sync with each other.
package session

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Recordings are of your screen, so nothing here should be readable by other local
// accounts. Directories are owner-only; files are created 0600 rather than chmod'ed
// afterwards, so there is no window where the content exists at a laxer mode.
const (
	dirMode  fs.FileMode = 0o700
	fileMode fs.FileMode = 0o600
)

// stampLayout is the folder name: `YYYY-MM-DD_HH-MM-SS`, in local time.
const stampLayout = "2006-01-02_15-04-05"

// createPrivateDir creates a directory owner-only, tightening it if it already exists
// laxer. Parents are created with the usual umask-governed mode, as the Rust version did.
func createPrivateDir(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := os.Mkdir(path, dirMode); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("create %s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("create %s: not a directory", path)
	}
	if info.Mode().Perm() != dirMode {
		if err := os.Chmod(path, dirMode); err != nil {
			return fmt.Errorf("restrict %s: %w", path, err)
		}
	}
	return nil
}

// WritePrivate writes a file that only the owner can read.
func WritePrivate(path string, contents []byte) (err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fileMode)
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("write %s: %w", path, cerr)
		}
	}()
	if _, err := f.Write(contents); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	// O_CREATE leaves the mode alone on an existing file, so enforce it explicitly.
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if info.Mode().Perm() != fileMode {
		if err := f.Chmod(fileMode); err != nil {
			return fmt.Errorf("restrict %s: %w", path, err)
		}
	}
	return nil
}

// Restrict tightens a file produced by something other than us — ffmpeg's output, or
// ScreenCaptureKit's capture — which is created with the default umask. A missing file
// is not an error.
func Restrict(path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("restrict %s: %w", path, err)
	}
	if info.Mode().Perm() != fileMode {
		if err := os.Chmod(path, fileMode); err != nil {
			return fmt.Errorf("restrict %s: %w", path, err)
		}
	}
	return nil
}

// ConfigPath is ~/Library/Application Support/recordo/config.toml. The directory is
// created owner-only if needed.
func ConfigPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("could not locate a config directory: %w", err)
	}
	dir := filepath.Join(base, "recordo")
	if err := createPrivateDir(dir); err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

// RecordingsDir is ~/Movies/Recordo, created owner-only if needed.
func RecordingsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not locate your home directory: %w", err)
	}
	dir := filepath.Join(home, "Movies", "Recordo")
	if err := createPrivateDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}

// Session is one recording: raw capture, sidecars and rendered output in a single folder.
type Session struct {
	Dir string
}

// Create makes a new recording folder named for the current local time.
func Create() (*Session, error) {
	root, err := RecordingsDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, time.Now().Format(stampLayout))
	if err := createPrivateDir(dir); err != nil {
		return nil, err
	}
	return &Session{Dir: dir}, nil
}

// Open is an existing recording folder; it must hold a capture.
func Open(dir string) (*Session, error) {
	s := &Session{Dir: dir}
	if !exists(s.Capture()) {
		return nil, fmt.Errorf("%s does not contain a capture.mp4", dir)
	}
	return s, nil
}

// Latest is the most recent recording, for re-rendering without naming it.
func Latest() (*Session, error) {
	root, err := RecordingsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", root, err)
	}
	// ReadDir sorts by name, and names start with the timestamp, so the last one with a
	// capture is the newest.
	for i := len(entries) - 1; i >= 0; i-- {
		dir := filepath.Join(root, entries[i].Name())
		if exists(filepath.Join(dir, "capture.mp4")) {
			return &Session{Dir: dir}, nil
		}
	}
	return nil, errors.New("no recordings yet — run `recordo` to make one")
}

// All is every recording folder, oldest first.
func All() ([]*Session, error) {
	root, err := RecordingsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", root, err)
	}
	var out []*Session
	for _, e := range entries {
		dir := filepath.Join(root, e.Name())
		// Stat rather than e.IsDir() so a symlinked folder counts, as in the Rust version.
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			out = append(out, &Session{Dir: dir})
		}
	}
	return out, nil
}

func (s *Session) Capture() string      { return filepath.Join(s.Dir, "capture.mp4") }
func (s *Session) Telemetry() string    { return filepath.Join(s.Dir, "telemetry.json") }
func (s *Session) Frames() string       { return filepath.Join(s.Dir, "frames.json") }
func (s *Session) Meta() string         { return filepath.Join(s.Dir, "meta.json") }
func (s *Session) Export() string       { return filepath.Join(s.Dir, "export.mp4") }
func (s *Session) Camera() string       { return filepath.Join(s.Dir, "camera.mp4") }
func (s *Session) CameraFrames() string { return filepath.Join(s.Dir, "camera_frames.json") }

// HasExport reports whether the recording has been rendered.
func (s *Session) HasExport() bool { return exists(s.Export()) }

// rawFiles are the raw capture and its sidecars — everything Prune would remove.
func (s *Session) rawFiles() []string {
	return []string{s.Capture(), s.Telemetry(), s.Frames(), s.Camera(), s.CameraFrames()}
}

// RawBytes is the bytes held by the raw capture and its sidecars — everything DropRaw
// would remove.
func (s *Session) RawBytes() uint64 {
	var total uint64
	for _, p := range s.rawFiles() {
		if info, err := os.Stat(p); err == nil {
			total += uint64(info.Size())
		}
	}
	return total
}

// DropRaw deletes the raw capture and its sidecars, keeping the rendered video.
//
// The raw capture is the unredacted one: for a browser it still contains the tab strip,
// address bar and bookmarks that the render deliberately removes. Keeping it forever
// means the redaction only ever applied to the copy you share. camera.mp4, when present,
// is the most personal file in the folder — a raw recording of your face — so leaving it
// out here would be a privacy bug, not just an oversight.
func (s *Session) DropRaw() error {
	for _, p := range s.rawFiles() {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", p, err)
		}
	}
	return nil
}

// Bytes is every byte the folder holds, raw and rendered alike. Unreadable entries count
// as zero.
func (s *Session) Bytes() uint64 {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return 0
	}
	var total uint64
	for _, e := range entries {
		if info, err := e.Info(); err == nil {
			total += uint64(info.Size())
		}
	}
	return total
}

// Label is the part of the folder name after the timestamp, if it has been labelled.
func (s *Session) Label() (string, bool) {
	// `YYYY-MM-DD_HH-MM-SS_label`: the stamp is the first two underscore-separated
	// fields, anything after that is the label.
	parts := strings.SplitN(filepath.Base(s.Dir), "_", 3)
	if len(parts) < 3 || parts[2] == "" {
		return "", false
	}
	return parts[2], true
}

// SetLabel renames the folder to carry label, keeping its timestamp prefix. An empty
// label removes one that was there.
//
// The timestamp is deliberately not up for editing: AgeDays reads a recording's age
// straight off the folder name, and All sorts by it, so a free-form name would quietly
// drop the recording out of `prune --older-than` and confuse which recording counts as
// the latest.
func (s *Session) SetLabel(label string) error {
	name := filepath.Base(s.Dir)
	parts := strings.SplitN(name, "_", 3)
	stamp := strings.Join(parts[:min(len(parts), 2)], "_")

	renamed := stamp
	if l := sanitizeLabel(label); l != "" {
		renamed = stamp + "_" + l
	}
	target := filepath.Join(filepath.Dir(s.Dir), renamed)
	if target == filepath.Clean(s.Dir) {
		return nil
	}
	if exists(target) {
		return fmt.Errorf("%s already exists", renamed)
	}
	if err := os.Rename(s.Dir, target); err != nil {
		return fmt.Errorf("rename %s to %s: %w", s.Dir, renamed, err)
	}
	s.Dir = target
	return nil
}

// AgeDays is the recording's age in whole local days, derived from the folder name
// rather than mtime, which re-rendering would otherwise reset. ok is false when the name
// does not start with a date.
func (s *Session) AgeDays() (days int64, ok bool) {
	return s.ageDaysAt(time.Now())
}

func (s *Session) ageDaysAt(now time.Time) (int64, bool) {
	stamp, _, _ := strings.Cut(filepath.Base(s.Dir), "_")
	fields := strings.Split(stamp, "-")
	if len(fields) < 3 {
		return 0, false
	}
	var ymd [3]int64
	for i := range ymd {
		n, err := strconv.ParseInt(fields[i], 10, 64)
		if err != nil {
			return 0, false
		}
		ymd[i] = n
	}
	then := civilDays(ymd[0], ymd[1], ymd[2])
	_, offset := now.Zone()
	today := floorDiv(now.Unix()+int64(offset), 86_400)
	return max(today-then, 0), true
}

// civilDays is the day number of a calendar date, counted from 1970-01-01. Out-of-range
// months and days roll over, as they did in the Rust version's days_from_civil.
func civilDays(y, m, d int64) int64 {
	t := time.Date(int(y), time.Month(m), int(d), 0, 0, 0, 0, time.UTC)
	return floorDiv(t.Unix(), 86_400)
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// sanitizeLabel is the folder-safe form of a typed label. A rename lands in a filesystem
// path, so rather than rejecting awkward input this reduces it to letters, digits and
// dashes — which also stops `..` or a stray separator from climbing out of the
// recordings directory.
func sanitizeLabel(label string) string {
	var b strings.Builder
	for _, c := range strings.TrimSpace(label) {
		switch {
		case c < 0x80 && (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'):
			b.WriteRune(c)
		case !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
