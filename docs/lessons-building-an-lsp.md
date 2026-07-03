# Lessons from Building nixls — Field Notes for the Next LSP

Everything learned building this language server across 72 commits, distilled
so the same mistakes are never paid for twice. Written with the next target in
mind (a Terraform LSP); §12 maps each lesson onto Terraform directly. Every
lesson here was earned — each cites the real incident that taught it.

---

## 1. Architecture: the layering that made everything else possible

**Pure analysis packages under a thin protocol layer.** Every analysis
(`scopes`, `imports`, `options`, `packages`, `datadiag`) is a pure function of
a parsed tree plus explicit inputs — no filesystem, no network, no protocol
types, no globals. The `server` package owns all LSP JSON, all I/O, all
caching. This paid off constantly: pure packages are trivially table-testable,
agents/contributors could work them in parallel without touching the server,
and every feature (hover, completion, diagnostics, code actions) reuses the
same analysis functions instead of reimplementing them per protocol method.

**One diagnostics pipeline, no ad hoc publishing.** All diagnostics flow
through a single owned publisher goroutine with per-URI debouncing. Every time
a feature needed to publish (didChange, watched files, dataset loads), it
joined the existing path instead of spawning its own goroutine. The two times
staleness bugs appeared anyway (§3), the single pipeline made them diagnosable.

**A memoization engine with automatic dependency tracking.** Queries call
`Context.Get(otherQuery)` and the engine records the edge; invalidating an
input dirties dependents lazily. This is the salsa/rust-analyzer model in
miniature and it is worth building on day one — but see §2 for the two ways
it will silently betray you.

**Full-document text sync.** `TextDocumentSync: 1` (client resends the whole
document per change) is dramatically simpler than incremental sync and was
never the bottleneck. Keep incremental behind an API seam (`Reparse(tree,
edits, content)` that does a full reparse internally) so you can upgrade
later without touching callers. Never needed to upgrade.

**Design for testability as user-facing configuration.** The dataset override
settings (`optionsPath`, `packagesPath` via `initializationOptions`) were
designed as the *test hook* first — fixtures load through the exact code path
users' overrides use. One seam, two purposes, zero test-only code in
production paths.

---

## 2. Caching: every staleness bug was a cache-key bug

The single most repeated class of bug. A memo/cache serves exactly what its
key describes — anything real that is not in the key WILL be served stale.

**Incident: content-hash-only keys collided across files.** The original plan
keyed file facts purely on content hash. Two files with byte-identical content
(ubiquitous boilerplate — many `default.nix` containing `import ./foo.nix`)
shared one memo entry whose stored *path* flip-flopped with evaluation order,
resolving relative imports against the wrong directory, nondeterministically.
Fix: composite key `path + "\x00" + contentHash`. Lesson: if the computation
reads it (the path, for relative resolution), the key must contain it. "Pure
function of content" is only true if it is actually a function of content
alone.

**Incident: git state was invisible to the cache.** The untracked-import
warning persisted after the user ran `git add` + commit + push in a terminal.
Two stacked causes: (a) the file watcher covered `**/*.nix` only — git touches
`.git/index`, which fired nothing; (b) worse, git-trackedness was consumed
*inside* a memoized query keyed on content+workspace, so even a recompute
served the cached `GitTracked:false` edge. Fix: a git-state token (stat of
`.git/index`, mtime+size) became an explicit memo input, refreshed at
discovery, on watcher events, and cheaply per recompute; plus the extension
watches `**/.git/index`. Lesson generalized: **enumerate every external state
your analysis reads (VCS state, lock files, disk mtimes, downloaded datasets)
and make each one either a memo input or an event source — ideally both.** An
input without an event means staleness until the next unrelated recompute; an
event without an input means the recompute reads the same stale cache.

**Dataset-dependent results must not live in content-keyed caches.** Dataset
diagnostics (unknown-option etc.) depend on *index identity*, not file
content, so they are computed at publish time and appended to the memoized
static set — never memoized themselves. When an async dataset finishes
loading, an explicit hook re-publishes all open files. Without that hook, a
file opened before the download finished never gains its dataset features.

---

## 3. Concurrency: the bugs that only a restart "fixed"

