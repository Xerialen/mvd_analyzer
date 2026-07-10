package democache

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mvd-analyzer/mvd-analytics/analyzer"
	"github.com/mvd-analyzer/mvd-analytics/result"
)

// TestParseSemaphore_BoundsConcurrentColdParses fires many distinct cold
// demos at once and proves the parse semaphore caps how many run in
// parallel. The per-SHA singleflight cannot help here — every demo has a
// distinct SHA — so only MaxParses bounds the storm.
func TestParseSemaphore_BoundsConcurrentColdParses(t *testing.T) {
	hub := newFakeHub()
	defer hub.Close()

	const N = 8
	ids := make([]DemoID, N)
	for i := 0; i < N; i++ {
		content := fmt.Sprintf("demo-%d-unique-bytes", i)
		sha := sha256Hex([]byte(content))
		hub.addGame(1000+i, sha, content)
		ids[i] = DemoID{Kind: "gameId", GameID: 1000 + i}
	}

	var cur, peak atomic.Int32
	parse := func(_ context.Context, _ []byte, filename string) (*result.Result, error) {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		cur.Add(-1)
		return &result.Result{SchemaVersion: result.CurrentSchemaVersion, FilePath: filename}, nil
	}

	c := New(t.TempDir(), hub.hubClient())
	c.Parse = parse
	c.MaxParses = 2

	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id DemoID) {
			defer wg.Done()
			if _, _, err := c.GetResult(context.Background(), id); err != nil {
				t.Errorf("GetResult(%v): %v", id, err)
			}
		}(id)
	}
	wg.Wait()

	if got := peak.Load(); got > 2 {
		t.Errorf("peak concurrent parses = %d; want <= MaxParses (2)", got)
	}
}

// TestParseSemaphore_ColdParseCoWaiterSurvivesWinnerCancel pins FIX 4: the
// cold loadResult runs inside the per-SHA singleflight, so its error is shared
// with every co-waiter. If the singleflight WINNER is cancelled while queued on
// the parse semaphore, its context.Canceled must NOT become the shared inflight
// error — every innocent co-waiter must still get a real result. GetResult
// strips cancellation (context.WithoutCancel) from the ctx threaded into
// loadResult; before the fix acquireParse honoured the winner's ctx and 500'd
// the whole cohort.
func TestParseSemaphore_ColdParseCoWaiterSurvivesWinnerCancel(t *testing.T) {
	hub := newFakeHub()
	defer hub.Close()

	contentA, contentC := "demo-A-bytes", "demo-C-bytes"
	shaA, shaC := sha256Hex([]byte(contentA)), sha256Hex([]byte(contentC))
	hub.addGame(1, shaA, contentA)
	hub.addGame(3, shaC, contentC)

	startedA := make(chan struct{})
	releaseA := make(chan struct{})
	parsedC := make(chan struct{})
	parse := func(_ context.Context, mvd []byte, filename string) (*result.Result, error) {
		switch {
		case strings.Contains(string(mvd), "demo-A"):
			close(startedA)
			<-releaseA
		case strings.Contains(string(mvd), "demo-C"):
			close(parsedC) // C parsed to completion despite the winner's cancel
		}
		return &result.Result{SchemaVersion: result.CurrentSchemaVersion, FilePath: filename}, nil
	}

	c := New(t.TempDir(), hub.hubClient())
	c.Parse = parse
	c.MaxParses = 1 // one slot; A holds it, C's winner must queue

	aDone := make(chan struct{})
	go func() {
		defer close(aDone)
		_, _, _ = c.GetResult(context.Background(), DemoID{Kind: "gameId", GameID: 1})
	}()
	<-startedA // A now occupies the only parse slot

	type out struct {
		res *result.Result
		err error
	}
	// Winner: requests demo C with a cancellable ctx; blocks on the semaphore
	// and registers the inflight entry.
	winnerCtx, cancelWinner := context.WithCancel(context.Background())
	winnerCh := make(chan out, 1)
	go func() {
		res, _, err := c.GetResult(winnerCtx, DemoID{Kind: "gameId", GameID: 3})
		winnerCh <- out{res, err}
	}()
	time.Sleep(30 * time.Millisecond) // let the winner register inflight + queue

	// Co-waiter: same demo C, its own live ctx; joins the singleflight.
	waiterCh := make(chan out, 1)
	go func() {
		res, _, err := c.GetResult(context.Background(), DemoID{Kind: "gameId", GameID: 3})
		waiterCh <- out{res, err}
	}()
	time.Sleep(30 * time.Millisecond) // let the co-waiter block on inflight.done

	cancelWinner()  // OLD bug: poisons the shared inflight error
	close(releaseA) // free the slot so C's cold parse can proceed

	for _, tc := range []struct {
		name string
		ch   chan out
	}{{"winner", winnerCh}, {"co-waiter", waiterCh}} {
		select {
		case o := <-tc.ch:
			if o.err != nil {
				t.Errorf("%s: err = %v; a cancelled winner must not poison the shared cold parse", tc.name, o.err)
			}
			if o.res == nil {
				t.Errorf("%s: nil result", tc.name)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: GetResult did not return", tc.name)
		}
	}

	<-parsedC // the cold parse actually ran (ignored the winner's cancel)
	<-aDone   // let A finish its parse+tier-2 write before TempDir cleanup runs
}

// TestParseSemaphore_BoundsConcurrentLOSRaycasts proves the on-demand LOS
// raycast in EnsureLOS goes through the same parse-semaphore acquire as the
// cold parse: N concurrent /los for N distinct cold demos never run more than
// MaxParses raycasts in parallel. The injectable BuildLOS hook stands in for
// the real (BSP-backed) raycast so the test needs no map data.
func TestParseSemaphore_BoundsConcurrentLOSRaycasts(t *testing.T) {
	hub := newFakeHub()
	defer hub.Close()

	const N = 8
	ids := make([]DemoID, N)
	for i := 0; i < N; i++ {
		content := fmt.Sprintf("los-demo-%d-bytes", i)
		sha := sha256Hex([]byte(content))
		hub.addGame(2000+i, sha, content)
		ids[i] = DemoID{Kind: "gameId", GameID: 2000 + i}
	}

	c := New(t.TempDir(), hub.hubClient())
	c.Parse = streamsWithPlayers // Result with a Streams block, no BSP
	c.MaxParses = 2

	var cur, peak atomic.Int32
	c.BuildLOS = func(art *analyzer.LazyArtifact, res *result.Result) error {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		cur.Add(-1)
		return art.Build(res, analyzer.MaterializeDeps{})
	}

	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id DemoID) {
			defer wg.Done()
			if _, _, err := c.EnsureLOS(context.Background(), id); err != nil {
				t.Errorf("EnsureLOS(%v): %v", id, err)
			}
		}(id)
	}
	wg.Wait()

	if got := peak.Load(); got > 2 {
		t.Errorf("peak concurrent LOS raycasts = %d; want <= MaxParses (2)", got)
	}
}

