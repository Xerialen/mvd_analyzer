# Release notes

Feature-level changes as they land on `main`, newest first. Dates are
the merge dates on `main`; schema bumps reference
[RESULT_SCHEMA.md](mvd-analytics/RESULT_SCHEMA.md) for field-level
detail.

## 2026-07-05

- **Web: chat shows every authentic message (no schema change).** The chat
  panel silently dropped any message whose exact text repeated within three
  seconds. The wire-level duplication that filter targeted (KTX sprints each
  say once per recipient at one wire timestamp) is already collapsed upstream
  by the analyzer's exact-key dedupe, so the web filter was only swallowing
  authentic repeats — a re-sent bind, two identical obituaries in quick
  succession. Removed; only the match-window clip remains.
- **Single obituary parser; timeline frag weapon codes corrected (no schema
  change).** The obituary/suicide pattern table lived twice — once for the
  frag log (`frags`), once for the timeline message stream
  (`messages.events[type=frag]`) — and the two had drifted. The timeline copy
  is now generated from the same table the frag log uses, so for the affected
  obituary lines the timeline stream's `weapon` now matches `frags[]` exactly.
  Consumer-visible corrections in `messages.events` (the frag log was already
  correct):
  - drowning self-kills now carry weapon `water` (was `drown`), matching the
    documented FragEntry vocabulary;
  - the unknown-cause self-kill "X somehow becomes bored with life" is now
    `suicide` (was mislabelled `rl` by the shorter-substring fallthrough);
  - CRMod obituaries ("blown to chunks", "shish-kebabed", "disembowled",
    "gets intimate", "warm fuzzy feeling") and KTX `k_spawnicide` self-kills
    ("shiny spawn point", "baby factory", "poor life choices") now produce a
    timeline frag event instead of being dropped or mislabelled.
  These lines are rare (env/CRMod/spawnicide), so the golden corpus output is
  byte-identical; the fix is verified by unit tests. Internal cleanup in the
  same change: one canonical slot→identity resolver (`ResolveSlotAt`) replaces
  five drifted per-analyzer copies, and one `parseInfoString` helper replaces
  three duplicated serverinfo walkers.

- **One entity-delta wire-layout implementation; FTE parse fixes (no
  schema change).** The parser's entity-baseline and entity-delta
  layouts each had a decode copy and a separate byte-skip copy that had
  drifted apart; they are now a single reader each (`readBaselineBody`
  for the baseline body, `readDeltaBits` + `readEntityDelta` for the
  delta), and `svc_spawnstatic` / `svc_fte_spawnstatic2` decode through
  that same code and discard rather than re-skipping by hand. Three
  FTE-only divergences between the old copies are fixed by construction
  to match ezquake (`cl_ents.c`) and mvdsv (`sv_ents.c`): the FTE
  "evenmorebits" byte is read only when an FTE extension was negotiated
  (the `svc_fte_spawnbaseline2` / `svc_fte_spawnstatic2` path previously
  read it unconditionally, misaligning a non-FTE stream), and the
  transparency / colour-mod fields are gated on the negotiated
  `FTE_PEXT_TRANS` / `FTE_PEXT_COLOURMOD` (previously consumed on the
  flag bit alone). Only demos that negotiated FTE entity extensions are
  affected — none exist in the golden corpus, which stays byte-identical,
  so the schema is unchanged (v48).

- **Dev-CLI correctness fixes (no schema change).**
  - `qw-analyze -view state-at -time 0` (and `-time 0s`) now queries match
    start instead of erroring with "requires -time"; flag presence is
    tracked separately from the zero value.
  - `mapgen` now fails the run when its `-demos` directory can't be walked
    (e.g. a typo'd path) instead of silently emitting an unpruned corpus,
    matching the existing `-bsp-dir` behaviour.

- **mvd-api / mvd-mcp hardening for hosted use (no schema change; REST
  surface semantics + one MCP tool-schema change).** Prerequisites for
  running the API/MCP on the internet for third-party apps:
  - **Downloaded demo bytes are verified against their SHA before
    caching.** A cold download now hashes the `.mvd.gz` bytes and rejects
    them as `hub_upstream` (502) if they don't match the hub-claimed
    `demo_sha256` that keys the cache, the `sha:` address, and the ETag —
    a corrupted CDN object can no longer poison the cache permanently.
  - **Hub "not found" is classified by error identity, not message
    text.** A new `hubfetch.ErrNotFound` sentinel + `errors.Is` replaces
    two `strings.Contains(err, "not found")` checks, so a hub 5xx whose
    body merely contains "not found" is now correctly a 502 (was
    misreported as `demo_not_found` 404).
  - **Model-supplied `demoId` is validated + escaped in the MCP proxy.**
    All demo-scoped tool calls route through a `demoPath` helper that
    rejects anything but a canonical `gameId:N` / `sha:HEX` and
    PathEscapes it, so an id containing `/`, `?`, or `#` can no longer
    reroute a request to a different endpoint.
  - **Per-demo lazy computes no longer serialize globally.** `/los` and
    the on-demand shot-stream rebuild (`/shots`, `/aim`, `/streams/*`)
    now lock per demo SHA (shared `KeyedMutex` helper) instead of one
    server-wide mutex each, so a request for demo B does not queue behind
    demo A's multi-second pass. (Disk persistence of the computed
    artifacts is deliberately out of scope here.)
  - **Cache correctness nits.** A resolved-`GameInfo` map entry that
    previously leaked for process life when a demo was served from a
    warm tier is now drained unconditionally; `GetResult` documents that
    a cold fetch runs its hub download + parse to completion regardless
    of the caller's context (the singleflight shares one computation, so
    the first caller's cancellation must not poison the others).
  - **The stream-enriched endpoints signal a degrade instead of serving
    silently-incomplete data.** When the tier-1 MVD bytes are missing so
    the opt-in weapon-fire streams can't be rebuilt, `/shots`, `/aim`,
    and `/streams/*` now set `X-Shot-Streams: unavailable` (+
    `Cache-Control: no-store`) rather than quietly returning the lean
    result.
  - **MCP tool-schema change:** `getBackpacks`'s `weapon` input is now a
    `string[]` set (forwarded as CSV), matching REST `/backpacks` since
    v36 (was a single string); `getEvents`'s `types` schema now lists the
    opt-in `damage` / `telefrag` / `stomp` kinds.
  - **Docs:** API.md's `/overview` example is corrected to int32
    milliseconds (the code has emitted ms since v24) with an added §2.1
    units row; the §2.4 error table gains `shots_unavailable` /
    `aim_unavailable`; and stale `schemaVersion: 36` examples are updated
    to 48.