**The worst bug in the project: a blocking enqueue on the read loop.**
Notifications (didChange, didOpen, watched-files) were dispatched
*synchronously on the LSP read loop* — correct and common. But the task
scheduler's `Submit` blocked when its bounded queue was full. Two busy workers
+ 64 queued tasks + one keystroke = the read loop blocks = the server stops
processing EVERY message, with the last-published (mid-keystroke, broken)
diagnostics frozen on screen until restart. The user experienced it as
"random syntax errors that never go away unless I restart the LSP."
Rules extracted:
- **Nothing on the read loop may ever block.** Notification handlers must use
  non-blocking enqueue with an explicit overflow policy per call site
  (drop-and-log is fine when something re-arms the work later).
- **Coalesce recomputes whose input is "latest state".** Diagnostics only ever
  need the newest buffer: per-URI single-in-flight + dirty-bit (finisher
  re-runs once if dirtied) bounds queue pressure *structurally* — by open-file
  count, not keystroke rate — and guarantees convergence to the final content.
  Enqueue-per-keystroke is both wasteful and, under a bounded queue, dangerous.
- Prove wedges with a red test first: park the workers on a channel, fill the
  queue, assert the notification handler returns anyway.

**Generation guards, on EVERY cache the results touch.** The publisher
guarded sends by generation, but the handler's own `diagnostics[uri]` map did
not — so when two computes for one file ran on the two workers and the older
finished last, it overwrote the newer. Flaky under `-count=3` from day one.
The guard must cover every store, and `didClose` must record a generation too
(else a stale in-flight compute resurrects diagnostics for a closed file).

**Notifications have no reply channel — never let them kill anything.** VS
Code sent `$/setTrace`; the handler returned method-not-found; the transport
treated a notification-handler error as fatal; the server died; the client
restarted it 5 times and gave up. Per spec: unknown `$/` notifications are
ignored; ALL notification handler errors should be logged and swallowed
(there is nowhere to send them). Same for malformed `$/cancelRequest`.
Corollary trap created by this very fix: once notification errors are
swallowed, a silently failing `didChange` means a silently stale overlay —
log loudly.

**Requests run on their own goroutines and race notifications.** A test
driver (or misbehaving client) that pipes `initialize`+`didOpen`+`hover` in
one burst can see the hover execute before initialize's handler finished its
synchronous work, or after a later didChange. Real clients await responses;
your tests must too (§10).

---

## 4. The transport layer: small, boring, and full of landmines

- **The `--stdio` flag convention.** vscode-languageclient appends `--stdio`
  to executables when you specify `transport: TransportKind.stdio`. A Go
  `flag`-based server exits code 2 on the unknown flag → instant crash loop on
  activation. Either omit `transport` (stdio is the default for executables)
  AND accept a no-op `--stdio` flag server-side — other editors pass it too.
- **Content-Length is BYTES, not characters.** An em dash in a file broke a
  hand-rolled test driver counting `${#var}` in bash. Every driver and every
  frame writer must count bytes.
- **Return LSP null, never errors, for "no answer".** Hover/definition/
  completion misses are `result: null`. Reserve JSON-RPC errors for protocol
  violations. Unknown *requests* get `-32601`; unknown *notifications* get
  silence (§3).
- Route server→client requests (`window/workDoneProgress/create`) through a
  pending-response map with timeouts — a Call that waits forever is a parked
  worker (we bounded ours at 5s; that discipline later mattered in the wedge
  audit).

---

## 5. Tree-sitter (or any error-recovering parser): design from real recoveries

**Dump real parses of broken input BEFORE designing any mid-edit feature.**
This was made a standing rule after repeated surprises. Completion fires
mostly on syntactically broken buffers, and error recovery is *shape-diverse*:
a trailing dot after `pkgs` yields a clean select + sibling `ERROR "."`; the
same dot in a binding attrpath collapses the whole binding to an `ERROR`
holding bare identifiers; two levels deep the attrpath *survives inside* the
ERROR with the dot as a sibling; nested bindings flatten INTO the same ERROR
across `= {` separators. Four+ distinct shapes for one keystroke pattern. The
feature that assumed one shape worked in tests and failed for real users
("completion does nothing after `networking.firewall.`").

**Anonymous and MISSING nodes are invisible to a named-node walk.** The
single most user-hostile parser behavior: a missing `;` after a binding is
represented as an anonymous zero-width `MISSING ";"` token — a named-only
diagnostics walk reports NOTHING for the most common typo in the language.
Surface MISSING tokens explicitly: their kind IS the expected token, giving
mechanically precise `expected ';'` / `expected '}'` messages with zero
heuristics.

