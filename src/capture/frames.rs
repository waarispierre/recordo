//! Per-frame timestamp log.
//!
//! `display_time` comes from SCStreamFrameInfo and is mach absolute time — the same
//! timebase as CGEvent timestamps, which is what makes cursor correlation possible.
//! Frame *index* is deliberately not used for correlation: the capture queue is serial
//! and drops frames under load, so indices drift from wall clock.

use screencapturekit::cm::CMSampleBufferSCExt;
use screencapturekit::prelude::*;
use serde::{Deserialize, Serialize};
use std::sync::Mutex;

#[derive(Debug, Clone, Copy, Serialize, Deserialize)]
pub struct FrameRecord {
    pub pts_ns: Option<u64>,
    pub display_time_ns: Option<u64>,
}

#[derive(Default)]
pub struct FrameLog {
    records: Mutex<Vec<FrameRecord>>,
}

impl FrameLog {
    pub fn push(&self, record: FrameRecord) {
        if let Ok(mut guard) = self.records.lock() {
            guard.push(record);
        }
    }

    pub fn snapshot(&self) -> Vec<FrameRecord> {
        self.records.lock().map(|g| g.clone()).unwrap_or_default()
    }
}

pub struct FrameLogHandler {
    log: std::sync::Arc<FrameLog>,
}

impl FrameLogHandler {
    pub fn new(log: std::sync::Arc<FrameLog>) -> Self {
        Self { log }
    }
}

impl SCStreamOutputTrait for FrameLogHandler {
    fn did_output_sample_buffer(&self, sample: CMSampleBuffer, of_type: SCStreamOutputType) {
        if of_type != SCStreamOutputType::Screen {
            return;
        }
        let pts = sample.presentation_timestamp();
        let pts_ns = pts.as_seconds().map(|s| (s * 1e9) as u64);
        let display_time_ns = sample
            .frame_info()
            .and_then(|info| info.display_time)
            .map(crate::capture::clock::mach_to_nanos);

        self.log.push(FrameRecord {
            pts_ns,
            display_time_ns,
        });
    }
}
