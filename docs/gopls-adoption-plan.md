# Adopting the gopls audit — per-change plan for nixls

Companion to `lessons-building-an-lsp.md` §14.10, which ranked ten corrections
gopls would make to the nixls playbook. This document maps each one onto the
actual nixls code (file:line receipts from a five-agent sweep of the tree,
2026-07-03), decides adopt / adapt / skip, and sequences the adopted ones into
waves. Verdict summary:

| # | Change (§14.10) | Verdict | Effort |
|---|---|---|---|
| 1 | Immutable snapshots + persistent maps | **Partial** — fix the real defects (unbounded memo growth, redundant generation systems); full conversion is rebuild-scale, deferred | M |
| 2 | Named key-input struct + adjacent hasher | **Adopt** | S–M |
| 3 | Don't read external state you can get as an event | **Skip (doc-only)** — doesn't transfer; git-trackedness is a genuine analysis input here | — |
| 4 | Version-stamp every publish AND cancel stale work | **Adopt** — both layers missing today | M |
| 5 | Synthetic-node repair for broken buffers | **Adopt (fixSrc form)** — biggest code-quality win; collapses most shape-matchers | L |
| 6 | Suppression as an alternative to repositioning | **Adapt** — suppress dataset diagnostics on parse errors, keep syntax-error enrichment | S |
| 7 | Certainty from the type system vs only-warn-if-fixable | **Skip** — §14.7 itself says the fix-proxy rule stays right for a schema-driven server | — |
| 8 | Version derived caches by binary hash / format version | **Adopt (format-version form)** — packages trimmed cache silently misparses today | S |
| 9 | Settle tests by completion accounting, not quiescence | **Adopt** | S–M |
| 10 | Generate protocol from the LSP metaModel | **Adapt** — codegen not worth it at ~24 methods; typed method table + exhaustive capability↔handler tests instead | S |

Wave order (each wave = independent commits, verification gate per commit):

- **Wave 1 — cheap wins that de-flake everything after them:** #9, #8, #10-lite, #6.
- **Wave 2 — the correctness layers:** #4 (version stamping, then cancellation).
- **Wave 3 — key hygiene:** #2, plus the #1-partial memo eviction.
- **Wave 4 — the big one:** #5 repair loop, then revisit #6's gate ("suppress
  only when repair failed").

---

## #9 — Completion accounting for tests (Wave 1, first)

**Where we went wrong.** Tests settle by polling and sleeping.
`waitForDiagnostics` (`internal/server/handler_test.go:671-683`) polls every
10ms and, on its silent 1s deadline, **returns the last state without
failing** — a `want: 0` assertion passes vacuously if diagnostics simply
haven't computed yet (this trap already burned us once, §10 of the lessons
doc). Bare sleeps: `staleness_test.go:59,72(1.5s!),113`,
`datadiag_test.go:53,194,249`, `workspace_test.go:246`, `freeze_test.go:31`.
The timings being out-waited are the publisher's debounce (150ms), rate
(5ms/send), and 25ms flush ticker (`publisher.go:14-15,73`).

**What exists already.** The workspace-indexing pass has real begin/report/end
progress (`handler.go:365-402`, tested by `progress_test.go:121-181` with
`captureCaller`/`progressNotifier`) — a working prototype of the accounting.
`waitForPublish` (`handler_test.go:685-698`) and `waitForProgressEnd`
(`progress_test.go:106-119`) already fail loudly; they are the model.

**Plan.**
1. In-process signal: a per-URI "published at generation ≥ G" wait. The
   handler already has `generation`/`diagGeneration` (`handler.go:44-51,
   857-862`); add a condvar/channel signaled where the store+publish happens
   (`computeFileDiagnostics`, `handler.go:844-853`). Test helper blocks until
   the URI reaches a target generation, with a loud `t.Fatalf` timeout.
2. Make `waitForDiagnostics` fail loudly (or replace it outright with the
   generation wait) and migrate every sleep site listed above.