**Anchor errors where the fix belongs, not where recovery exploded.** Deleting
a `;` after `pkgs = import nixpkgs {...}` made the parser swallow the *next*
binding's name into a function application and error on the line below. Users
read that as "the wrong line is broken." Classify the recovery shape and
anchor a zero-width diagnostic at the position the missing token belongs,
naming the swallowed binding when provable.

**The never-wrong-token rule.** A hint that names the wrong thing is worse
than a generic message. Only emit a name/token in a message when the parse
tree *proves* it (full-token check: the identifier's adjacent source bytes are
not identifier characters). We shipped a hint that said `attribute 'wg'` for
a buffer containing `wg0` — it turned out to be a stale-keystroke correct
answer, but the audit produced the rule and it later prevented real ones.
Related: **enrich existing errors, never conjure new ones** — message
rewording of parser-reported errors carries zero false-positive risk;
inventing diagnostics from heuristics carries all of it.

**Grammar/binding mechanics:** vendor the grammar with its revision recorded;
keep the cgo bridge in ONE package so everything else depends on your syntax
API; verify every node-kind string and field name against the grammar's
`node-types.json` empirically before coding (standing rule that caught
mistakes repeatedly); expose `IsMissing`/anonymous-children accessors early.

---

## 6. Schema-driven language intelligence: the highest-leverage decision

The defining insight of this project: **don't evaluate and don't parse doc
sources — find the ecosystem's compiled schema artifact.** For NixOS that is
`options.json` (24k options: description/type/default/example/declarations)
and `packages.json` (145k packages: name/version/description), published per
channel. One dataset powered hover, completion, typo diagnostics, type
checks, enum validation, and value completion — all static, no `nix`
execution ever.

Hard-won sub-lessons:

- **Membership-is-the-filter beats context heuristics.** "Is this attrset a
  NixOS module?" is unanswerable syntactically; "does this attrpath exist in
  the options schema?" is a lookup. Wrong-context false positives largely
  vanish when the schema is the gate.
- **The schema is the set of DOCUMENTED things, not VALID things.** A real
  option (`system.disableInstallerTools`) was absent because it is declared
  `internal`/invisible. Any "unknown X" diagnostic keyed on schema membership
  WILL false-positive on hidden-but-valid names. The fix that preserved the
  feature: **only warn when you can also suggest** — a genuine typo is within
  edit distance ≤2 of a documented sibling; a hidden valid name almost never
  is. "Never flag what you cannot correct" became the project's diagnostic
  motto, applied to unknown-option, unknown-package, and enum quick fixes.
