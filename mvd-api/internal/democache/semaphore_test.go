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

// TestParseSemaphore_RespectsCtxCancellationWhileQueued proves a caller
// waiting for a parse slot returns promptly with the ctx error when
// cancelled, rather than blocking until the slot frees.
func TestParseSemaphore_RespectsCtxCancellationWhileQueued(t *testing.T) {
	hub := newFakeHub()
	defer hub.Close()

	contentA, contentB := "demo-A-bytes", "demo-B-bytes"
	shaA, shaB := sha256Hex([]byte(contentA)), sha256Hex([]byte(contentB))
	hub.addGame(1, shaA, contentA)
	hub.addGame(2, shaB, contentB)

	started := make(chan struct{})
	releaseA := make(chan struct{})
	var bParses atomic.Int32
	parse := func(_ context.Context, mvd []byte, filename string) (*result.Result, error) {
		if strings.Contains(string(mvd), "demo-A") {
			close(started)
			<-releaseA
		} else {
			bParses.Add(1) // must never run: B is cancelled while queued
		}
		return &result.Result{SchemaVersion: result.CurrentSchemaVersion, FilePath: filename}, nil
	}

	c := New(t.TempDir(), hub.hubClient())
	c.Parse = parse
	c.MaxParses = 1 // one slot; A holds it, B must queue

	go func() { _, _, _ = c.GetResult(context.Background(), DemoID{Kind: "gameId", GameID: 1}) }()
	<-started // A now occupies the only parse slot

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, _, err := c.GetResult(ctx, DemoID{Kind: "gameId", GameID: 2})
		errCh <- err
	}()

	time.Sleep(30 * time.Millisecond) // let B reach the semaphore wait
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("cancelled GetResult err = %v; want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled GetResult did not return; ctx not honoured while queued")
	}

	if got := bParses.Load(); got != 0 {
		t.Errorf("B parsed %d times; a cancelled queued request must not acquire the slot", got)
	}
	close(releaseA)
}
