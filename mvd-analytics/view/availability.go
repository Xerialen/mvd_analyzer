package view

import "github.com/mvd-analyzer/mvd-analytics/result"

// This file adds the object-shaped "section accessor" half of the R3
// availability rule (see sections.go for ErrUnavailable and the
// list-vs-object convention). Each accessor returns the section, or
// ErrUnavailable when the demo lacks the enabling signal, so every
// consumer funnels through one availability predicate instead of
// hand-rolling a nil-field check — HTTP handlers map ErrUnavailable to a
// 422 via writeUnavailable, and in-process callers (WASM/CLI) get the
// same gate for free (mvd-api F11).

// Metadata returns the demo's server-cvar + KTX match-settings block, or
// ErrUnavailable when the demo carried no fullserverinfo / countdown
// centerprint.
func Metadata(r *result.Result) (*result.MetadataResult, error) {
	if r.Metadata == nil {
		return nil, ErrUnavailable
	}
	return r.Metadata, nil
}

// DemoInfo returns the KTX demoinfo blob, or ErrUnavailable on a non-KTX
// or pre-match-abort demo that carried none.
func DemoInfo(r *result.Result) (*result.DemoInfoResult, error) {
	if r.DemoInfo == nil {
		return nil, ErrUnavailable
	}
	return r.DemoInfo, nil
}

// LocGraph returns the per-map loc adjacency graph, or ErrUnavailable
// when no position track was emitted.
func LocGraph(r *result.Result) (*result.LocGraphResult, error) {
	if r.LocGraph == nil {
		return nil, ErrUnavailable
	}
	return r.LocGraph, nil
}

// Shots returns the per-fire weapon-fire stream, or ErrUnavailable when
// no weapon fires were decoded.
func Shots(r *result.Result) (*result.ShotsResult, error) {
	if r.Shots == nil {
		return nil, ErrUnavailable
	}
	return r.Shots, nil
}

// Aim returns the per-player aim analysis, or ErrUnavailable when the
// demo has no shots + position/view streams to derive it from.
func Aim(r *result.Result) (*result.AimResult, error) {
	if r.Aim == nil {
		return nil, ErrUnavailable
	}
	return r.Aim, nil
}

// Airgibs returns the Key Moments airgib list. Availability tracks the
// timeline-analysis pass, not the map's clip hull: ErrUnavailable only
// when there is no TimelineAnalysis at all. A present-but-BSP-less demo
// returns an empty (non-nil) list — an airgib-less map is a 200 [], not
// a 422.
func Airgibs(r *result.Result) ([]result.AirgibEvent, error) {
	if r.TimelineAnalysis == nil {
		return nil, ErrUnavailable
	}
	if r.TimelineAnalysis.Airgibs == nil {
		return []result.AirgibEvent{}, nil
	}
	return r.TimelineAnalysis.Airgibs, nil
}

// RegionControlAvailable reports ErrUnavailable when the demo has no
// region-control layout (no TimelineAnalysis, or a nil RegionControl on
// it). This is the gate the /region-control endpoint checks before
// computing a windowed view via RegionControl(opts); the compute itself
// is always safe once the layout is present.
func RegionControlAvailable(r *result.Result) error {
	if r.TimelineAnalysis == nil || r.TimelineAnalysis.RegionControl == nil {
		return ErrUnavailable
	}
	return nil
}