- **Type strings are a parseable goldmine.** `one of "a", "b"` → enum
  validation AND in-string value completion; `string matching the pattern X`
  → regex checks (only when Go's regexp compiles it; skip otherwise);
  "attribute set of (submodule)" → structural expectations. Parse
  conservatively: any member/shape you don't recognize → skip the check
  entirely.
- **Judge only literals.** A type check that evaluates `lib.mkIf cond "yes"`
  or a variable reference will be wrong; one that judges only plain,
  interpolation-free literals never is. Skip everything computed.
- **Wildcards/instances need first-class treatment.** `systemd.services.
  <name>.description` — the trie descends exact → `<name>` → `*` with
  backtracking; instance segments are always accepted (never flagged);
  display headers substitute the user's concrete instance for the
  placeholder. And beware: raw `<name>` in markdown renders as an HTML tag
  and disappears (`systemd.services..description`) — escape or substitute.
- **Freeform boundaries: stop at documented leaves.** Below `nix.settings`
  (attrsOf anything) arbitrary keys are legal — descent must stop at any node
  holding a doc and never judge deeper.
- **Be honest about data provenance.** The channel artifact tracks the channel
  tip, not the user's locked rev; overlays change real versions. Hovers carry
  a provenance line ("<channel> channel data — an overlay may change the
  actual version"). A curated hand-table covered famous non-derivation attrs
  (`runtimeShell`, `mkShell`, fetchers) the dataset can never contain —
  descriptions only, no invented versions, no provenance line.
- **Big-data engineering:** the packages artifact is 391MB decompressed —
  stream-parse with a token decoder (never hold the document), retain a
  trimmed struct per entry, cache the *trimmed* serialization (tens of MB),
  and lazily build sorted indexes on first completion so hover-only sessions
  never pay. Measured results: full both-dataset load in ~3.9s, option
  completion 1ms, package completion 23ms over 145k entries.
- **Downloads: async, cached, fallback, and OFF in tests.** Channel from the
  lock file (fallback to a default), user cache dir keyed by channel, 7-day
  TTL, atomic tmp+rename writes, stale-cache-on-download-failure, never block
  a request on loading (features return null until ready, then a refresh hook
  re-publishes). Auto-download is gated behind a method only `main()` calls,
  so no test can ever touch the network even by omission.

---

## 7. Diagnostics discipline: the false-positive budget is zero

A missing diagnostic costs little; a wrong one destroys trust in every other
diagnostic. Rules that survived contact with a real user:

1. Gate by context evidence (module gate: `config` formal OR ≥2 exact schema
   hits) so arbitrary data attrsets are never judged.
2. Unknown first segment → silent (can't distinguish "not an option tree"
   from "typo'd root"; e.g. `isoImage.*` from an unimported profile must not
   flag).
3. Warn only with a suggestion attached (§6).
4. One diagnostic per path — the first unknown segment, not a cascade.
5. Quick fixes pair with their diagnostic by exact code+range and recompute
   from the same inputs, so a fix can never appear without its warning.
6. **Every real-world false positive becomes a permanent verbatim fixture.**
   The user's actual appliance module (interpolated import, hidden option,
   freeform leaf, `mkDefault`, no `config` formal) is now a handler-level
   test asserting *zero* diagnostics — with the gate proven armed, so the
   silence is non-vacuous.
7. Sweep real corpora with the full dataset before shipping a new diagnostic
   (all example configs must publish zero dataset diagnostics; a typo buffer
   must publish exactly its two expected warnings).

---

## 8. Feature-design patterns that repeated

- **Chain features with strict priority and falling-through nulls.** Hover:
  flake-input → path-literal → package → option → binding-value; each returns
  nil to pass. Cheap, predictable, and each addition slots in without
  touching the others. Same for definition and completion dispatch.
- **Lazy documentation via `completionItem/resolve`.** Shipping 100 rendered
  markdown docs per keystroke is real payload; ship lean items with an opaque
  `data` payload (source + path/attr) and fill documentation on selection,
  reusing the hover renderers. Resolve must never error — unknown data
  round-trips the item unchanged.
- **CompletionItem mechanics:** `textEdit` with the exact partial-segment
  range beats `insertText`; `CompletionList{isIncomplete}` when capped;
  segment-dedupe namespaces (offer one `python312Packages` group entry, not
  400 leaves); `sortText` to rank leaves before groups; minimum-prefix gates
  where the namespace is huge (2+ chars for bare `with pkgs;` names).
- **Value hover = source text, honestly labeled.** Showing the bound
  *expression* (`**system** — let binding` + fenced source) answers most
  "what is this" questions without evaluation; never imply it is a computed
  value. Render script-carrying attrs' content as ```bash fences (VS Code
  highlights fences natively — the one place you get embedded highlighting
  for free). Dedent from the continuation lines only — the first line of an
  extracted expression starts at the expression, not at column 0.
- **Everything that consumes X should get X's features** — users think in
  workloads, not AST shapes. Package hover had to cover `pkgs.foo`, bare
  names under `with pkgs;`, AND `${pkgs.foo}` in strings before it felt
  "done". Enumerate the surface forms of a concept early.

---

## 9. Editor extension & TextMate lessons (a genuinely deep swamp)

- **Injection grammars: delimiter ownership is host-state management.** If
  your injected region consumes tokens the host grammar uses for its own
  state (string delimiters, the first binding name an attrset-vs-function
  disambiguation peeks at), the host desyncs and scopes later tokens
  `invalid` (the "red semicolon" and "broken closing brace" bugs). Solutions
  discovered, in order of preference: match via **lookbehind** so the host
  tokenizes the shared token itself; or consume-and-rescope host-identically
  AND consume through a clean host boundary (the trailing `;`) — and the
  right choice differs per trigger depending on whether the host had already
  opened its region (we needed opposite policies for two triggers in the same
  grammar).
- **`begin`/`while`, not `begin`/`end`, for line-oriented embedded regions:**
  the embedded language's own rules stay open across lines and will eat your
  terminator; `while` re-checks at each line start and force-pops them.
- **Scope parity assertions.** Theme-visible bugs (white delimiters) came
  from missing parent scopes (`punctuation.*` without `string.*`). The
  harness asserts *scope-set equality* between our tokens and host-owned
  reference tokens in the same tokenization run, so drift is impossible.
- **Nested-context injection needs a second grammar.** Once the shell grammar
  pushes its own string context, region-level patterns aren't consulted; a
  tiny second injection grammar with selector `L:meta.embedded.block.
  shellscript` reasserts host-language interpolation inside embedded code.
