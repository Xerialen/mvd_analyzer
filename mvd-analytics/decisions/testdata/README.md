# decisions/testdata

Fixtures for `decisions/kdlog_golden_test.go` (`TestGoldenServerLog`).

## golden-server.log

A verbatim excerpt of a **real** mvdsv + KTX `server.log` from a
`kbot-0.28.0-dials` run (2026-07-06) on the `komodobots2-bv1` bench — not
hand-typed, every byte from an actual server run.

Byte-identical to KomodoBench's `tests/fixtures/golden-server.log`
(SHA256 `3fdfa752588972d68d64b7fb6fe9170be2a9b87c539e088ae84a61b0886cdebe`);
both repos share the same fixture bytes by design.

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
