# Writing an analyzer

A walkthrough for adding an analytics node to the pipeline — from "I want
to compute X" to a scheduled, cached, served, documented artifact. Read
[`README.md`](README.md) first for the architecture picture; this document
is the hands-on path. The worked example is a small **kill-pace** analyzer
(kills per minute over the match, per player).

The pipeline is a declared dependency DAG (see the diagram in
[`README.md`](README.md) and the catalog in [`ARTIFACTS.md`](ARTIFACTS.md)).
Adding an analyzer means: write the collector/Finalize, **declare the
node's inputs and outputs in `analyzer/dag.go`**, and update the generated
docs. Order of registration does not matter — the engine schedules from
your declared edges, and the test suite proves output is identical under
any valid order.

## 1. Decide your inputs

Two kinds of input exist, and the choice shapes everything:

- **Raw events** (you implement `OnEvent`): you see every parsed wire
  event during the single shared streaming pass. Use this when your
  signal lives in the wire data itself (prints, sounds, entity state,
  stats). The event vocabulary is `mvd-reader/events` — see
  [`../mvd-reader/README.md`](../mvd-reader/README.md).
- **Artifacts** (other nodes' outputs, read at Finalize): use these
  whenever another node already computed what you need. The catalog of
  artifacts is [`ARTIFACTS.md`](ARTIFACTS.md) — generated, always
  current.

Artifacts arrive two ways at Finalize time:

**(a) `CoreOutputs` fields** — typed state published by producer nodes
(today all in the core tier, but any analyzer tier can publish — see §2),
handed to you via the `CoreConsumer` hook. The field → producing node map:

| `co.` field | Produced by node | What it is |
|---|---|---|
| `DemoInfo` | `demoinfo` | Parsed KTX end-of-match scoreboard (nil on non-KTX demos) |
| `Names` | `demoinfo` | Display-name → demoinfo-team table (nil-safe) |
| `Slots` | `demoinfo` | Per-slot final occupant (prefer `SlotIdentityAt` when you have a timestamp) |
| `Sessions` | `identity` | Per-slot time-sorted identity sessions (reconnect-unified) |
| `FragEntries` | `frag` | The canonical **raw** kill log (pre-telefrag-recovery — see `frags:final` below) |
| `VictimNamedTeamkills` | `frag` | Teamkill obituaries with no named killer (input to `frags-final`) |
| `Clock` | `clock` | The match time base — call `co.Clock.ToMatch(t)` (or `co.MatchStartMs()`) so your timestamps are **born match-relative**; nil-safe |
| `Roster` | `roster` | Final team labels — call `co.TeamFor(name, rawTeam)` so duel demos get name-as-team labels **at birth**; nil-safe |

The nil-safe helpers (`co.MatchStartMs()`, `co.TeamFor(...)`,
`co.Clock.ToMatch(...)`) tolerate a missing producer, so unit tests can
build a bare `CoreOutputs` without wiring the whole core tier.

**(b) `result.*` sections** — a post-tier node reads sections earlier
nodes wrote (e.g. `aim` reads `res.Shots` + `res.Streams` + `res.Damage`).
`ARTIFACTS.md`'s `resultKey` column maps artifact → JSON section.

**The `:final` rule.** Two artifacts are refined after birth: the frag
log (`frags-final` appends recovered telefrag teamkills) and the match
scoreboard (`match-final` fills corrected kills/deaths/suicides). If you
consume either, decide which version you mean and declare it — require
`"frag"` for the raw obituary log (what `timeline` deliberately uses) or
`"frags:final"` for the recovered one; same for `"match"` vs
`"match:final"`.

Every timestamp you emit must be match-relative (via `co.Clock`) and every
team label final (via `co.TeamFor`) — there is no post-hoc rewrite pass to
fix them anymore.

## 2. Write the analyzer

Implement `analyzer.Analyzer`. For kill-pace we need `FragEntries` +
`Clock`, so we also implement `CoreConsumer`:

```go
package analyzer

import "github.com/mvd-analyzer/mvd-reader/events"

type KillPaceAnalyzer struct {
	core *CoreOutputs
}

func NewKillPaceAnalyzer() *KillPaceAnalyzer { return &KillPaceAnalyzer{} }

func (a *KillPaceAnalyzer) Name() string { return "killpace" }

func (a *KillPaceAnalyzer) Init(ctx *Context) error { return nil }

// No OnEvent work: everything we need is already an artifact.
func (a *KillPaceAnalyzer) OnEvent(events.Event) error { return nil }

// UseCoreOutputs runs immediately before Finalize (CoreConsumer hook).
func (a *KillPaceAnalyzer) UseCoreOutputs(co *CoreOutputs) { a.core = co }

func (a *KillPaceAnalyzer) Finalize(res *Result) error {
	if a.core == nil || len(a.core.FragEntries) == 0 {
		return nil // section stays absent — omitempty, not an error
	}
	end := a.core.Clock.MatchEndMs // match-relative window
	_ = end
	// ... count kills per player per minute into a result.KillPaceResult,
	// stamping any timestamps via a.core.Clock.ToMatch(...) ...
	// res.KillPace = out
	return nil
}
```

Conventions that matter:

- **Absent ≠ error.** A demo without your signal leaves your section nil
  (`omitempty`); return an error only for real failures (it lands in
  `result.Errors`, sorted deterministically, and the run continues).
- If your node *produces* state that other nodes should consume, there
  are two channels, and both work from **any** analyzer tier — the
  engine applies the hooks identically to core and derived nodes, and
  ordering comes from declared edges, not from the tier:
  1. **A typed `CoreOutputs` field** + `CoreProducer` (`PopulateCore`
     runs right after your Finalize; consumers finalized later see it).
     Add the field to the table above. The core tier is the *convention*
     for canonical state-reconstruction everyone consumes
     (demoinfo/identity/frag/clock/roster); a derived producer is
     legitimate when the audience is narrower.
  2. **Your `result.*` section**, read by later nodes — the existing
     derived→post pattern (`aim` reads `res.Shots`/`res.Streams`).
  Either way the contract is the same: every consumer must declare
  `requires: ["your-node"]` — that edge, not tier or registration order,
  is what guarantees you ran first (and the shuffle test will catch a
  consumer that forgets).
- Determinism: iterate maps via sorted keys before emitting anything —
  the golden corpus pins your output byte-for-byte.

## 3. Declare the node in `analyzer/dag.go`

**This step is mandatory** — `NewDefaultRegistry` panics at startup for a
registered analyzer with no node metadata (the panic names your analyzer).
Add one entry, keyed by your `Name()`:

```go
// in analyzerNodeMeta:
"killpace": {name: "kill-pace", tier: "derived",
	requires: []string{"clock", "frag"}},
```

- `name` is the kebab-case DAG/artifact name (it becomes the catalog row,
  the graph node, and — if servable — the REST/MCP name).
- `requires` lists every artifact you read: each `co.*` field's producer
  (from the table in §1) and each `result.*` section's node. Use the
  `:final` names when that's what you consume.
- `provides` is only for *extra* names beyond your own (rare — the
  barrier/`:final` pattern).
- Then register it — **anywhere** in `NewDefaultRegistry`; the list is
  inventory, not ordering:

```go
r.RegisterDerived(NewKillPaceAnalyzer())
```

What the machinery now checks for you:

- **Startup validation**: a typo'd `requires` panics with the artifact
  and node name; two providers of one artifact likewise.
- **`TestOrderIndependence`**: runs the corpus under shuffled valid
  orders and asserts byte-identical output. If you *forgot* to declare
  something you read, a shuffle can schedule you before its producer and
  this test fails — that failure means "add the missing edge", never
  "pin the order".
- **`TestDAGNodeInventory` / manifest / catalog drift tests**: fail until
  you update the expected node list and regenerate `ARTIFACTS.md`.

Give your node a one-line `description` in the meta (it becomes the
catalog and manifest text) and, if it writes a Result section, the
`resultKey` so the generic endpoint can serve it.

## 4. Eager or lazy?

Default is **eager**: your node runs in the single shared pass/finalize
and its section ships in every Result. Register a **lazy** artifact
instead only when the compute is genuinely heavy *derived* work with no
default consumer — the bar is `los` (~2.5 s of raycasts). Lazy nodes
implement the `LazyArtifact` hooks in `analyzer/materialize.go`
(build + tier-3 gob encode/decode + latch) and inherit on-demand
materialisation, per-SHA locking, disk persistence across restarts, the
generic REST endpoint, and the MCP tool. What laziness is *not* for:
skipping event-tier work — every collector rides the one parse, and
"lazy" parse-level data means re-parsing (we deleted exactly that
machinery in phase 12; don't reintroduce it).

## 5. The checklist before you commit

1. **Result type** in `result/` with JSON tags; `omitempty` on the new
   `Result` field; times are int32 match-relative ms (see the coord.go
   add-a-column checklist if you touch streams).
2. **Schema bump**: a new Result section is a consumer-visible change —
   bump `CurrentSchemaVersion` in `result/result.go` with a changelog
   comment, add the `RESULT_SCHEMA.md` section + version-history row,
   and a `RELEASE_NOTES.md` entry.
3. **`make artifacts-md`** — regenerate the catalog (a drift test fails
   otherwise). If the graph changed shape, re-embed the README mermaid
   (`qw-analyze -graph mermaid`; its drift test will remind you).
4. **Tests**: unit tests beside the code; then `make test` — the golden
   corpus will show your new section (regenerate goldens **only** for
   this intended change, per CLAUDE.md, and commit them with the code).
5. **Docs lock-step** (CLAUDE.md rule): a one-page `analyzer/<name>.md`
   if the analyzer has non-obvious semantics; README analyzer table row.
6. `gofmt` / `go vet` clean; build all modules from a clean worktree.

## Serving surface you get for free

A servable node (has a `resultKey`, or lazy) is automatically available
at `GET /v1/demos/{id}/artifacts/<name>`, listed in `GET /v1/artifacts`
and `/v1/graph`, reachable via the mvd-mcp `getArtifact` tool, and shown
in `qw-analyze -graph`. Curated endpoints with filters are a separate,
deliberate addition (see `mvd-api/API.md`) — add one only when the
generic accessor's ergonomics aren't enough.