- **Build a token-level harness (vscode-textmate + vscode-oniguruma) on day
  one.** 41 assertions ran on every grammar change; injections are supplied
  via `RegistryOptions.getInjections` (the `loadGrammarWithConfiguration`
  injections parameter is silently ignored — found empirically).
- **Trigger lists should be exact-segment, word-bounded, and grown from user
  demand** (6 names → 41: shell-init family + stdenv phases). Negative tests
  for near-miss names (`shellInitFor`) are as important as positives. Skip
  ambiguous carriers (`text = ''` holds arbitrary content — a wrong bash
  colorization is worse than none).
- **Assorted extension facts:** contribute `filenames: ["flake.lock"]` under
  the built-in `json` language id to associate lock files; language
  `configuration` files are JSONC (comments/trailing commas fine);
  `languages[].icon` gives file icons but icon *themes* may override;
  `.vscodeignore` must NOT exclude `node_modules` unless you bundle (runtime
  deps live there — a broken-VSIX-in-waiting we caught by inspection);
  markdown in hover strips unknown HTML-ish tags (escape `<name>`); language
  icons + grammars need a window reload, server changes only need a restart
  command — build a `restart` command early, it is the dev loop.

---

## 10. Testing strategy that actually caught things

- **Per-stage gate, run by the integrator, not just the implementer:** build,
  vet, full `go test -count=1`, `-race` on every concurrency-touching
  package, gofmt, extension tsc — before every commit, serially, on a settled
  tree.
- **Real-binary stdio smoke tests for every feature** — the compiled server
  driven over framed stdio caught things unit tests could not (the `$/
  setTrace` crash, response framing, the initialize/hover race, byte-length
  bugs). Drivers must behave like real clients: await each response before
  the next request; never fire-and-sleep (requests run on goroutines and
  race notifications — a hover raced a didChange and "tested" the wrong
  buffer).
- **Verify against full real data before shipping**, not just fixtures: the
  391MB dataset load time, completion latencies, and the zero-false-positive
  sweep over real configs were all full-data checks; fixtures alone would
  have hidden the hidden-options hole (§6).
- **Proof-first bug fixing:** for the scheduler wedge the rule was "build the
  failing repro BEFORE the fix" — a red test that parks workers and shows
  didChange blocking. The discipline prevents fixing a theory instead of the
  bug — and twice here the theory was wrong (the 'wg' hint was a stale
  keystroke; the real bug was an invisible MISSING token).
- **Beware the stale binary.** Twice, a smoke test "failed" or "passed"
  against a binary that predated the change (`go build ./...` does not update
  `./nixls`; `-o` does). Make the smoke script build the binary itself.
- Structure loaders so channel selection, TTL math, and parsing are pure
  functions tested without network; keep ALL network behind a main()-only
  enable switch.

---

## 11. Build, packaging, release

- **cgo (tree-sitter) dictates deployment:** no `GOOS=wasip1` (so no
  vscode.dev), no easy cross-compilation → per-platform builds on native CI
  runners. Plan for this before choosing a cgo parser — or budget for a
  pure-Go/wasm parser alternative.
- **Platform-specific VSIXes with the server bundled** (`vsce package
  --target linux-x64` etc.) are the standard distribution (rust-analyzer
  model); extension resolves explicit setting → bundled binary → PATH. The
  VSIX is self-contained; datasets download at runtime keeping it ~3MB.
- **Nix-first pipeline gotchas (a day of learning in five bullets):**
  `git+file:` flakes see only *tracked* files — `git add` new files or the
  sandbox build silently lacks them; buildGoModule's checkPhase runs the test
  suite, making `nix build` the CI gate; vendorHash/npmDepsHash by the
  fake-hash-then-copy-real-hash dance; npm `@vscode/vsce` drags `keytar`
  (node-gyp, needed only for *publishing*) into sandbox builds — use
  nixpkgs' `vsce`; vsce **interactively prompts** on missing LICENSE and can
  exit 0 having written nothing → a "successful" cached empty derivation.
  Add a LICENSE before any packaging.
- **CI entropy is constant:** runner images deprecate (macos-13 was the last
  Intel image — dropping it dropped darwin-x64), caching actions die
  (magic-nix-cache), and UI-created releases fire `release` events, not tag
  pushes — support both triggers and update-in-place so prerelease flags
  survive. Windows has no nix; keep one plain job and accept the asymmetry.

