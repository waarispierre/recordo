//! Mach absolute time -> nanoseconds.
//!
//! Both ScreenCaptureKit frame display times and CGEvent timestamps are expressed in
//! mach absolute time, which is what makes correlating them possible. Everything
//! downstream works in nanoseconds on this one shared timebase.

use mach2::mach_time::{mach_absolute_time, mach_timebase_info, mach_timebase_info_data_t};
use std::sync::OnceLock;

fn timebase() -> &'static mach_timebase_info_data_t {
    static TB: OnceLock<mach_timebase_info_data_t> = OnceLock::new();
    TB.get_or_init(|| {
        let mut info = mach_timebase_info_data_t { numer: 0, denom: 0 };
        unsafe { mach_timebase_info(&mut info) };
        info
    })
}

pub fn mach_to_nanos(mach_time: u64) -> u64 {
    let tb = timebase();
    // Widen before multiplying: on Apple Silicon numer/denom is 125/3, so a raw u64
    // multiply overflows a few minutes after boot.
    ((mach_time as u128 * tb.numer as u128) / tb.denom as u128) as u64
}

pub fn now_nanos() -> u64 {
    mach_to_nanos(unsafe { mach_absolute_time() })
}
