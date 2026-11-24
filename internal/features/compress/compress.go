// Package compress is the compress slice: re-encode a rendered export so it fits under a
// size limit, e.g. for a PR or CI upload.
package compress

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"

	"github.com/waarispierre/recordo/internal/domain/session"
	"github.com/waarispierre/recordo/internal/platform/ffmpeg"
)

// CompressReport is the result of CompressToSize.
type CompressReport struct {
	InputBytes, OutputBytes, MaxBytes uint64
	// Reencoded is false when the input already fit under MaxBytes and was copied
	// byte-for-byte rather than re-encoded.
	Reencoded bool
	// VideoBitrateBps is the bitrate of the final encode attempt, if re-encoded.
	VideoBitrateBps uint64
}

// Fits reports whether the output is within budget.
func (r CompressReport) Fits() bool { return r.OutputBytes <= r.MaxBytes }

const (
	// sizeSafety is subtracted from the naive bitrate budget up front: container overhead
	// and h264_videotoolbox's own overshoot both eat into it, and undershooting slightly is
	// cheaper than needing a second encode pass.
	sizeSafety = 0.92
	// maxEncodeAttempts: hardware VBR can still miss high on hard-to-compress content; if
	// the first pass overshoots, one more pass scaled by the actual ratio gets close
	// enough without an unbounded retry loop.
	maxEncodeAttempts = 2
	// minVideoBitrateBps keeps a very tight target from solving for a bitrate too low to
	// be watchable.
	minVideoBitrateBps = 300_000
)

// CompressToSize re-encodes src so it fits under maxBytes, writing the result to dst.
//
// It solves for the highest video bitrate that should fit the budget rather than applying
// a fixed compression level, so quality only drops as much as the target actually
// forces. Audio is stream-copied, never re-encoded, since it is a small fraction of the
// budget and copying costs nothing. If src already fits, it is copied byte-for-byte.
func CompressToSize(src, dst string, maxBytes uint64) (CompressReport, error) {
	if err := ffmpeg.CheckNotOptionLike("input", src); err != nil {
		return CompressReport{}, err
	}
	if err := ffmpeg.CheckNotOptionLike("output", dst); err != nil {
		return CompressReport{}, err
	}
	info, err := os.Stat(src)
	if err != nil {
		return CompressReport{}, fmt.Errorf("read %s: %w", src, err)
	}
	inputBytes := uint64(info.Size())
	if inputBytes <= maxBytes {
		if err := copyFile(src, dst); err != nil {
			return CompressReport{}, err
		}
		if err := session.Restrict(dst); err != nil {
			return CompressReport{}, err
		}
		return CompressReport{InputBytes: inputBytes, OutputBytes: inputBytes, MaxBytes: maxBytes}, nil
	}

	_, _, durationS, err := ffmpeg.ProbeVideo(src)
	if err != nil {
		return CompressReport{}, err
	}
	if durationS <= 0 {
		return CompressReport{}, fmt.Errorf("%s reports zero duration", src)
	}
	audio, err := ffmpeg.HasAudio(src)
	if err != nil {
		return CompressReport{}, err
	}
	var audioBps uint64
	if audio {
		audioBps = 128_000
		if v, ok, err := ffmpeg.AudioBitrateBps(src); err != nil {
			return CompressReport{}, err
		} else if ok {
			audioBps = v
		}
	}

	videoBps := videoBitrateForBudget(maxBytes, durationS, audioBps)
	for attempt := 1; ; attempt++ {
		if err := encodeAtBitrate(src, dst, audio, videoBps); err != nil {
			return CompressReport{}, err
		}
		info, err := os.Stat(dst)
		if err != nil {
			return CompressReport{}, fmt.Errorf("read %s: %w", dst, err)
		}
		outputBytes := uint64(info.Size())
		if outputBytes <= maxBytes || attempt >= maxEncodeAttempts || videoBps <= minVideoBitrateBps {
			if err := session.Restrict(dst); err != nil {
				return CompressReport{}, err
			}
			return CompressReport{
				InputBytes: inputBytes, OutputBytes: outputBytes, MaxBytes: maxBytes,
				Reencoded: true, VideoBitrateBps: videoBps,
			}, nil
		}
		// Overshot: scale down by how far off the last attempt actually landed, with a
		// little extra margin so this converges rather than oscillating.
		ratio := float64(maxBytes) / float64(outputBytes)
		videoBps = max(uint64(float64(videoBps)*ratio*0.95), minVideoBitrateBps)
	}
}

func videoBitrateForBudget(maxBytes uint64, durationS float64, audioBps uint64) uint64 {
	targetTotal := float64(maxBytes) * 8 / durationS * sizeSafety
	return uint64(max(targetTotal-float64(audioBps), minVideoBitrateBps))
}

func encodeAtBitrate(src, dst string, audio bool, videoBps uint64) error {
	bin, err := ffmpeg.Require("ffmpeg")
	if err != nil {
		return err
	}
	args := []string{
		"-v", "error", "-nostdin", "-protocol_whitelist", "file,pipe,fd", "-y",
		"-i", src,
		"-c:v", "h264_videotoolbox", "-b:v", strconv.FormatUint(videoBps, 10),
		"-pix_fmt", "yuv420p",
	}
	if audio {
		args = append(args, "-c:a", "copy")
	} else {
		args = append(args, "-an")
	}
	cmd := exec.Command(bin, append(args, dst)...)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg compress: %w", err)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("copy %s to %s: %w", src, dst, err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("copy %s to %s: %w", src, dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("copy %s to %s: %w", src, dst, err)
	}
	return out.Close()
}
