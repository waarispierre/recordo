package ffmpeg

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// ffprobe runs ffprobe with the protocol whitelist and returns trimmed stdout.
func ffprobe(args ...string) (string, error) {
	bin, err := Require("ffprobe")
	if err != nil {
		return "", err
	}
	args = append([]string{"-v", "error", "-protocol_whitelist", "file,pipe,fd"}, args...)
	out, err := exec.Command(bin, args...).Output()
	if err != nil {
		return "", fmt.Errorf("run ffprobe: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// ProbeVideo returns the first video stream's size and the container's duration.
func ProbeVideo(path string) (w, h uint32, durationS float64, err error) {
	text, err := ffprobe("-select_streams", "v:0",
		"-show_entries", "stream=width,height",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", path)
	if err != nil {
		return 0, 0, 0, err
	}
	vals := strings.Fields(text)
	if len(vals) < 3 {
		return 0, 0, 0, fmt.Errorf("unexpected ffprobe output: %q", text)
	}
	pw, err1 := strconv.ParseUint(vals[0], 10, 32)
	ph, err2 := strconv.ParseUint(vals[1], 10, 32)
	d, err3 := strconv.ParseFloat(vals[2], 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, 0, fmt.Errorf("unexpected ffprobe output: %q", text)
	}
	return uint32(pw), uint32(ph), d, nil
}

// HasAudio reports whether the file carries an audio track, i.e. it was recorded with
// voice over.
//
// Asked of the file rather than read from meta.json, so a capture made before the setting
// existed — or one whose microphone failed to start — is judged on what is actually in it.
func HasAudio(path string) (bool, error) {
	text, err := ffprobe("-select_streams", "a:0",
		"-show_entries", "stream=codec_type",
		"-of", "default=noprint_wrappers=1:nokey=1", path)
	return text == "audio", err
}

// AudioBitrateBps is the actual bitrate of the first audio stream, when the container
// records one.
func AudioBitrateBps(path string) (uint64, bool, error) {
	text, err := ffprobe("-select_streams", "a:0",
		"-show_entries", "stream=bit_rate",
		"-of", "default=noprint_wrappers=1:nokey=1", path)
	if err != nil {
		return 0, false, err
	}
	v, perr := strconv.ParseUint(text, 10, 64)
	return v, perr == nil, nil
}