// TestParseSemaphore_LOSRespectsCtxCancellationWhileQueued proves a /los
// caller waiting for a raycast slot returns with the ctx error and never
// runs Build, when cancelled while queued.
func TestParseSemaphore_LOSRespectsCtxCancellationWhileQueued(t *testing.T) {
	hub := newFakeHub()
	defer hub.Close()

	contentA, contentB := "los-A-bytes", "los-B-bytes"
	shaA, shaB := sha256Hex([]byte(contentA)), sha256Hex([]byte(contentB))
	hub.addGame(1, shaA, contentA)
	hub.addGame(2, shaB, contentB)

	c := New(t.TempDir(), hub.hubClient())
	c.Parse = streamsWithPlayers
	c.MaxParses = 1 // one slot; A's raycast holds it, B must queue

	// Warm both base parses first. The cold parse ignores caller cancellation by
	// design (FIX 4), so if B still had to parse it would block on the single
	// slot (held by A's raycast) and never observe the cancel — only the raycast
	// wait is cancellable, and that is what this test exercises.
	for _, gid := range []int{1, 2} {
		if _, _, err := c.GetResult(context.Background(), DemoID{Kind: "gameId", GameID: gid}); err != nil {
			t.Fatalf("warm parse gameId %d: %v", gid, err)
		}
	}

	started := make(chan struct{})
	releaseA := make(chan struct{})
	var bBuilds atomic.Int32
	c.BuildLOS = func(art *analyzer.LazyArtifact, res *result.Result) error {
		switch res.FilePath {
		case shaA + ".mvd.gz":
			close(started)
			<-releaseA
		case shaB + ".mvd.gz":
			bBuilds.Add(1) // must never run: B is cancelled while queued
		}
		return art.Build(res, analyzer.MaterializeDeps{})
	}

	aDone := make(chan struct{})
	go func() {
		defer close(aDone)
		_, _, _ = c.EnsureLOS(context.Background(), DemoID{Kind: "gameId", GameID: 1})
	}()
	<-started // A now occupies the only raycast slot

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, _, err := c.EnsureLOS(ctx, DemoID{Kind: "gameId", GameID: 2})
		errCh <- err
	}()

	time.Sleep(30 * time.Millisecond) // let B reach the semaphore wait
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("cancelled EnsureLOS err = %v; want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled EnsureLOS did not return; ctx not honoured while queued")
	}
	if got := bBuilds.Load(); got != 0 {
		t.Errorf("B raycast ran %d times; a cancelled queued request must not acquire the slot", got)
	}
	close(releaseA)
	<-aDone // let A finish its Build+tier3Store write before TempDir cleanup runs
}
