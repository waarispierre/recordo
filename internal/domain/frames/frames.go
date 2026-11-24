// Package frames holds the per-frame timestamps written to frames.json and
// camera_frames.json.
package frames

// Record is one captured frame's timing, in mach nanoseconds. Either field may be
// missing when the capture API did not supply it.
type Record struct {
	PtsNs         *uint64 `json:"pts_ns"`
	DisplayTimeNs *uint64 `json:"display_time_ns"`
}
