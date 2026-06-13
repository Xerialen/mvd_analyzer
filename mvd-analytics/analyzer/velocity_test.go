package analyzer

import "testing"

// Constant-velocity motion: 10 units every 100 ms = 100 units/sec. The
// central difference (and the one-sided differences at the ends) must all
// recover 100 on the x axis and 0 on y/z.
func TestResolveVelocities_ConstantMotion(t *testing.T) {
	a := NewTimelineAnalyzer()
	st := &timelinePlayerState{}
	for i := 0; i < 4; i++ {
		st.streams.recordPosition(int32(i*100), float32(i*10), 0, 0, 0, 0)
	}
	a.playerState[0] = st

	a.resolveVelocities()

	b := &st.streams
	if len(b.posVX) != 4 || len(b.posVY) != 4 || len(b.posVZ) != 4 {
		t.Fatalf("velocity columns misaligned: vx=%d vy=%d vz=%d (want 4)", len(b.posVX), len(b.posVY), len(b.posVZ))
	}
	for i := range b.posVX {
		if b.posVX[i] != 100 {
			t.Errorf("vx[%d] = %d, want 100 units/sec", i, b.posVX[i])
		}
		if b.posVY[i] != 0 || b.posVZ[i] != 0 {
			t.Errorf("vy/vz[%d] = %d/%d, want 0", i, b.posVY[i], b.posVZ[i])
		}
	}
}

// A respawn teleport between two samples must NOT be differentiated
// across — the velocity reads the real ±100 ups motion on each side, not
// the ~9900 ups spike the 990-unit teleport would fabricate.
func TestResolveVelocities_NoSpikeAcrossRespawn(t *testing.T) {
	a := NewTimelineAnalyzer()
	st := &timelinePlayerState{}
	st.streams.recordPosition(0, 0, 0, 0, 0, 0)
	st.streams.recordPosition(100, 10, 0, 0, 0, 0)
	st.streams.recordPosition(200, 1000, 0, 0, 0, 0) // respawned far away
	st.streams.recordPosition(300, 1010, 0, 0, 0, 0)
	st.streams.recordSpawn(150) // respawn between sample 1 and 2
	a.playerState[0] = st

	a.resolveVelocities()

	b := &st.streams
	for i, got := range b.posVX {
		if got != 100 {
			t.Errorf("vx[%d] = %d, want 100 (no teleport spike)", i, got)
		}
	}
}

// An abnormal time gap (death / pause / disconnect) must break the
// segment: the isolated tail sample reads 0 rather than averaging the
// motion across the gap.
func TestResolveVelocities_BreaksOnLargeGap(t *testing.T) {
	a := NewTimelineAnalyzer()
	st := &timelinePlayerState{}
	st.streams.recordPosition(0, 0, 0, 0, 0, 0)
	st.streams.recordPosition(13, 5, 0, 0, 0, 0)
	st.streams.recordPosition(5000, 500, 0, 0, 0, 0) // 5 s gap → not connected
	a.playerState[0] = st

	a.resolveVelocities()

	b := &st.streams
	// Sample 2 has no usable neighbour (prev edge spans the gap) → 0.
	if b.posVX[2] != 0 {
		t.Errorf("post-gap isolated sample vx = %d, want 0", b.posVX[2])
	}
	// Samples 0,1 are connected (13 ms apart): 5 units / 13 ms ≈ 385 ups.
	if b.posVX[0] == 0 || b.posVX[1] == 0 {
		t.Errorf("pre-gap samples should have non-zero vx: %v", b.posVX[:2])
	}
}

// A map teleporter relocates the player at normal sample cadence with no
// respawn — the displacement guard must catch it so the velocity reads
// the real ~385 ups motion on each side, not a ~150000 ups spike.
func TestResolveVelocities_NoSpikeAcrossTeleport(t *testing.T) {
	a := NewTimelineAnalyzer()
	st := &timelinePlayerState{}
	st.streams.recordPosition(0, 0, 0, 0, 0, 0)
	st.streams.recordPosition(13, 5, 0, 0, 0, 0)
	st.streams.recordPosition(26, 2000, 0, 0, 0, 0) // teleported (no spawn, normal cadence)
	st.streams.recordPosition(39, 2005, 0, 0, 0, 0)
	a.playerState[0] = st

	a.resolveVelocities()

	for i, got := range st.streams.posVX {
		if got > velTeleportSpeedUps || got < -velTeleportSpeedUps {
			t.Errorf("vx[%d] = %d implies a teleport spike past the cap", i, got)
		}
	}
}