- **Clean end-of-demo, and truncated demos become visible (no schema
  change).** The standard MVD termination — `svc_disconnect
  "EndOfDemo"` — now surfaces through `events.Source.Next` as `io.EOF`
  (the value the Source contract always promised) instead of a hard
  error the analytics registry silently swallowed; any tail events in
  that final message are drained before EOF. A *non*-EOF `Next` error
  (a truncated or corrupt stream) now records `"event stream aborted:
  …"` into `result.errors`, and a failed region-control pass records
  `"region control: …"` there too, so a partial parse is
  distinguishable from a clean one. Previously-ignored read/skip errors
  inside the parser (sound/baseline/download skips, serverdata movevars,
  entity-diff emits) now propagate instead of silently misaligning the
  decode cursor, and a truncated known command is reported as
  `parse_error` naming the command rather than `unknown_svc`. No value
  changes on well-formed demos — the golden corpus is byte-identical
  (its `EndOfDemo` message carries no tail events); the new `errors`
  entries appear only on broken demos, so the schema version is
  unchanged (v48).

- **Timeline, scoreboard and duel-detection correctness fixes (schema
  v48).** Five bugs in already-emitted values, no field-shape change:
  - **Kill events were on the wrong clock and mislabeled in duels.**
    `timelineAnalysis.killEvents` shipped demo-relative (each kill
    ~`demoOffset`, typically ~10 s, later than the matching
    `deathEvents`) and, in 1v1s, tagged with a raw colour team like
    `"red"` instead of the player name — the two post-processors that
    rebase time and rewrite duel teams both skipped it. The Timeline
    per-player drill-down plots `killEvents − deathEvents` on one axis,
    so every kill drew displaced and duel team colours were wrong. Now
    `killEvents` is match-relative and duel-team-labelled exactly like
    its sibling streams. A new structural invariant test over the golden
    corpus enforces this for *every* timeline event stream, so a future
    field that forgets a post-processor fails loudly instead of shipping
    wrong.
  - **A chat line could start or end the match.** Match-start/-end
    detection scanned every print for substrings like "go!" / "game
    over", including player chat: a pre-match "go go go!" started
    recording warmup, a mid-match "gg game over" froze every stat and
    stream for the rest of the demo. Chat prints (level 3) are now
    ignored for match timing; only broadcast prints move the match
    window. The obituary parser likewise rejects chat-level prints, so a
    say containing " rides " can't inject a phantom frag.
  - **CRMod super-shotgun kills were mislabeled.** "X eats 2 scoops of
    Y's lead shot" was matched by the generic grenade " eats " pattern
    first, so the kill came out as weapon `gl` with a phantom killer
    "2 scoops of Y". It is now correctly `ssg` with killer Y.
  - **0-frag players no longer vanish; team games aren't mistaken for
    duels.** A player who legitimately finished on exactly 0 frags was
    dropped from `match.players`/`match.teams`. That is fixed
    (surface-authoritative-data), and duel detection now trusts
    `demoInfo.players` as authoritative — so a 2on2 in which two players
    end on 0 frags is no longer misread as a 1v1 and team-renamed.
    To keep actual spectators from leaking in once the 0-frag filter is
    gone, the reader now parses the server-set `*spectator` userinfo
    star key (mvdsv strips the client's bare `spectator` key before
    broadcast, so the old bare-key check never matched in MVDs) and
    recomputes the flag on every full userinfo update the way ezquake
    does, so a slot reused after a spectator disconnects doesn't
    inherit a stale flag.
  - **Consistent powerup interval end times.** On a demo cut before
    intermission, quad/pent/ring runs were closed at each player's last
    position sample while weapon intervals closed at the global last
    sample; both now use one shared effective match end.

