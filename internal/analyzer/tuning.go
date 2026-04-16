package analyzer

// Tuning constants for the timeline analyzer.
//
// Sampling cadence: the high-res bucket is the granularity used for all
// timeline data — map playback, graphs, region control. Defaults to 50 ms
// (20 Hz), which captures every stat update (~3 Hz) and most position
// updates (~73 Hz). Can be overridden via TimelineAnalyzer.SetBucketDuration
// for higher fidelity (e.g., 1/77 ≈ 13 ms for full server frame rate).
const (
	// DefaultHighResBucketDuration is the sampling interval (50 ms ≈ 20 Hz).
	DefaultHighResBucketDuration = 0.05

	// timelineBucketPrealloc reserves capacity in the bucket slice for a
	// 20-minute match at the default 50 ms cadence (20 * 60 / 0.05 = 24000).
	// Just an allocation hint — the slice grows naturally beyond this.
	timelineBucketPrealloc = 24000
)
