package analyzer

// CoreOutputs is the typed bundle of state-reconstruction results
// that derived analysers consume during their Finalize. It replaces
// the previous mechanism — shared mutable Context fields like
// ctx.DemoInfo and ctx.FragEntries written by one analyser's Finalize
// and read by the next, with no compile-time guarantee that the
// writer ran first.
//
// The registry builds this struct incrementally as core analysers
// finalize, then calls UseCoreOutputs on every analyser that
// implements CoreConsumer just before its own Finalize runs. Two-phase
// in spirit (core finishes its writes, derived starts its reads), but
// the registration order still drives the actual sequencing — there
// is no separate "phase 1 / phase 2" loop today.
//
// Adding a field here is the right place when an analyser's Finalize
// would otherwise need to peek into another analyser's intermediate
// state.
type CoreOutputs struct {
	// DemoInfo is the parsed KTX demoinfo JSON, populated from the
	// demoinfo analyser's Finalize. Nil when the demo has no demoinfo
	// hidden message (older demos, non-KTX servers).
	DemoInfo *DemoInfoResult

	// Names resolves a display-name string back to its demoinfo team.
	// Built once from DemoInfo so callers don't each rebuild their own
	// nameToTeam map. Nil-safe: TeamForName returns "" when the table
	// itself is nil.
	Names *NameTable

	// FragEntries is the canonical frag-event log emitted by the frag
	// analyser. Used by timeline (streaks, powerup-frag counts) and
	// weapon_pickups (kill attribution). Nil when the demo had no
	// obituaries or the frag analyser was not registered.
	FragEntries []FragEntry
}

// CoreConsumer is the optional interface for analysers that need
// access to CoreOutputs before their Finalize runs. The registry
// checks for this interface and invokes UseCoreOutputs in registration
// order, so an implementer is guaranteed to see every core output
// produced by an analyser registered earlier than itself.
type CoreConsumer interface {
	UseCoreOutputs(co *CoreOutputs)
}
