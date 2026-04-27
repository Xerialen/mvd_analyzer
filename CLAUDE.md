# Instructions for AI assistants working on this repository

This file is the single, authoritative onboarding document for any LLM-based
assistant (Claude Code, coding agents embedded in IDEs, etc.) collaborating
on mvd-analyzer. Read it before doing non-trivial work.

## Keep documentation in lock-step with code

The repo ships **user-facing docs that have to stay truthful**. Any PR that
changes observable behaviour, schema, or file layout should update the
relevant docs in the same commit (or the next one — whatever the human
asks for). Do not leave "I'll document it later" items behind.

The docs that matter, in priority order:

| Doc | Scope |
|---|---|
| [`README.md`](README.md) | Top-level: architecture, event list, result schema shape, repo layout, known limitations |
| [`qwdemo/README.md`](qwdemo/README.md) | Layer 1: event table + derivation notes, Source implementation guide |
| [`qwanalytics/README.md`](qwanalytics/README.md) | Layer 2: registered analyzers, Result schema, how to add an analyzer, MH / items semantics |
| [`qw-web/README.md`](qw-web/README.md) | Layer 3: build targets, dist/ layout, map-tab overlay behaviour, loc corpus fetch |
| [`qwdemo/MVD_FORMAT.md`](qwdemo/MVD_FORMAT.md) | MVD binary format reference — every svc_* we decode, entity-state item tracking, derived events, ezquake/mvdsv line refs |

**When you add a new feature or event type**, update:
1. The relevant layer's README (event table, result schema, or UI docs).
2. `qwdemo/MVD_FORMAT.md` if the feature touches the wire protocol, entity
   state, KTX protocol, or hidden messages.
3. The top-level README's event list, result-schema blurb, and known-
   limitations list if consumers can see the change.

**When you change the schema**, bump `CurrentSchemaVersion` in
`qwanalytics/result/result.go` and mention the bump in the commit message.

**When you delete or move files**, fix every README / doc cross-reference
that pointed at them. `grep -r` before committing.

**When the user explicitly adds documentation** (new plan file, design note,
CLEANUP-PLAN update), ensure it's referenced from the right README so a
future reader can find it.

## Architecture you must respect

Three modules in a Go workspace (`go.work`):

- **`qwdemo/` (Layer 1)** owns the MVD wire format. `events/` is the
  public contract; `parser/` does the decoding; `source/mvd/` wraps it
  in the Source iterator interface. No `qwanalytics` or `qw-web`
  imports allowed here.
- **`qwanalytics/` (Layer 2)** takes an `events.Source` and produces a
  `result.Result`. `result/` is the stable JSON contract. `analyzer/`
  implements each sub-analyzer. Nothing here reaches into MVD bytes —
  everything flows through events.
- **`qw-web/` (Layer 3)** is one consumer of Result. The WASM entry
  is in `cmd/wasm/`; the static frontend is in `static/`. Nothing
  qwdemo or qwanalytics imports from here.

Put logic in the lowest layer that can express it. Protocol-level signals
(entity state, health transitions) belong in the parser. Cross-event
derivation (phase timelines, streaks) belongs in analyzers. Rendering
belongs in the frontend.

## Conventions

- Commit messages: subject under ~70 chars, imperative mood, body
  explains the *why* and cites source-code line references when
  relevant. No AI-attribution trailers.
- Don't add trailing comments like "// added for X feature" — that
  belongs in the commit history.
- Match existing code style — no new lint configs, no reformatting
  of untouched files.
- **Always run tests.** `make test` (which runs
  `go test ./qwdemo/... ./qwanalytics/... ./qw-web/...`) before every
  commit, no exceptions for "trivial" changes. If a test you don't
  understand fails, surface it — don't skip it.
