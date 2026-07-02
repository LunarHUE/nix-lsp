# NixOS Option Hover — Handoff Plan (2026-07-02)

Phase 4 slice pulled forward with user sign-off: hover documentation for
NixOS option attrpaths (e.g. `networking.firewall.allowedTCPPorts`) in any
`.nix` file. Signed-off decisions: **auto-download + cache** for data
acquisition (network OK, still no nix execution), **hover only** for v1
(no completion, no diagnostics, no home-manager/darwin, no builtins).

## Data source (verified this session)

Official NixOS release artifact, the same data behind search.nixos.org:

- `https://channels.nixos.org/<channel>/options.json.br` → 302 to a pinned
  `releases.nixos.org/nixos/<channel>/nixos-<version>/options.json.br`.
- Verified 2026-07-02 on `nixos-unstable`: 24,631 options, ~1.1 MB br,
  ~10.6 MB JSON. `nixos-25.05` artifact also live.
- Entry shape (keyed by dotted path, wildcards appear as `<name>`/`*`):
  `description` (string), `type` (human string), `default`/`example`
  (either plain JSON or `{"_type":"literalExpression","text":"..."}`;
  also `literalMD`), `declarations` (paths like
  `nixos/modules/services/networking/firewall.nix`), `loc` (segments),
  `readOnly`.
- Known approximation: the artifact tracks the channel tip, not the exact
  locked nixpkgs rev. Acceptable for docs hover; document it.
- Dead end checked: `nix __dump-builtins` is removed in the devshell's
  Determinate Nix 3.21 — builtins hover (future work) needs a vendored
  table, not runtime dumping.

## Dependency decision (needs Wesley's eyes at review)

The artifact only exists brotli-compressed (plain `options.json` 404s).
Go stdlib has no brotli decoder → one new module dependency:
`github.com/andybalholm/brotli` (pure Go, decode path only, no cgo).
This is the repo's first external Go dep; flagged per the phase-3 policy
notes. Zero-dep alternative (rejected): require users to hand-place a
decompressed file — kills the out-of-box experience that was the point of
the auto-download decision.

## Design

### Slice 1 — `internal/analysis/options` (pure, no I/O)

- `Doc` struct: Loc []string, Description, Type string, Default, Example
  (rendered to Nix source text from literalExpression/plain JSON),
  Declarations []string, ReadOnly bool.
- `Parse([]byte) (*Index, error)` over the options.json shape.
- `Index.Lookup(path []string) (*Doc, bool)`: trie keyed by segment;
  descent tries exact child, then `<name>`, then `*` at each level (so
  `users.users.wesley.home` hits `users.users.<name>.home`).
- Rendering helper for hover markdown lives here too (pure): name in
  bold, description, `*Type:*`, `*Default:*` / `*Example:*` in ```nix
  fences, `*Declared in:*` file list.

### Slice 2 — attrpath-at-position (pure CST, `internal/analysis/scopes`)

- New helper (attrnav.go sibling): `OptionPathAt(tree, pos) (path
  []string, r syntax.Range, ok bool)`. Walk from the attrpath segment
  under pos outward through enclosing binding attrpaths / attrset
  nesting, concatenating static segments; bail (ok=false) on any dynamic
  segment or non-attrpath context. Strip a single leading `config.`.
  Return path up to and including the hovered segment, and the hovered
  segment's range (hover range = that segment).
- No "is this a module" heuristic: membership in the options index IS the
  filter. Wrappers like `lib.mkIf`/`mkMerge` wrap values, not binding
  paths, so they need no special handling for path extraction.
- Verify grammar shapes empirically against
  third_party/tree-sitter-nix/src/node-types.json before coding (standing
  rule).

### Slice 3 — fetch/cache + memo wiring (`internal/server` or new `internal/optionsfetch`)

- Channel selection: flake.lock nixpkgs input's `original.ref` when it
  matches `nixos-*`; else `nixos-unstable`. Lock already parsed via
  `facts.FlakeLock`.
- Cache: `os.UserCacheDir()/nixls/options/<channel>.json` (decompressed),
  7-day TTL via mtime; refresh re-follows the channel redirect.
- Async only: kick off on first hover need (or post-initialize); loads
  never block a request — hover returns null until the index is ready.
  Reuse the existing workDoneProgress plumbing for a "Loading NixOS
  options" progress if cheap, else skip.
- Test/override hook: `nixls.optionsPath` (extension setting, forwarded
  via initializationOptions) points at a local decompressed options.json
  and disables networking; also the mechanism smoke tests use. An empty
  sentinel value (`"off"`) disables the feature.
- Index stored as a memo fact keyed by channel + file content hash so a
  changed flake.lock (already watched) can switch channels.

### Slice 4 — hover wiring (`internal/server/flakehover.go` + new optionhover.go)

- Restructure `hover()`: keep the existing root-flake gate for flake
  hovers, then for ANY workspace `.nix` file fall through to option
  hover: facts-parsed tree → `OptionPathAt` → `Index.Lookup` → rendered
  markdown. Null on any miss, as today.
- Order: flake hover first for flake.nix (input hovers must not regress),
  option hover otherwise/afterwards.

## Testing + gate (per slice, standard)

- Unit: options parse/lookup (wildcards, literalExpression vs plain,
  literalMD), OptionPathAt (nested attrsets, `config.` strip, dynamic
  bail, rec attrs, inherit — expect ok=false), markdown rendering golden.
- Handler test: inject a fixture index (small options.json subset in
  testdata/, ~10 entries incl. `networking.firewall.allowedTCPPorts` and
  a `<name>` wildcard) via the optionsPath hook; no network in any test.
- Gate: `nix develop --command` build, vet, `go test ./... -count=1`,
  `go test -race ./internal/server/`, then a real-binary stdio smoke test:
  initialize → didOpen a module snippet → hover over
  `networking.firewall.allowedTCPPorts` with optionsPath pointed at the
  fixture → expect markdown containing "List of TCP ports".
- Manual: hover in the extension against the real downloaded unstable
  dataset.

## Explicit non-goals for v1

Completion, unknown-option diagnostics, home-manager/nix-darwin datasets,
eval-based exactness (nixd-style), builtins/lib hover, go-to-declaration
into nixpkgs sources (declarations are rendered as text only).

## Workflow reminders

Fable writes per-slice spec prompts; Opus subagents implement; agents
never commit; Fable re-reads every production diff, re-runs the full gate,
commits with explicit `git add <paths>` and a one-line message, no
trailers.