---

## 12. The Terraform mapping

Direct translations of the above for a Terraform LSP:

- **Parser:** HCL2 has an official Go parser (`hashicorp/hcl/v2`) with
  diagnostics and *partial-content tolerance* — likely better than
  tree-sitter-hcl for semantics (and no cgo → cross-compile and wasm come
  back!). Consider hcl for analysis + tree-sitter-hcl only if you need
  TextMate-adjacent tokenization. If pure Go, the whole §11 cgo section
  dissolves — one machine cross-compiles everything.
- **The schema artifact exists and is better than Nix's:** `terraform
  providers schema -json` emits every resource/data-source/attribute with
  types, required/optional/computed, and descriptions — the exact analog of
  options.json, but *per-workspace exact* (generated from the lock file's
  providers, no channel-tip drift, no provenance caveats). The catch: it
  requires running `terraform` with initialized providers — decide early
  whether running the CLI is inside your policy line (we never ran `nix`;
  terraform-ls DOES run terraform). Fallback: the public registry serves
  schemas over HTTP (registry.terraform.io API) — that is the channels.nixos
  .org equivalent, with real version pinning from `.terraform.lock.hcl`
  (analog of flake.lock — parse it for provider versions the way we parsed
  the lock for the channel).
- **Membership-is-the-filter transfers perfectly:** block type + labels
  (`resource "aws_instance" "web"`) → schema lookup; attributes validate
  against it. `required` fields enable an honest "missing required argument"
  diagnostic that Nix could never support (§6's "required isn't knowable"
  limitation does not apply — Terraform schemas declare it).
- **Hidden-option lesson still applies:** provider schema versions drift from
  what's installed; dynamic blocks, `for_each`, meta-arguments (`count`,
  `lifecycle`, `depends_on`, `provider`) are valid-but-not-in-resource-schema
  — build the meta-argument allowlist first or you will recreate the
  `disableInstallerTools` incident on day one. Warn only with a near-miss
  suggestion, always.
- **Enum/type checks:** Terraform types are *structured* (cty types in the
  schema JSON), not strings to parse — the whole §6 type-string parser is
  unnecessary; you get types for free and can go further (object attribute
  checking). Judge only literals; skip anything with references, functions,
  or interpolation, same rule.
- **References are richer:** `var.x`, `local.y`, `module.z.output`,
  `resource_type.name.attr` — the scopes/VisibleBindings work maps to a
  cross-file symbol table per module directory; a Terraform *module* is the
  workspace-unit the way a flake was. `.terraform/modules/modules.json`
  locates downloaded module sources for cross-module navigation (path-literal
  navigation analog: `source = "./modules/vpc"`).
- **Everything in §§1–3, 7, 10 transfers verbatim:** pure analysis layers,
  memo with explicit inputs (make `.terraform.lock.hcl`, `.terraform/`
  contents, AND git state inputs from day one), never block the read loop,
  coalesced recomputes, generation guards, notification error swallowing,
  zero-false-positive budget, real-config regression fixtures, real-binary
  smoke drivers, full-dataset sweeps.
- **Study terraform-ls first** (HashiCorp's own) the way nil/nixd were
  reference points here — the goal is knowing which gaps are worth filling,
  not rebuilding what exists.

---

## 13. Meta: process lessons

- **Empirical-first is the master rule.** Nearly every wrong design in this
  project came from assuming a shape (a parse recovery, an API's semantics, a
  dataset's contents, a grammar's behavior) instead of dumping the real
  thing first. The standing instruction "verify against node-types.json / dump
  the real parse / check the live dataset BEFORE coding" caught issues every
  single time it was followed and cost issues every time it wasn't.
- **Parallel work needs disjoint file ownership declared up front**, plus the
  instruction that unexpected failures in others' files mean *wait and
  re-run*, never fix. Two uncoordinated sessions in one repo produced two
  collisions (files swept into others' commits, duplicate feature builds).
- **Commit hygiene under concurrency:** stage explicit paths (never `-A`),
  check HEAD before and after committing, keep a stale-binary and stale-cache
  suspicion at all times.
- **A user living in the tool beats any test plan.** The highest-value bugs
  (frozen diagnostics, wrong-line errors, false-positive on a hidden option,
  git-add staleness) all came from someone actually using it within minutes
  of each feature landing. Ship to a real config early; treat every report as
  a permanent regression fixture.
- **Keep a changelog and per-session handoff notes** — half this document was
  reconstructable only because they existed.