- Tests come in three layers:
  1. **Unit tests** alongside the code (`*_test.go`). Coverage spans
     `qwdemo/parser/` (KTX pickup/drop/print, stats, userinfo),
     `qwanalytics/analyzer/` (backpacks, duel normalisation, items,
     loc graph, metadata, obituaries, pickup invariants, timeline +
     blip filter, weapon pickups), `qwanalytics/internal/hubfetch/`,
     and `qwanalytics/mapgen/{bsp,mapgeom}/`.
  2. **Golden corpus** — `qwanalytics/analyzer/golden_test.go` reads
     `qwanalytics/testdata/corpus.json` (a manifest of hub.quakeworld.nu
     gameIds), fetches each demo on first run into
     `qwanalytics/testdata/cache/` (gitignored), and pins the full
     pipeline output to `qwanalytics/testdata/golden/<label>.json`
     (committed). Steady-state runs are offline.
  3. **Diagnostic harness** in `qwanalytics/diagnostic/` —
     `TestDiagnosticParseDemos` runs every `.mvd` / `.mvd.gz` dropped
     into its `testdata/` through the parser in warning-collecting
     mode and applies data-quality invariants on the result. No-op
     when no demos are present.
- If the golden test fails on an intended change, regenerate with
  `go test ./qwanalytics/analyzer/... -run TestGoldenCorpus -args -update-golden`
  (the `-update-golden` flag is registered only in the analyzer
  package — wider scopes like `./qwanalytics/...` fail in `mapgen`
  with "flag provided but not defined") and commit the regenerated
  `qwanalytics/testdata/golden/*.json` together with the code change.

## Surface authoritative data, don't filter

The pipeline's job is to faithfully report what happened. When a
downstream consumer (an analyzer, a UI panel, a result-schema field)
is tempted to drop, dedupe, smooth, or coerce a value because it
"looks wrong" or "is noisy", the default answer is **no** — let the
raw signal through and trust the consumer to interpret it.

Reach for filtering only when:
- the data is provably bogus (a parser-level invariant violation, a
  protocol-impossible state), or
- leaving it in would actively mislead a downstream consumer that
  cannot itself disambiguate.

When you do filter, leave a comment on the *why* (the invariant or
incident that motivated it) so the next reader can tell the deliberate
filter from accidental data loss. If you find an existing filter
without that justification, treat it as suspect and ask before
extending it.

## Ground-truth sources

The vendored `ktx/`, `mvdsv/`, and `ezquake-source/` directories
(untracked, provided per-machine) are the authoritative references
for protocol and server-mod behaviour. When in doubt about how a
message or event is emitted, read those first — not the documentation
at the top of this repo. Memory pointer: see
`/home/ubuntu/.claude/projects/-home-ubuntu-coding-mvd-analyzer/memory/reference_ground_truth_sources.md`.

## Behavioural expectations

- **Prefer action over planning** for small edits, but write down a
  plan file for multi-step refactors (the user will invoke `/plan`
  explicitly for that).
- **Read existing code before adding new code** — this project has a
  lot of domain-specific conventions (event naming, result-struct
  shapes, pipeline ordering) that are easier to match than to
  rediscover.
- **Run `make test`** after every change (see "Always run tests"
  above). For deeper sanity-checking on non-trivial work, also run a
  sample analysis: the demos under `demos/` are the regression corpus,
  and a quick
  `go run ./qwanalytics/cmd/qw-analyze -format json demos/broken.mvd.gz`
  catches most categories of break that unit tests miss.
- **Don't commit generated artefacts** (`mapgen` binary, `dist/`).
  Those are in `.gitignore` — don't route around.
- **Never destructively rewrite history** (force-push, reset --hard,
  amend published commits) without an explicit instruction.

## Quick reference

Build everything: `make build`
Serve the web UI: `make serve`
Run all tests: `make test`
Regenerate map geometry: `go run ./qwanalytics/cmd/mapgen -bsp-dir /path/to/bsps -verbose`
Analyze one demo: `go run ./qwanalytics/cmd/qw-analyze -format json demos/X.mvd.gz | jq`