- **Crosshair placement plots in true Quake units.** The Aim Stats
  density images and yaw/pitch marginals plotted hull-normalized error
  (each axis divided by the target's angular half-extent), so one x
  unit was ~16-23 qu while one y unit was 28 qu, and the "hull" box
  drew as a square. Both now plot the offset in Quake units at the
  target's range (derived from the existing `dyaw`/`dpitch`/`dist`
  columns — no schema change), the axes share one scale, and the
  solid outlined box is the player's collision box true to shape (32
  wide × 56 tall), with a dashed outline at its corner-on silhouette
  (~45 qu wide — the axis-aligned box reads √2 wider viewed
  diagonally). Extents ±96 qu × ±64 qu, ticks every hull-width (32
  qu), hover read-outs and histogram bins in qu.

- **LG whiff split reclassified: miss / blocked / far (schema v47).**
  The split classified from the beam endpoint only, so every beam that
  stopped on geometry short of its ~600u max range counted as `blocked`
  — including shafts fired into a wall with nobody behind them, which
  made Blocked % implausibly high. Now a whiff only counts as blocked
  or out of range when the shooter was on target: `blocked` = the beam
  stopped short on geometry and its extension to full range crosses a
  live enemy's collision hull (in range — the obstruction denied a
  would-be hit); `outOfRange` = the beam ran its full length and its
  extension to infinity crosses a live enemy's hull (denied by reach);
  `miss` = every other whiff, a plain aim error with no enemy on the
  beam's line (shares the `miss` field with the SG/SSG pellet split). The
  endpoint-proximity `nearMiss` field is **removed** — with blocked
  detection on the beam line, the near/wide distinction among aim
  errors carried no signal. The Aim Stats LG table now shows Miss /
  Blocked / Far (+ share-of-fires %) with reworded tooltips. Only
  beam-enriched parses are affected (the split needs `streams.beams`),
  so the golden corpus is untouched.

## 2026-07-04