3. Optional, rides with #4: a custom `nixls/didFinishDiagnostics {uri,
   version, generation}` notification emitted from `publisher.send`
   (`publisher.go:155-167`) so future real-binary smoke drivers can settle by
   accounting too instead of the quiescence loops the scratch drivers use.

**Why first:** every later wave's regression tests become deterministic
instead of sleep-tuned.

---

## #8 — Version the packages trimmed cache (Wave 1)

**Where we went wrong.** The packages dataset persists a nixls-defined
derived format — `MarshalTrimmed`/`ParseTrimmed`
(`internal/analysis/packages/packages.go:252-288`) written to
`<cache>/nixls/packages/<channel>.json` (`packagesindex.go:117-122`) — with
**no format version anywhere**. A binary upgrade that *adds or renames* a
`trimmedDoc` field decodes old cache files without error (JSON is lenient)
and serves silently wrong/blank data for up to the 7-day TTL. (Type changes
self-heal: `ParseTrimmed` errors → `publishPackagesFromCache` returns false →
re-download, `packagesindex.go:124-134,167-170`.) The options cache is immune
— it stores the raw upstream `options.json` and re-parses every load
(`optionsindex.go:146-151,169`).

**Plan.** gopls's binary-hash namespace is overkill at our scale; a format
constant does the same job with zero machinery:
1. `const trimmedFormatVersion = 2` in `packages.go`, embedded in the cache
   filename (`packages/v2-<channel>.json`) — an old-format file is simply a
   cache miss, and stale files age out of the directory. Bump on any
   `trimmedDoc` change; note the bump rule in a comment adjacent to the
   struct (same adjacency principle as #2).
2. Test: write a v1-shaped file at the v1 path, assert the loader treats it
   as a miss and re-downloads (fixture-mode).
3. Optionally version the options filename for consistency; not required.

---

## #10-lite — Typed method table + capability↔handler exhaustive tests (Wave 1)

**Where we went wrong (and where we didn't).** No codegen exists; dispatch is
one hand-written ~24-case switch (`handler.go:188-248`) over ~60 hand-trimmed
structs. That scale doesn't justify the metaModel pipeline gopls needs for
hundreds of full-fidelity types. The *actual* drift surface found:
- Method names are bare string literals duplicated across the dispatch
  switch, the notification-swallow list (`internal/lsp/server.go:245`), and
  every test.
- Nothing enforces "advertised capability ⇒ wired handler" or the reverse
  (`ServerCapabilities` at `server.go:359-371` vs the switch) — the likeliest
  real drift bug.
- Known wire-fidelity gaps: `publishDiagnosticsParams` lacks `version`
  (`handler.go:923-926`, fixed by #4); `protocolDiagnostic` lacks
  `tags`/`relatedInformation`/`data` and `code` is string-only
  (`handler.go:928-934`) — adopt only when a feature needs them;
  `versionedTextDocumentIdentifier.Version` uses `omitempty` and silently
  drops version 0 (`handler.go:880-883`) — fix alongside #4.

**Plan.**
1. A `methods.go` with typed constants for every method string; replace all
   literals (switch, swallow-list, tests).
2. Two exhaustive tests: (a) every capability advertised in
   `ServerCapabilities` has a switch case; (b) every switch case is either
   advertised or on an explicit intentionally-unadvertised list
   (notifications, lifecycle).
3. Record in the lessons doc that full metaModel codegen is the >50-method
   answer; revisit only if nixls's surface grows toward that.

---

## #6 — Suppress the dataset cascade on parse errors (Wave 1)

**Where we went wrong.** Dataset diagnostics run on broken trees.
`datasetDiagnostics` (`internal/server/datadiag.go:24-40`) guards only on
`tree == nil`, never `HasError`; `gatherModuleBindings`
(`datadiag/options.go:71-108`) then walks whatever survived recovery. A
garbled recovery (e.g. the swallowed-binding shape reparenting a name) can
compose a wrong option path and emit a spurious `unknown-option` /
`option-type-mismatch` — exactly the cascade gopls kills by suppressing all
type diagnostics in unparsable files (`check.go:1694`). The near-miss gate
limits but does not eliminate it.

**What NOT to suppress.** `enrichSyntaxDiagnostics`
(`datadiag/syntaxcontext.go:29-101`) — the shipped "missing ';' … <path>
accepts options like …" guidance — only *rewords parser-reported errors*
(zero false-positive risk per §5's enrich-don't-conjure rule) and is a
user-visible feature we deliberately built. Keep it. This is the deliberate
divergence from gopls's blanket suppression: enrichment of the syntax error
itself is not a cascade.

**Plan.**
1. In `computeFileDiagnostics` (`handler.go:832`), gate the
   `datasetDiagnostics` append on `!tree.Root().HasError()` (accessor exists,
   `syntax/tree.go:544`). Line 833's enrichment stays unconditional.
   (Equivalent placement: inside `datasetDiagnostics` itself at
   `datadiag.go:30` — pick handler-level so the policy sits where diagnostics
   are assembled.)
2. Same gate on `datasetCodeActions` (`datadiag.go:76`) so fixes can't appear
   for suppressed warnings.
3. Tests to update: `TestDatasetSyntaxErrorOptionGuidancePublished`
   (`server/datadiag_test.go:200-217`) — split: still asserts enrichment
   publishes, now asserts unknown-option/type-mismatch do NOT on a broken
   buffer. The `datadiag/syntaxcontext_test.go` suite stays (enrichment
   survives). Multi-error syntax reporting is unpinned by any test, and we
   keep it — repositioned precise errors are our strategy; suppression here
   applies only to the *dataset* layer.
4. Revisit after #5: once a repair pass exists, relax to "run dataset
   diagnostics on the *repaired* tree; suppress only when repair failed."

**Not adopting** gopls's first-error-only reporting: our anchored, precise
multi-error messages (missing-';' family) are a feature users responded to,
and dedupe (`dropShadowedGenerics`, `tree.go:211-236`) already handles the
noise case.

---

## #4 — Version-stamp every publish, then cancel superseded work (Wave 2)

**Where we went wrong.** nixls has exactly one staleness layer — server-minted
generations — where gopls runs three (generation-order, version-verify at
publish, cancellation). Receipts:
- The LSP document version is **decoded and discarded** in both `didOpen`
  (`handler.go:404-418`, `textDocumentItem.Version` at 873-878) and
  `didChange` (`handler.go:420-441`, `versionedTextDocumentIdentifier` at
  880-883). Neither the VFS `overlayFile` (`vfs/vfs.go:36-39`) nor any
  handler map stores it.
- `publishDiagnosticsParams` (`handler.go:923-926`) has no `version` field —
  nothing version-like ever goes out on the wire.
- No context is ever cancelled on a newer edit: the coalescer's `diagEntry`
  (`diagcoalesce.go:39-44`) holds no cancel; `schedule` only sets `dirty`
  (`:67-68`) and stale computes run to completion; `enqueueBackground`
  hardcodes `context.Background()` (`handler.go:146-152`); and the leaf
  parse ignores ctx entirely — `syntax.Parse` calls
  `parser.ParseCtx(context.Background(), …)` (`tree.go:76`).

Generation guards already make late results *safe* (drop at
`handler.go:840` and `publisher.go:90`), so both additions are independent
layers: version-verify is a correctness backstop that holds even if someone
later violates generation discipline; cancellation is a liveness/work-saving
layer (directly relevant to the known superlinear-compute perf bug — a stale
multi-second compute currently runs to completion).

**Plan — step 1, version stamping (correctness):**
1. Add `version int32` to the VFS overlay: `OpenBuffer`/`UpdateBuffer` take
   it (`vfs.go:65-72`), `overlayFile` stores it, `Snapshot.ReadFile` /
   a new accessor exposes it. didOpen/didChange stop discarding it. Drop the
   `omitempty` on the decode side so version 0 survives.
2. Thread it through the compute: `computeFileDiagnostics` records the
   version of the content it read; store it beside `diagGeneration`; carry it
   in `diagnosticUpdate` (`publisher.go:18-23`).
3. Verify at publish: in `publisher.send` (or `accept`), drop the update if
   the handler's current version for that URI has moved past the one the
   compute read; emit `version` in `publishDiagnosticsParams`. (Non-overlay
   files have no version — guard applies to open documents only, which is
   exactly where the race class lives.)
4. Regression test: the staleness_test scenarios re-asserted through the
   version layer with generations deliberately mis-ordered, proving the
   backstop holds independently.

**Plan — step 2, cancellation (liveness):**
1. `diagEntry` gains a `cancel context.CancelFunc`; `runLoop` derives a
   cancellable child ctx per iteration; `schedule` cancels the in-flight ctx
   when marking dirty. The scheduler already honors caller ctx
   (`scheduler.go:98-141,241-249`) — only `enqueueBackground`'s hardcoded
   `context.Background()` changes.
2. Make cancellation bite: `syntax.Parse` honors the passed ctx
   (`tree.go:76`), and the memo engine already checks `ctx.Err()` per query
   (`engine.go:140`), so cancellation takes effect at least at query
   boundaries; add coarse ctx checks in the expensive walks
   (static/datadiag) opportunistically.
3. This is purely work-saving — correctness is already covered by
   generations + (new) versions — so land it as its own commit with a test
   that a superseded compute observes cancellation.

---

## #2 — Name the key-input set (Wave 3)

**Where we went wrong.** The true input set of a *published* diagnostic set is
`{path, content-hash, workspace/git-tracked set, git-index token, flake.lock
bytes, options-index identity, packages-index identity}` — assembled across
`computeFileDiagnostics` + `datasetDiagnostics` + `enrichSyntaxDiagnostics`
plus three singleton `SetInput`s and two atomic pointers, **never named as one
struct**. Six call sites independently build the `{FileID, SetFileInput}`
tuple: `handler.go:817-821`, `features.go:413-414`, `features.go:535-539`
(`fileInputForURI`, the closest to a shared constructor), `features.go:699-700`,
`optionhover.go:66-67`, `flakehover.go:108-109`. gopls's discipline
(`typeCheckInputs` adjacent to its hasher, check.go:1460/1544) makes
"reads an input but forgot to key it" a visible diff; ours is invisible.

**Plan.**
1. One constructor: fold the six sites into a single
   `facts.FileInputFor(snapshot, path) (fileID, error)` (promote
   `fileInputForURI`), so key construction has exactly one spelling.
2. A named `diagnosticInputs` struct in `internal/server` listing every input
   the publish path reads — path, hash, workspace snapshot identity, git
   token, flake.lock hash, options/packages index identities — built at the
   top of `computeFileDiagnostics` and passed down. The struct is
   documentation-as-code: adding a read without adding a field is now a
   reviewable diff. The memo engine's dep-edges keep doing the actual
   invalidation; the struct doesn't need to become a hash key to pay for
   itself.
3. Comment adjacency rule (same as #8): the struct definition sits directly
   above the code that consumes it, with the "if the computation reads it,
   it lives here" law from §2 quoted.

---

## #1-partial — Fix the mutable-cache defects without the rebuild (Wave 3)

**Honest scoping.** The full gopls model — derived state inside immutable,
refcounted, structural-sharing snapshots — is the right *day-one* choice and
is recorded as such for the Terraform LSP (§12, §14.10-1). Retrofitting it
into nixls means rewriting the memo engine, the facts layer, and every
feature entry point; that is rebuild-scale work that buys correctness we now
already have via the single-writer + stamp-before-read + (Wave 2) version
layers. Not worth it here. What IS worth fixing are the two concrete defects
the sweep found:

1. **Unbounded memo growth.** Every edit mints a new `FileID(path, hash)` key
   and the old entry is never evicted — no GC, no LRU, nothing
   (`memo/engine.go:36-44`, `facts.go:56`). `entries` grows for the life of
   the session; a long editing session on a large file leaks every keystroke's
   parse tree. Fix: per-path last-key tracking in the facts layer — when
   `SetFileInput` sees a new hash for a path, drop the superseded FileID's
   entries (the engine gains a `Forget(prefixOrKeys)`). Cheap, and the
   single-writer coalescer guarantees no live reader holds the old key by the
   time the new input is set for that path. Test: recompute counters +
   entry-count assertions across an edit burst.
2. **Three overlapping generation systems.** `Store.generation`
   (`vfs.go:47`) is captured into every snapshot/file and **used by nothing**
   — the server runs its own `h.generation` plus the publisher's `latest`
   map. Either delete the VFS generation (less state to misread) or — better,
   rides with #4 — make the VFS generation the one ordering token the handler
   uses, deleting `h.generation`. Decide at Wave 2 implementation time;
   default to deletion if unification complicates #4.
3. The VFS snapshot itself is already immutable (deep-copied,
   `vfs.go:93-105`) — no change; note the eager copy is O(open buffers) and
   fine at our scale.

---

## #5 — fixSrc repair loop for broken buffers (Wave 4)

**Where we went wrong.** Instead of repairing the tree once, every feature
pattern-matches raw recovery shapes independently. The sweep's inventory: 16
shape-matchers, with "missing ';'" recognized **four** ways
(`syntax/tree.go:141-152, 272-333, 441-446, 452-457`) and re-derived a fifth
time downstream (`datadiag/syntaxcontext.go:79-135`); "trailing dot"
reconstructed **three** ways with three mechanisms
(`scopes/completioncontext.go:134-163, 225-235+659-741` byte-rescanners,
`359-438` pure byte-scan); "am I in an option-binding value" computed
independently in four places. This is the maintenance bill gopls avoids with
its bounded fixAST/fixSrc loop (`parsego/parse.go:82`).

**Constraint.** tree-sitter trees are immutable and `Reparse` is a stub doing
a full parse (`tree.go:85-87`) — so only the **fixSrc** half is available:
rewrite source bytes, re-parse, bounded iterations. Affordable: ≤10 full
single-file parses of small Nix modules, memoized once.

**Plan.**
1. New memo query `QueryRepairedParseTree` depending on `QueryParseTree`
   (`facts.go:237-243` is the choke point; all features already read through
   `getParseTree`). If `Root().HasError()`, run a bounded (≤10) loop:
   classify → rewrite bytes → `syntax.Parse` → re-check. Caches under the
   *original* FileID (repaired bytes are internal, not a new input), so no
   key explosion.
2. Taint flag, gopls-style (`File.Fixed()`): the repaired tree carries
   `Repaired bool` (+ the edit list) so features can distrust repaired
   positions — diagnostics keep coming from the ORIGINAL tree (repair must
   never move or invent errors); repaired trees serve completion, hover
   context, and (post-#6-revisit) dataset analysis.
3. Repair catalogue, by leverage (each with golden broken-input → repaired
   fixtures, per gopls's parse_test.go pattern; per §5's standing rule, dump
   the real recovery for each BEFORE coding the fixer):
   - **Insert missing `;`** at the anchor the existing classifiers already
     compute — collapses matchers at `tree.go:272-333, 441-446, 452-457` and
     simplifies `syntaxcontext.go`'s re-walk into a well-formed traversal.
   - **Synthetic identifier after a trailing dot** (`pkgs.` →
     `pkgs.__nixls__`) — kills `classifyFlattened`/`gatherFlatSegments` and
     both byte-rescanners (`completioncontext.go:659-741`); completion still
     derives the partial token/dot from the raw bytes (`:116-128`), so this
     simplifies path reconstruction, not the cursor-token scan.
   - **Close an unclosed quote** — collapses the ERROR branch of
     `valueStringContext` (`:410-417`).
   - **Close unclosed `{`/`[`/`(` at EOF** — unblocks scope/dataset analysis
     inside the region (`tree.go:339-356` stays as the *diagnostic*; the
     repair is for downstream consumers).
   - NOT `{ name }` (lone attribute): genuinely ambiguous (binding vs formal)
     — stays a classifier, per the never-wrong-token rule.
4. Migration is incremental: land the loop + one fixer (missing `;`), port
   consumers, delete the dead matchers, then add fixers one commit each.
   Every deleted matcher's tests become fixtures for the fixer.
5. After the trailing-dot fixer lands, revisit #6: dataset diagnostics may
   run on repaired trees (suppress only when repair failed), restoring
   dataset coverage on mid-edit buffers with the cascade risk removed.

---

## #3 — Git state: recorded divergence, no code change

**Why the lesson doesn't transfer.** gopls never needs VCS state as an
*analysis input* — the on-disk file set is its ground truth, so it can let
watcher events carry all the news. nixls's `untracked-import` diagnostic
exists because a `git+file:` flake only sees *tracked* files — trackedness is
first-class analysis input, and no file event can tell you *which* files
became tracked; only `git ls-files` can (`project.go:219-240`). The event can
be the trigger; the git read is unavoidable as the input.

**The narrow candidate we're declining.** The per-recompute `.git/index` stat
(`refreshGitState` at `handler.go:805` via `gitIndexToken`,
`handler.go:684-699`) is the one "proactive read of event-carriable state."
Deleting it would rely solely on the `**/.git/index` watcher
(`extension.ts:86-90` → `handler.go:526-534` → `refreshWatchedFiles`, which
already re-discovers AND refreshes the token, `handler.go:580,601`). Declined
because: the stat costs microseconds, and it is what makes the server
self-heal on the next keystroke under *any* LSP client — a non-VS-Code client
with no `.git/index` watcher would otherwise regress to "warning lingers
until restart-ish." Portability beats purity at this price.

**Action:** none in code. Add the reconciliation above to
`lessons-building-an-lsp.md` §14.2-L2c so the divergence is a decision, not
an oversight.

---

## #7 — Only-warn-when-you-can-fix: stays

§14.7-L7b's own conclusion: gopls affords fix-less warnings because the type
checker supplies certainty; a schema-driven server's certainty proxy IS the
near-miss suggestion. No change; the rule remains load-bearing for
`unknown-option`/`unknown-package` (`datadiag/options.go:154-198`).

---

## Execution notes

- Standard workflow: Fable specs and reviews, Opus implements, one commit per
  change, full verification gate per commit (`nix develop --command go build
  ./... && go vet ./... && go test ./... -count=1 && go test -race
  ./internal/server/`).
- Wave 1's four items are file-disjoint and can run as parallel agents:
  #9 (test helpers + handler signal), #8 (packages cache), #10-lite
  (methods.go + tests), #6 (datadiag gate). #4/#2/#5 are sequential — they
  share handler.go/facts.go.
- The known superlinear diagnostics-compute perf bug (thousands of bindings)
  is intertwined with #4's cancellation (a stale slow compute is the pain
  amplifier) but is its own investigation — not scheduled here.
