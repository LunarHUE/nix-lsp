# 2026-07-03 — gopls adoption plan: all five waves landed

Executed `docs/gopls-adoption-plan.md` end to end: 10 commits (c3d31c4 →
3c61888), each implemented by an Opus agent from a written spec, reviewed
line-by-line, gated (build/vet/`go test ./...`/`-race ./internal/server/`),
and — where the publish path was touched — smoke-tested over real LSP stdio
with a rebuilt binary (driver asserts the dataset gate AND the new wire
`version` field).

## Commit map

- 2530f70 #9 publish-generation test accounting (de-flake foundation)
- 7c148f1 #8 versioned packages-cache filename (`v2-<channel>.json`)
- c123928 #6 dataset diagnostics/quick-fixes gated on `HasError`
- c229c78 #10-lite typed method constants + capability↔handler drift tests
- 58123fb #4a document-version backstop, `version` on the wire
- 704e6c5 #4b cancellation of superseded computes (`syntax.ParseCtx`)
- bde80d3 #2 `facts.FileInputFor` + `diagnosticInputs` struct
- 1a15233 #1-partial memo eviction + dead VFS generation deleted
- 3c61888 #5 commit 1: bounded fixSrc repair loop + missing-';' fixer

## New lessons (candidates for lessons-building-an-lsp.md — not added there
## because another session holds large uncommitted edits to that file)

1. **Stateless eviction needs a single writer — split the API to enforce it.**
   The first eviction design swept superseded FileIDs inside `SetFileInput`,
   which every registration path calls. A hover/completion request holding a
   slightly OLDER snapshot could therefore evict the NEWER fileID's input out
   from under a mid-flight diagnostics compute: `FileDiagnostics` errors with
   `ErrNoQuery`, the error is swallowed, and that edit's publish is lost with
   nothing to re-trigger it. Fix: `SetFileInput`/`FileInputFor` never evict
   (features); `SupersedeFileInput` registers-and-sweeps and is called ONLY by
   the diagnostics coalescer, the single writer per path. Boundedness survives
   because every keystroke flows through the coalescer and the prefix sweep is
   self-healing. Regression test: `TestFeatureRegistrationDoesNotEvictNewerFileID`.

2. **Read waited-on state and its broadcast channel under ONE lock hold.**
   Two of the new wait helpers checked state via one lock acquisition, then
   grabbed the notification channel via a second. A publish landing between
   the two closes the OLD channel and parks the waiter on the NEW one — a
   missed wakeup that turns an already-satisfied wait into a spurious 5s
   timeout. The de-flaking pattern is: `RLock; state := ...; ch := broadcast;
   RUnlock; if satisfied return; select { <-ch; <-deadline }`.

3. **When deleting a settling sleep, ask what regression the sleep was
   catching.** An agent replaced "sleep 100ms then assert nothing recomputed"
   with an immediate assert, on the correct grounds that the handler is
   synchronous — but the sleep existed to catch a regression where the handler
   WRONGLY schedules async work. Asserting immediately would pass right through
   that regression. Replacement must be a deterministic fence (drain the
   coalescer), not deletion.

4. **Key a source-repair fixer on the classifier's own output, not a re-derived
   shape.** The missing-';' fixer reads the original tree's `Diagnostics()` and
   uses the classified anchor (the zero-width range start IS the insertion
   point), recognized via the shared message constant. Fixer and classifier
   share one anchor computation, so they cannot drift — the same adjacency
   principle as #2/#8, applied to repair.

## Deferred (next session picks up here)

- Repair-loop later commits: trailing-dot fixer (kills the completioncontext
  byte-rescanners), unclosed-quote/bracket fixers, server wiring so enrichment
  consumes the memoized `facts.RepairedParseTree` instead of calling
  `syntax.RepairParse` per compute (unmemoized today — up to ~2 extra parses
  per keystroke on a broken buffer; acceptable, but the fact exists and is
  tested), then relax #6's gate to "dataset diagnostics on the repaired tree;
  suppress only when repair failed".
- `#3`/`#10` doc notes into lessons-building-an-lsp.md §14.2/§14.10 once that
  file is free.
- `handledMethods` in handler.go mirrors the dispatch switch by convention; a
  brand-new UNADVERTISED case missing from the slice evades both drift tests.
  Acceptable for -lite; a map-driven dispatch would close it.
- Known superlinear diagnostics-compute perf bug: still its own investigation;
  #4b's cancellation only stops the bleeding on stale computes.