- **Timeline frag/death events no longer dropped for players with no
  resolvable team.** `timelineAnalysis.fragEvents` and `.deathEvents`
  gated on a resolvable team, so a duel player whose userinfo *and*
  KTX demoinfo team are both empty lost every entry: gameId 224758
  (iddQd, 34 deaths missing from the frags/deaths drill-down) and the
  bravado golden (speedball, 7 deaths). FragEvents were partially
  papered over by the duel post-processor's frogbot synthesis (rebuilt
  from obituaries); DeathEvents had no such fallback. Both exports now
  gate on a resolvable *name* — matching `killEvents`' documented
  rationale — with `team` best-effort (`""` when unresolvable,
  rewritten to the player's own name by duel normalization in 1v1s).
  Audited the remaining team-keyed consumers for the same class of
  loss: weapon pickups / items / messages fallback-fill instead of
  dropping, and aim, airgibs, loc graph, and region control are
  (re)computed after duel normalization, so they were already correct.

- **Frag streaks now include the opening life.** A player already alive
  when the match begins has no spawn recorded for that first life (the
  parser's initial SpawnEvent fires during warmup and is dropped as
  pre-match), so the spawn-to-death run from match start to first death
  never entered `timelineAnalysis.fragStreaks` — a player who never
  died had no streak at all (gameId 224758: reload's 33-frag run was
  missing). The streak detector now synthesizes a match-start spawn
  when a death or credited frag predates a player's first recorded
  spawn — strictly after match start, since KTX's match-start reset can
  surface as a death at exactly `StartTime` (gameId 212260) and must
  not shift the spawn/death pairing. Opening runs read `time: 0`
  (match-relative).

- **Weapon-stay pickup recovery (schema v46).** In weapon-stay modes
  (serverinfo `deathmatch` 2/3/5 or `coop` — dmm3, the standard
  duel/2on2 mode, included) KTX never emits `//ktx took` for weapons
  and the weapon entity never leaves the wire, so `result.weaponPickups`
  contained **zero world weapon pickups** on those demos and weapon
  `items` timelines never closed a phase. Both analyzers now synthesize
  the pickups from STAT_ITEMS weapon-bit 0→1 transitions: weapon_pickups
  records kind-level entries (`inferred: true`; `source: "world"` when
  the picker was in touch range of a matching pad during the stat-lag
  window, else the new `"unknown"` — typically a non-RL/LG backpack
  grant), and items.go closes the matched entity's phase as a
  zero-length unavailability (`takenAt == respawnAt`; the weapon never
  left the map). Spawn-loadout bursts and `//ktx bp` grants are
  deduplicated. Verified against KTX's own per-player counters
  (`TookWeaponHandler` increments before the weapon-stay early return,
  so `demoInfo.players[].weapons[].pickups.*` were always correct).

- **Pickups tab: KTX counters are the displayed numbers.** The
  weapon/item verify cells now show the KTX-authoritative counter as
  the cell value and acknowledge the analytics-derived count in the
  tooltip, instead of showing the analytics count and flagging
  divergence red. Rationale: in weapon-stay demos the analytics
  reconstruction is known-imperfect (wire-invisible grab-then-die
  coalescing, pad-vs-pack ambiguity), so a small divergence is
  expected, not an error — while the analytics stream stays the right
  source for timestamped/per-entity questions (the per-entity `@ loc`
  columns are unchanged). Demos without KTX pickup counters (old /
  non-KTX servers) fall back to the analytics counts, trusted as-is.

- **Duel team normalization now covers pickup/shot data (v46).** In 1v1
  demos `normalizeDuelTeams` rewrites every player's team to their own
  name, but `items` phase teams, `weaponPickups` team/dropperTeam,
  `backpacks` team, `shots` stream/byPlayer teams (feeding `aim` teams),
  and `airgibs` attacker/victim teams kept the raw pre-normalization
  strings — so the Pickups tab's per-team aggregation bucketed duel
  pickups under stale colour labels and showed zero rows. All are now
  rewritten in the duel pass (airgibs sources teams from the normalized
  player streams). The pass also reclassifies `shots[].victimKinds`
  `"team"` → `"enemy"` and folds the per-weapon `teamHits` bucket into
  `enemyHits` (v45's `victimKindOf` compares raw team strings, so a duel
  where both players share a colour team classified every opponent hit
  as team damage; in a 1v1 any non-self victim is an enemy — exact, since
  the single opponent pair classifies uniformly). Aim's enemy/team splits
  inherit the correction via `aimPost` ordering. The web Pickups tab's
  per-team tables also join the existing duel-mode hide
  (`team-aggregate-table`), matching the other per-team stats tables.

- **Pickup attribution: touch-instant sampling + measured 128 u gate
  (v46).** The Layer-4 distance corroborator now samples each player's
  position from the per-frame history at the entity-removal instant
  (which is the touch frame) instead of a latest-only sample up to
  250 ms stale, and all proximity consumers (corroborator, insta-regrab
  picker, weapon-stay classifiers) share one 128 u touch gate — genuine
  touches measure 54-104 u across the corpus, non-touch same-room grabs
  ≥150 u. A handful of beyond-gate guesses become honestly unattributed.

- **Shots/aim: enemy / team / self victim classification + Aim Stats
  Victims cycle** (schema v45). Every linked victim is now classified
  relative to the shooter — `enemy`, `team` (same non-empty team, not
  self) or `self` (own wire slot: rl/gl self-splash, i.e. rocket jumps) —
  mirroring the damage layer's `isSelf`/`isTeam` semantics, per victim
  per fire (one rocket can splash an enemy, a teammate and the shooter at
  once and counts in every bucket it has a victim in). `Shot` gains
  `victimKinds`, `WeaponShots` gains `enemyHits`/`teamHits`/`selfHits`,
  the aim crosshair/ramp samples gain a `team` column, and `WeaponAim`
  gains `enemy`/`team`/`self` counter slices (`WeaponAimSplit`: hits,
  pellet splits, direct — see RESULT_SCHEMA.md for the emission rules).
  All additive; `hits`/`accuracy` stay all-victims for KTX scoreboard
  parity. The Aim Stats tab gains a **Victims** filter
  (All / Enemy / Team / Self) that slices the weapon tables, the
  crosshair heatmaps + marginals and the LG ramp; **All** (default)
  preserves the previous numbers, and **Enemy** is the first view where
  rocket jumps no longer inflate RL hit % (they were always counted as
  hits — now visible under Self). Duels hide the Team option (no
  teammates); Self shows tables only (hitscan cannot self-hit, so there
  are no self crosshair samples). The MCP `getAim` tool and `/aim` +
  `/shots` endpoints carry the new fields automatically. Tab layout
  reworked alongside: the Victims strip sits at the top of the tab, the
  LG and SG crosshair blocks sit side by side where the pane is wide
  enough (stacking on narrow panes), and the player picker moved into
  the Crosshair placement panel — the only place it applies.

- **Aim: hits attribute to the confirmed victim** (schema v44). The v43
  liveness gate excluded the victim of a killing blow from attribution —
  the kill lands in the same frame the victim dies, so `losAliveAt` read
  the victim as already dead at the fire time. In team games the sample
  went to the nearest *other* live enemy, producing impossible
  "hits" tens of hull-widths off target (the big far-edge bars with
  nonzero hit counts in the Aim Stats marginal histograms — verified on
  hub demo 223930: 78 of 79 LG edge-bin hits were killing blows, the 79th
  a team-damage hit, while the beams ended inside the actual victim's
  hull); duels dropped their killing-blow samples entirely. Crosshair
  samples of hit shots now attribute to the server-confirmed victim
  (nearest by crosshair error when a pellet fire hit several), with no
  liveness gate and no enemy filter (team damage is a confirmed target —
  `tgt` can then name a teammate); misses keep the live-enemy
  nearest-crosshair heuristic. Duels gain one crosshair sample per
  hitscan kill; the web attribution note now spells out the hit/miss
  split.

- **API: `/shots` endpoint + complete `/aim`; MCP: `getAim`** (no schema
  change). New `GET /v1/demos/{id}/shots` serves the per-fire weapon stream
  (`result.Shots`: linked hits/victims, per-player aggregates, KTX
  reconciliation; `nails=1` opts into ng/sng fires). `/aim` and `/shots` are
  served from the stream-enriched parse (`EnsureShotStreams` re-parses on
  first request, then caches — the rebuilt `Shots`/`Aim` blocks are grafted
  onto the cached result), so the stream-derived aim blocks (RL/GL
  direct/splash, the LG near/blocked/out-of-range split) are now always
  present over the API instead of silently absent. New
  `GET /v1/demos/{id}/airgibs` serves the Key Moments airgib list
  (`timelineAnalysis.airgibs` — the last Result block with no endpoint);
  empty, not an error, on maps without a provisioned BSP. The MCP server
  adds a `getAim` tool (aim stats only — the raw per-fire stream stays
  API/JSON-only by design).

- **Wider BSP corpus + phantom map-alias fix.** `scripts/fetch-bsps.sh`
  now provisions the most-played 1on1/2on2 community maps from a
  hub.quakeworld.nu sample (ztndm3, metron, toxicity, dad2, catalyst,
  nova, pocket, katt, shifter, spinev2, zeal), so loc attribution,
  map geometry and the BSP-gated visibility metrics light up on them.
  Removed the `phantombase → phantoma` map alias in
  `mvd-analytics/loc/loader.go`: the phantom family (`phantom` /
  `phantoma` / `phantombase`) are distinct map versions and now each
  resolve to their own loc/entity/geometry data. Previously the alias
  routed the ~1000-game `phantombase` corpus through the 23-game
  `phantoma` data; this touched every `NormalizeMapName` consumer — loc,
  mapents, mapbsp, and the `/v1/maps/{map}` endpoint. Only the dominant
  `phantombase` BSP is fetched; the low-play predecessors are not.

## 2026-07-03

- **Aim: alive-gated attribution + density image, marginal histograms,
  share-of-fires columns** (schema v43).
  Crosshair-error target attribution now skips enemies who are dead at the
  fire time (same liveness rule as line of sight, `losAliveAt` over the
  spawn/death streams). Dead players keep streaming position samples — the
  death-anim body — so a corpse sitting near the crosshair could previously
  win nearest-crosshair attribution in team games and log a guaranteed-miss
  sample; a duel fire while the lone enemy is dead now emits no sample at
  all (the per-weapon fire counts still include it). No field changes —
  sample counts and targets shift on team demos. The web Aim Stats tab
  replaces the crosshair grid with a **smoothed density image** per weapon
  (LG and SG): a Gaussian-smoothed 2-D histogram on canvas (the shared
  viridis ramp anchored to the page background at zero, like the table
  heatmaps; no external deps) with hull box + dead-center overlays, axis
  ticks and a colorbar in shots per bin; hover reads exact shot/hit counts.
  Under each image, two **marginal histograms**: the same normalized
  samples projected onto one axis at a time — yaw (enemy left ↔ right) and
  pitch (enemy below ↔ above) — zero-centered bins, with the |n| ≤ 1
  on-hull band shaded and a dead-center rule. Image and histograms share
  the same extents (yaw ±6, pitch ±4); samples outside them are dropped
  from the image (a clamp pile-up would paint a bright rim) but stay
  visible in the histograms' clamp edge bins. The LG ramp panel is folded
  into the LG block as a third histogram in the same style (hit % by time
  since the shaft opened, hover for per-bin counts; `lgRamp` in the
  schema is unchanged), and the histograms stack vertically. All binning
  stays client-side. The per-weapon accuracy
  tables add **share-of-fires % columns** next to every count (LG
  near/blocked/far, RL/GL direct/splash/missed, SG/SSG full/partial/miss)
  so players with different shot volumes compare directly.

## 2026-06-28

- **Aim analytics** (schema v41–v42). A new top-level `aim` block: per-player aim
  metrics derived as a post-processor from `shots` + `streams`
  (position/view interpolated at fire time) + `damage` + LG `beams`. Columnar
  per-shot **crosshair-error samples** for hitscan (sg/ssg/lg) — both signed
  degrees off the enemy and a version normalized by the target's angular size
  (range-comparable; radius 1 ≈ the hitbox edge); an **LG ramp-onto-target**
  series (ms since shaft start + hit); **rocket direct/splash** counts; and an
  **LG reach/whiff** classification (near miss vs blocked vs unresolved).
  Target attribution is exact in duels (`mode: "duel"`) and a labeled
  nearest-crosshair-enemy heuristic in team games (`mode: "team"`). Computed
  by default for every client (CLI / API / web) — the crosshair + ramp blocks
  always; the rocket + reach blocks when the projectile/beam streams were
  built (the WASM map build, `qw-analyze -include projectiles,beams`). The web
  UI adds an experimental **Aim Stats** tab that renders the block: a rich
  per-weapon table (SG/SSG pellets hit/fired + full/partial/whiff fires, RL/GL
  direct/splash/missed, LG near-aim/blocked whiffs — the pellet and direct
  figures match the server's authoritative stats), a hitscan
  crosshair-placement heatmap (shot density, normalized so ±1 = the hull edge),
  and an LG ramp chart. Also adds a reusable
  `result.PositionTrack.SampleAt` interpolating
  sampler (position + shortest-arc view angle + velocity) other position-
  derived analytics can adopt. The web table is one table per weapon with
  players on the rows (team-coloured like the Summary tab), and the heatmap is
  split into LG and SG. Shots gained `warmup` (v42) — fires outside the match;
  the aim analysis is match-time and excludes them, matching `shots.byPlayer`.

## 2026-06-27

- **Weapon-fire map overlay** (schema v40). Two opt-in spatial streams under
  `streams` for the 3D map view: `projectiles` (every tracked rocket/grenade
  flight as a spawn→despawn segment + times) and `beams` (every LG
  `TE_LIGHTNING2` bolt as a muzzle→impact segment + time), both columnar.
  They are **off by default** — sizeable in a team game (thousands of beams)
  — and built only on request: `qw-analyze -include projectiles,beams`, and
  the WASM map build (where the result stays in browser memory, so no extra
  download). The map renders rockets/grenades as moving dots and LG bolts as
  brief beams at the playback cursor (`drawProjectiles` / `drawBeams`). The
  REST API serves them as three independent, build-on-demand endpoints —
  `GET /v1/demos/{id}/streams/{projectiles|beams|nails}` — re-parsing the
  cached demo on the first request and latching the result (like `/los`).

- **Nail (ng/sng) tracking** (schema v40, opt-in). A separate, highest-volume
  request (`qw-analyze -include nails`) that decodes nails — spike packet
  entities on `sv_nailhack` servers (the common case), or the `svc_nails` /
  `svc_nails2` stream otherwise — brackets each nail's flight, links ng/sng
  fires to their nail damage (`hit`/`victims`, approximate: per-fire linking
  credits one of SNG's two nails), and emits a `streams.nails` map overlay.
  Off everywhere by default — including the web map — so nails are never
  downloaded unless explicitly requested. Per-player nail hit counts track
  KTX's within a small margin across the corpus.

- **Per-shot weapon-fire stream** (schema v39). New top-level `shots`
  result: who fired what weapon, at exactly what match-relative ms — the
  foundation for accuracy metrics (including over short intervals) and for
  external analysis correlating crosshair/aim movement with when shots were
  taken (join a shot's `time` against `streams.players[].pos` view
  angles/velocity).
  - **Detection.** SG/SSG/RL/GL/NG/SNG fires come from `svc_sound` on the
    shooter's `CHAN_WEAPON` — the sound carries the firing entity, so
    attribution is exact and works on **any** QW server (not just KTX), and
    the distinct fire wavs disambiguate RL vs GL where ammo deltas cannot.
    (The Quake sound filenames are historically mismatched: the rocket
    launcher fires `sgun1.wav`, the nailgun fires `rocket1i.wav`.) LG has no
    per-shot fire sound, so it is counted from its `TE_LIGHTNING2` beam —
    emitted once per fire tick and carrying the firing entity directly
    (`source:"beam"`). One beam == one LG attack == one cell, so LG counts
    match KTX `acc.attacks` exactly. The beam decode also surfaces the
    muzzle→impact geometry as `BeamEvent` (for map rendering).
  - **Truthful cross-linking.** Instantaneous hitscan fires (sg/ssg/lg) are
    linked to the damage they caused in the **same server frame** via the
    KTX `mvdhidden_dmgdone` stream (`hit`/`victims`). Rocket/grenade fires
    (rl/gl) are linked by **entity flight tracking**: the projectile entity
    brackets the flight (`spawn → despawn`), so a fire is matched to its
    launch frame (by muzzle) and its impact damage to the shooter's
    same-weapon damage at the despawn frame — which disambiguates *which*
    fire caused *which* impact when several rockets are in flight (a naive
    "next damage" link cannot). Across the corpus, rl/gl connect-counts
    match KTX's authoritative `real` hit counts to within one. Nail fires
    (ng/sng) ride a separate stream and stay unlinked for now.
  - New parser events `ProjectileSpawnEvent` / `ProjectileDespawnEvent`
    track rocket (`progs/missile.mdl`) and grenade (`progs/grenade.mdl`)
    entities by their recycled entity number.
  - **Validation built in.** A `reconciliation` block cross-checks detected
    counts against KTX's authoritative `acc.attacks`; across the golden
    corpus the converted `streamAttacks` matches KTX exactly (a 4on4 game
    reconciles 42/42 player×weapon rows), with LG occasionally off by a
    single cell at a death/discharge boundary.
  - New parser events: `svc_sound` is decoded into `SoundEvent` and
    `svc_soundlist` is captured to resolve fired sounds to weapons.

- **Line-of-sight & potential-visibility metrics** (schema v37–v38,
  [#94](https://github.com/galfthan/mvd_analyzer/pull/94)). Two new
  per-opponent visibility tracks on `PlayerStream`, both computed lazily
  on first request (the heaviest position-derived pass, BSP-gated — only
  on maps with a provisioned BSP; absent from the default parse):
  - **`PlayerStream.LOS`** (v37) — geometric **line of sight**: intervals
    during which a player has a clear ray to an opponent (eye point at
    `origin + (0,0,22)`, nine rays against the BSP clip hull and moving
    movers, the opponent's bounding-box corners + centre). Directional, so
    asymmetric one-way sightlines are preserved; gated to live players
    (alive from match start, not the first recorded spawn).
  - **`PlayerStream.PVS`** (v38) — server-reproduced **potential
    visibility**: whether a live mvdsv would have sent that opponent's
    entity to this player's client at that frame, made wire-exact against
    `SV_PlayerVisibleToClient` (fat PVS of `origin + view_ofs` vs. the
    target's entity-leaf set, with the `MAX_ENT_LEAFS` overflow gate).
    PVS ⊇ LOS by construction; the **PVS-minus-LOS gap** is an
    occlusion-tolerant proximity/awareness signal, not a sightline.

  Surfaced three ways: the REST/MCP **`/v1/demos/{id}/los`** endpoint
  (returns both `los` and `pvs`), the CLI, and the **web map overlay** —
  two per-player toggles (**LOS**, **PVS**) that draw inter-player lines
  on the 3D map tab (PVS as thin faint lines beneath the thicker LOS
  lines), both filled by one lazy pass and cached client-side. Both
  metrics are `omitempty`/absent on non-BSP maps, so the schema bump is
  additive for existing consumers.

## 2026-06-20

- **API contract cleanup** (schema v36). Consolidates the REST/MCP
  surface; section-filtering logic moves into the shared
  `mvd-analytics/view` layer (REST, MCP, and WASM now share one tested
  implementation — no wire change from that move). The observable contract
  changes:

  **Breaking**
  - **`match.startTime` / `match.endTime` removed** from the result. They
    were always `0` / equal to `duration` and duplicated
    `streams.global.matchStart` / `matchEnd`. Read **`match.duration`** for
    match length and **`streams.global`** for the match window. (The
    `endTime` key disappears from the `match` object; `startTime` was
    already `omitempty`-absent.) Schema bumps **35 → 36**, so the ETag /
    `X-Schema-Version` change and cached results re-validate.
  - **`GET /v1/demos/{id}/map-entities` removed** (and the MCP
    `getMapEntities` tool). Use **`GET /v1/maps/{map}/entities`** (MCP
    `getMapEntitiesByMap`) — identical payload; get the map name from
    `/overview`.
  - **`view_error` (400) is gone** — every malformed/rejected query now
    returns **`invalid_param` (400)**, including an unknown `fields` code or
    reducer name.

  **Additive / non-breaking**
  - **`/region-control` accepts `from` / `to`** (match-relative seconds) to
    clip control attribution to a sub-window.
  - **`weapon` is a comma-separated set on every endpoint** that takes it
    (`/frags`, `/damage`, `/backpacks`, `/weapon-pickups`); `/backpacks`
    previously accepted only a single value.
  - **Query-parameter names are case-insensitive** (canonical spelling
    stays camelCase: `windowMs`, `minDwellMs`, `includeTeam`).
  - **Documented "section absent" rule**: capability-gated sections
    (`demoinfo`, `damage`, `frags`, `loc-graph`, `metadata`,
    `region-control`) return `422 <section>_unavailable`;
    always-computable / list sections (`items`, `backpacks`,
    `weapon-pickups`, `chat`) return `200` with an empty body.

- **3D map view & mover streams** (schema v34–v35,
  [#91](https://github.com/galfthan/mvd_analyzer/pull/91)). `streams.movers[]`
  carries the pose timeline of every tracked brush-model entity (lift, door,
  plat, train); map geometry gains version 4 (per-vertex 3D triangles +
  optional `walls` / `liquids` / `submodels`), and
  `timelineAnalysis.locationData` collapses to one medoid anchor point per
  loc name (v34) so map labels no longer duplicate. Drives the new
  orbit-camera 3D map tab over a usage-pruned committed corpus.

## 2026-06-14

- **Float32 positions, velocity & height** (schema v33,
  [#90](https://github.com/galfthan/mvd_analyzer/pull/90)). `pos.x/y/z`,
  `pos.vx/vy/vz` and `pos.h` change from `int32` to `float32`, so the
  wire-native sub-unit origin is no longer truncated to whole units and the
  derived velocity loses its ±1-unit quantization noise. Values stay native
  float32 in memory; JSON text is rounded to 3 decimals (lossless for
  eighth-unit coords). The `hgt` no-floor sentinel changes to `-1000000000`.

## 2026-06-13

- **Player view direction & velocity** (schema v31–v32). Every
  native-rate position sample now also carries **where the player is
  looking** — view pitch/yaw kept losslessly as the raw `angle16` wire
  value (`pos.vp`/`vya`, decode `uint16(v)*360/65536`) — and a derived
  per-sample **velocity** vector in units/sec (`pos.vx`/`vy`/`vz`) from a
  central-difference estimator that does not differentiate across
  respawns, map teleporters, or time gaps. The view-layer query API and
  CLI gain opt-in per-channel field codes: `pos` is now strictly x/y/z,
  with `view`, `hgt`, `lq`, and `vel` each requestable on their own
  (served by mvd-api `stream-slice` / `state-at` / `buckets`).

## 2026-06-12

- **Per-sample floor height, airgibs, movers, liquids** (schema v24–v30,
  [#84](https://github.com/galfthan/mvd_analyzer/pull/84)). Every
  position sample now carries the player's height above the floor
  (BSP clip-hull traces, footprint-aware, standing on lifts/doors at
  their demo-streamed poses) and a water/slime/lava submersion state.
  On top of it: the **airgibs** Key Moment — direct enemy rocket hits on
  airborne victims, with lethality, Hub shooter links, and the victim's
  height above the shooter as the headline "how spectacular" number.

## 2026-06-07

- **Wall-clock timing for demos** (schema v23,
  [#82](https://github.com/galfthan/mvd_analyzer/pull/82)). Recovers a
  real-world clock anchor for each demo so any match-relative time maps
  to wall-clock time; pause segments are accounted for in the mapping.
- **Demo-start timestamp decoding**
  ([#83](https://github.com/galfthan/mvd_analyzer/pull/83)). Parses the
  mvdhidden `0x000B` block (ULEB128 Unix-ms) — the millisecond-accurate
  demo-open anchor the wall-clock mapping builds on.

## 2026-06-03

- **Per-hit damage end to end** (schema v20,
  [#81](https://github.com/galfthan/mvd_analyzer/pull/81)). The KTX
  hidden damage stream becomes a full per-hit log with
  attacker→victim matrices, per-weapon aggregates, and EWep
  victim-weapon buckets; telefrags and stomps are surfaced separately.
- **Corrected scoreboard stats** (schema v19,
  [#80](https://github.com/galfthan/mvd_analyzer/pull/80)). Kills,
  deaths and suicides corrected from the frag log for every consumer;
  efficiency is kills-based to match hub.quakeworld.nu.

## 2026-06-02

- **Player timelines**
  ([#79](https://github.com/galfthan/mvd_analyzer/pull/79)). Per-player
  timeline view of vitals, weapons and powerups across the match.

## 2026-05-30

- **Static map-entity corpus + map endpoints** (schema v14,
  [#77](https://github.com/galfthan/mvd_analyzer/pull/77)). Item and
  spawn locations extracted from map BSPs ship embedded, with REST
  endpoints to serve per-map geometry and entities.

## 2026-05-29

- **Schema reference reconciled with code**
  ([#76](https://github.com/galfthan/mvd_analyzer/pull/76)).
  RESULT_SCHEMA.md brought back in lock-step with `result/` after
  drift; version history table became the single change record.

## 2026-05-25

- **Reconnect identity unified** (
  [#75](https://github.com/galfthan/mvd_analyzer/pull/75)). A player
  rejoining mid-match keeps one identity across slots; deaths and
  pickups reconcile against KTX's authoritative counters.

## 2026-05-24

- **Locs & Regions tab**
  ([#74](https://github.com/galfthan/mvd_analyzer/pull/74)).
  Combat-posture loc graphs (armed/unarmed movement between locs) plus
  sortable loc heatmap and region tables in the web UI.

## 2026-05-23

- **Column-major bucket format** (schema v11,
  [#72](https://github.com/galfthan/mvd_analyzer/pull/72)). Bucketed
  timelines ship as columnar arrays; the legacy HighResBucket shape is
  dropped.
- **Web load perf tuning**
  ([#70](https://github.com/galfthan/mvd_analyzer/pull/70)). Profiling,
  deferred bucket builds, and a faster `view.Buckets` cut initial load
  time.
- **Chat dedup on KTX demos**
  ([#68](https://github.com/galfthan/mvd_analyzer/pull/68)). Per-recipient
  copies of the same chat line collapse to one message.

## 2026-05-20

- **Visibility-aware loc attribution** (schema v9–v10,
  [#64](https://github.com/galfthan/mvd_analyzer/pull/64)). Loc
  resolution gains a BSP PVS veto (locvis V6) so positions no longer
  bleed through walls to the nearest loc point; death/spawn handling
  rebuilt on top.
- **API/MCP loc representation**
  ([#65](https://github.com/galfthan/mvd_analyzer/pull/65)). Views
  return loc names by default with an opt-in index mode; analyzer
  errors surface properly through the API.
- **MCP fixes**
  ([#67](https://github.com/galfthan/mvd_analyzer/pull/67)). Array tool
  outputs wrapped for spec compliance; `getItems` filter vocabulary
  corrected.

## 2026-05-16

- **All times become int32 milliseconds** (schema v8,
  [#62](https://github.com/galfthan/mvd_analyzer/pull/62)). Every
  timestamped field migrates from float seconds to the MVD wire
  format's native integer-ms unit, eliminating float drift at
  boundaries.
- **Region control as a normal view**
  ([#63](https://github.com/galfthan/mvd_analyzer/pull/63)). Region
  control re-derives from streams like every other view instead of
  being a parse-time special case.

## 2026-05-15

- **REST API + MCP server** (
  [#61](https://github.com/galfthan/mvd_analyzer/pull/61)). `mvd-api`
  serves analysis over HTTP with a demo cache, and an MCP server
  exposes the same views to AI tooling; repository reorganised into the
  three-module workspace.

## 2026-05-11

- **Streams as canonical storage** (schema v7,
  [#60](https://github.com/galfthan/mvd_analyzer/pull/60)). Per-player
  change streams, intervals, and the native-rate position track replace
  parse-time buckets as the single event-rate source all views derive
  from.

## 2026-05-09

- **Timeline GL/ammo, clean chat text, Go region control** (schema v6,
  [#59](https://github.com/galfthan/mvd_analyzer/pull/59)). Timeline
  gains grenade launcher and ammo tracking, chat messages get a
  markup-stripped `messageClean`, and region control moves from the
  frontend into the Go analyzer.

## 2026-05-08

- **Match in the header**
  ([#57](https://github.com/galfthan/mvd_analyzer/pull/57)). The web UI
  shows the loaded match in the header bar and tab title.
- **Timeline rendering rewrite**
  ([#56](https://github.com/galfthan/mvd_analyzer/pull/56)). Scanline
  rendering fixes resize artifacts and speeds the timeline up.

## 2026-05-07

- **Per-map regions from JSON** (
  [#55](https://github.com/galfthan/mvd_analyzer/pull/55)). Embedded
  per-map region definitions fully replace the auto-detection
  heuristic.

## 2026-05-03

- **Pickups tab**
  ([#54](https://github.com/galfthan/mvd_analyzer/pull/54)). Per-player
  item pickup breakdown in the web UI, with the KTX weapon-pickup
  counter semantics documented.

## 2026-05-02

- **Search tab**
  ([#53](https://github.com/galfthan/mvd_analyzer/pull/53)). Search for
  demos from the web UI, with a reshaped tab layout around it.
