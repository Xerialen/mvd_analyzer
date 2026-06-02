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

	// VictimNamedTeamkills are teamkill obituaries that name only the
	// victim ("X was telefragged by his teammate"). The killer is the
	// generic "teammate", so they never enter FragEntries; the
	// recoverTelefragTeamkills post-processor recovers the killer from
	// position co-location + the teamkiller's -1 frag-delta.
	VictimNamedTeamkills []FragEntry

	// Slots is the per-slot resolved player view: Name is the demoinfo
	// display name when the slot matches a demoinfo entry (via login or
	// name join), otherwise the userinfo name from ctx.Players[slot].
	// Team is the userinfo team (the demoinfo team override only kicks
	// in via NameTable lookups).
	//
	// Slots replaces the previous mid-Finalize patch in registry.go that
	// rewrote ctx.Players[slot].Name in place — the patch was the worst
	// instance of cross-analyser shared mutable state in the audit. Now
	// every Finalize site that wants the display name reads
	// co.Slots[slot].Name instead, and ctx.Players keeps its on-the-wire
	// userinfo values untouched.
	//
	// Slots maps one slot → one *final* occupant, which is wrong when a
	// player reconnects onto another slot and their old slot is reused
	// (or stamped with a late userinfo name). Finalize sites that have an
	// event timestamp should prefer SlotIdentityAt(slot, tMs) instead;
	// Slots remains for the few callers with no time to key on.
	Slots map[int]SlotInfo

	// Sessions is the per-slot, time-sorted, identity-resolved occupancy
	// list produced by the identity analyser. Each ResolvedSession covers
	// a half-open [StartMs, EndMs) window of a wire slot and carries the
	// canonical identity (cross-reconnect-unified) that owned the slot
	// during it. Nil when the identity analyser was not registered.
	Sessions map[int][]ResolvedSession
}

// ResolvedSession is one contiguous occupancy of a wire slot, resolved
// to the canonical (reconnect-unified) player identity. IdentityKey is
// stable within a single analysis run and equal for every session the
// same human played, so stream merging can group on it.
type ResolvedSession struct {
	StartMs     int32
	EndMs       int32
	Name        string
	Team        string
	IdentityKey string
}

// SlotInfo holds the per-slot resolved player name and team that
// downstream Finalize sites read. See CoreOutputs.Slots for the
// resolution rules.
type SlotInfo struct {
	Name string
	Team string
}

// SlotName returns the resolved display name for slot. Equivalent to
// co.Slots[slot].Name with nil-safety on co; returns "" when the slot
// has no recorded entry.
func (co *CoreOutputs) SlotName(slot int) string {
	if co == nil {
		return ""
	}
	return co.Slots[slot].Name
}

// SlotIdentityAt returns the canonical identity that owned slot at the
// given time (integer ms). It consults the per-slot session table so
// events that happened before a reconnect/slot-reuse resolve to the
// player who was actually there, not the slot's final occupant. Falls
// back to the final-occupant Slots entry when no session covers tMs
// (e.g. the identity analyser was not registered, or an out-of-range
// timestamp).
func (co *CoreOutputs) SlotIdentityAt(slot int, tMs int32) SlotInfo {
	if co == nil {
		return SlotInfo{}
	}
	for i := range co.Sessions[slot] {
		s := co.Sessions[slot][i]
		if tMs >= s.StartMs && tMs < s.EndMs {
			return SlotInfo{Name: s.Name, Team: s.Team}
		}
	}
	return co.Slots[slot]
}

// CoreConsumer is the optional interface for analysers that need
// access to CoreOutputs before their Finalize runs. The registry
// checks for this interface and invokes UseCoreOutputs in registration
// order, so an implementer is guaranteed to see every core output
// produced by an analyser registered earlier than itself.
type CoreConsumer interface {
	UseCoreOutputs(co *CoreOutputs)
}

// CoreProducer is the optional interface for analysers that contribute
// fields to CoreOutputs after their own Finalize runs. The registry
// invokes PopulateCore on every implementer immediately after the
// analyser's Finalize, so any analyser registered later in the slice
// (Core or Derived) sees the produced fields.
type CoreProducer interface {
	PopulateCore(co *CoreOutputs)
}
