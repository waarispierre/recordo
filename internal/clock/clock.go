// Package clock converts mach absolute time to nanoseconds.
//
// Both ScreenCaptureKit frame display times and CGEvent timestamps are expressed in mach
// absolute time, which is what makes correlating them possible. Everything downstream
// works in nanoseconds on this one shared timebase.
package clock

/*
#include <mach/mach_time.h>
*/
import "C"

import (
	"math/bits"
	"sync"
)

var timebase = sync.OnceValue(func() C.mach_timebase_info_data_t {
	var info C.mach_timebase_info_data_t
	C.mach_timebase_info(&info)
	return info
})

// MachToNanos converts a mach absolute timestamp to nanoseconds.
func MachToNanos(mach uint64) uint64 {
	tb := timebase()
	return scale(mach, uint64(tb.numer), uint64(tb.denom))
}

// scale computes t*numer/denom without overflow. On Apple Silicon numer/denom is 125/3,
// so a plain 64-bit multiply overflows a few minutes after boot.
func scale(t, numer, denom uint64) uint64 {
	hi, lo := bits.Mul64(t, numer)
	// hi%denom keeps Div64 from panicking; it only differs from hi once the result no
	// longer fits in 64 bits, which is centuries of uptime away.
	q, _ := bits.Div64(hi%denom, lo, denom)
	return q
}

// NowNanos is the current mach absolute time in nanoseconds.
func NowNanos() uint64 {
	return MachToNanos(uint64(C.mach_absolute_time()))
}
