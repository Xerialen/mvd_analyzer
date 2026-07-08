# decisions/testdata

Fixtures for `decisions/kdlog_golden_test.go` (`TestGoldenServerLog`).

## golden-server.log

A verbatim excerpt of a **real** mvdsv + KTX `server.log` from a
`kbot-0.28.0-dials` run (2026-07-06) on the `komodobots2-bv1` bench — not
hand-typed, every byte from an actual server run.

The same KDLOG lines as KomodoBench's `tests/fixtures/golden-server.log`,
from the same run — both repos share the fixture by design. Line endings are
normalized to LF here (SHA256 `2b15992b8f3646156e0d3edb7f79115156ad56a41f27f5fcbb2619016cf091e5`);
KomodoBench's copy is still CRLF, so its bytes differ only by the `\r` — a
cosmetic follow-up on that side.

## Why it exists

Fed through `ResolveKDLog` (the `-decision-log` consumer) to pin the KDLOG
**emit format** produced by the KTX C brain: the `KDLOG_ANCHOR`
(`emitter=/dlog=`) line and the goal/enemy/evade record grammars.
`TestResolveKDLog` only proves parse inverts a Go-authored emitter and is
blind to drift in the C brain's `snprintf` format — this fixture pins real
brain output, so a C format change breaks a test instead of silently dropping
records at analysis time.

## Cross-repo ownership

The KDLOG grammar is pinned where each part is actually consumed:

- **Here:** anchor + goal/enemy/evade (no KomodoBench consumer).
- **KomodoBench:** play/dial/weap/harvest (`dial_report.LINE`).

Shared fixture bytes → no grammar left unpinned.

## Updating

After an intentional C brain emit change:

1. Re-copy a fresh, byte-verbatim real `server.log` excerpt over this file.
2. Update `goldenFixtureResult()` / assertions in `kdlog_golden_test.go`.
3. Keep in sync with KomodoBench's copy.
