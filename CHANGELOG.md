# Changelog

All notable changes to nixls and its VS Code extension. Format loosely follows
[Keep a Changelog](https://keepachangelog.com); dates are UTC.

## Unreleased

### Added — 2026-07-02 (evening)

- **Lazy completion documentation** (`completionItem/resolve`): completion
  lists ship lean items and the full markdown documentation loads only for the
  item you highlight, using the same renderers as hover.
- **Clickable hover links**: package homepages render as markdown links, and
  option "Declared in" paths link to the declaring module's source on the
  dataset channel's branch of nixpkgs on GitHub (plain text in offline/fixture
  mode or for non-nixpkgs paths).

- **Dot-triggered completion everywhere**: typing `.` (or invoking completion)
  now completes NixOS option paths (`networking.<cursor>` offers `firewall`
  with type and docs; works through submodule instances like
  `systemd.services.myservice.<cursor>`), nixpkgs attributes (`pkgs.cl<cursor>`
  offers `claude-code` with its version; namespaces like `python312Packages`
  collapse to a single group entry), bare names under `with pkgs;` (after two
  typed characters, curated helpers like `mkShell` included), and lexically
  visible local bindings — all working on mid-edit, syntactically incomplete
  code. Flake input completion (follows targets, outputs formals) keeps its
  existing behavior and priority.

### Added — 2026-07-02

- **NixOS option hover**: hovering an option attrpath (flat, nested, or read via
  `config.*`) in any `.nix` file shows its description, type, default, example,
  and declaring module, from the official channel `options.json` (24k+ options).
  Submodule wildcards resolve through concrete instances
  (`users.users.alice.home` → `users.users.<name>.home` docs), headers name the
  path you hovered, and hovering an instance segment (`demo-web` in
  `systemd.services.demo-web`) falls back to the nearest documented prefix.
- **Package version hover**: `pkgs.<attr>` selects and bare identifiers under
  `with pkgs;` show the package's name, version, and description from the
  channel `packages.json` (145k+ packages), with a provenance line noting the
  channel (overlays may change actual versions). Well-known non-derivation
  attrs (`runtimeShell`, `lib`, `callPackage`, `mkShell`, fetchers, trivial
  writers) get curated doc-only hovers.
- **Binding-value hover**: hovering a locally bound identifier (including
  inside `${...}`) shows the source expression it is bound to — let bindings,
  attributes, function parameters with defaults. Indented strings dedent
  properly, single-line strings collapse to one line, and script-carrying
  attributes (`script`, `preStart`, `shellHook`, ...) render their content in a
  bash fence for real shell highlighting in the hover.
- **Flake input hover**: now shows the pinned `ref:` (channel or version tag)
  alongside the locked revision and date.
- **Dataset auto-download**: both datasets fetch for the channel named by
  flake.lock's nixpkgs pin (fallback nixos-unstable), cache under the user
  cache dir with a 7-day TTL and stale-cache fallback, load off the request
  path, and can be overridden or disabled via `nixls.optionsPath` /
  `nixls.packagesPath` ("off" disables; a local file path serves offline).
- **Syntax highlighting** (extension): vendored the nix-community TextMate
  grammar and language configuration; original injection grammar highlights
  bash embedded in Nix strings (shebang bodies, `script`/`preStart`/
  `shellHook`-style attrs, `writeShellScript(Bin)` calls) with Nix
  interpolations inside them still scoped as Nix; `flake.lock` associates with
  JSON.
- **Example workspaces**: `examples/{nixos,monorepo,iso,scripts}` demonstrating
  option hover, monorepo navigation, an installer-ISO build (with the
  `isoImage.*` hover gap documented), and embedded shell scripts.
- **Server robustness**: unknown `$/` notifications (e.g. `$/setTrace`) are
  ignored per the LSP spec instead of killing the connection; notification
  handler errors log to stderr and never tear down the server; the binary
  accepts `--stdio` for client compatibility.

### Added — 2026-07-03

- **Enum and pattern value checks**: an enum-typed option bound to an illegal
  string warns with the full legal list (`PermitRootLogin expects one of
  "yes", "without-password", ...; got "maybe"`), with a "Change to" quick fix
  when a legal value is a near-miss; `string without spaces` and
  `string matching the pattern <regex>` types validate literal values too.
  Only plain literals are judged; interpolations, `mkIf`, and anything opaque
  stays silent.
- **Enum value completion inside strings**: with the cursor inside the string
  value of an enum option (`PermitRootLogin = "pro"`), completion offers the
  legal values — including mid-edit with an unclosed quote.
- **Hover notes missing defaults**: an option with no default renders
  "*No default — must be set when used.*" in its hover.

- **Option-aware syntax guidance**: a missing `;` after a binding — previously
  invisible (tree-sitter reports it as an anonymous zero-width token the
  diagnostics walk never visited) — now reports `missing ';' after binding`,
  and when the broken binding sits inside a known option path the message
  appends what belongs there:
  `— networking.wireguard.interfaces.wg0 accepts options like ips, peers,
  privateKey`. Name-bearing hints are emitted only when the parse tree proves
  the complete identifier, so a stale keystroke can never blame a truncated
  name.
- **Empty-body option completion**: invoking completion inside the empty
  braces of `wg0 = { }` under an options attrset lists the submodule's
  options (`ips`, `peers`, `privateKey`, ...), not just after typing begins.
- **Real Nix logo file icons**: the language icon now uses the NixOS
  lambda-snowflake (CC-BY-4.0, NixOS Foundation) — near-black for light
  themes, brand light blue for dark themes — replacing the placeholder mark.
- **Option type checks**: a documented option bound to a literal of the wrong
  kind warns, e.g. `type mismatch: networking.firewall.enable expects boolean,
  got string` (code `option-type-mismatch`). Only unambiguous literals are
  judged — references, `lib.mkIf`/`mkForce` calls, selects, and interpolated
  strings are never second-guessed, and unmapped types (enums, packages,
  paths) are skipped.
- **Clearer syntax errors**: recognizable mid-edit mistakes get specific
  messages — a bare name in binding position (`{ wg0 }`) says
  `attribute 'wg0' has no value (expected 'wg0 = <value>;')`, and a missing
  `;` between bindings is called out — without ever adding a diagnostic the
  parser did not already report.
- **Path-literal navigation**: go-to-definition follows any static path
  literal to its target (bare binding values like
  `nixosModules.x = ./module.nix`, list elements, directory imports via
  `default.nix`), and hovering a path shows where it resolves plus its status
  (exists / missing / not git-tracked). Interpolated paths and `<...>` search
  paths stay unfollowed.
- **Typo diagnostics with quick fixes**: a misspelled option path in a NixOS
  module (`networking.firewal.enable`) warns `unknown-option` with "Change to
  'firewall'" fixes, and a misspelled `pkgs.<attr>` (`pkgs.htoop`) warns
  `unknown-package` suggesting `htop`. Deliberately conservative: options only
  in module-shaped files under known namespaces (installer-profile paths like
  `isoImage.*` stay silent), wildcard instance names always accepted, packages
  only flagged when a near-miss exists (so `pkgs.lib.mkIf` never squiggles).
  Diagnostics refresh automatically for open files when a dataset finishes
  loading.

- **Release packaging, nix-first**: the flake now builds everything —
  `nix build .#nixls` produces the server (tests run in the check phase) and
  `nix build .#vsix` a platform-specific VSIX with the server bundled at
  `bin/nixls`. CI runs `nix flake check` on PRs, and the release workflow runs
  `nix build .#vsix` on linux x64/arm64 and macOS arm64 runners (Intel macOS
  is not built — the last x64 runner image is deprecated); Windows
  (where nix does not run) keeps a plain Go + npm job producing the same
  artifact shape. The extension resolves the server as explicit
  `nixls.serverPath` → bundled binary → PATH, and standalone binaries attach
  to releases for non-VS Code editors. Added the repo's MIT LICENSE file
  (matching the declared package license) — vsce requires one to package.

### Fixed — 2026-07-03

- **Stale untracked-import warning after terminal `git add`**: git state was
  invisible to both the file watcher (which ignored `.git`) and the memoized
  import analysis (git-tracking was baked into cached edges). The extension
  now watches `.git/index`, and git state is a real input to the analysis, so
  a terminal `git add`/`commit` clears the warning with no edit needed.
- **Embedded bash now covers the full shell-attr family**: `shellInit`,
  `interactiveShellInit` and friends, `extraCommands`, and all stdenv builder
  phases (`buildPhase`, `postInstall`, ...) highlight as bash — 41 trigger
  names total. Two grammar fixes rode along: a trigger attr as the first
  binding of an attrset no longer desyncs the host's brace matching, and Nix
  `${...}` interpolation inside shell double-quoted strings scopes as Nix
  instead of shell parameter expansion.
- **Frozen diagnostics requiring a restart**: bursts of edits could fill the
  background task queue, and the blocking submit then wedged the LSP read
  loop itself — freezing every feature with stale errors stuck on screen.
  Diagnostics recomputes are now coalesced per file (only the newest buffer
  matters) and the notification path never blocks, so heavy typing always
  converges to the current content.
- **`unknown-option` false positive on hidden options**: real options
  declared `internal`/invisible are absent from the docs dataset (e.g.
  `system.disableInstallerTools`); the warning now requires a near-miss
  did-you-mean suggestion to fire, so it never flags what it cannot correct.
- **Misplaced missing-`;` errors**: deleting the `;` after a binding like
  `pkgs = import nixpkgs { ... }` used to error below the real mistake; the
  diagnostic now anchors where the `;` belongs and names the swallowed
  binding when provable (`missing ';' before 'corePackages'`).
- **Precise expected-token syntax errors**: previously invisible parser
  recoveries now report `expected '}'` / `expected ']'` / `expected ')'` at
  their exact position, and unclosed delimiters at end of file say
  `unclosed '{' (expected '}')`; generic "syntax error" messages are dropped
  when a precise one covers the same spot.

- Completion now fires on trailing dots at any depth (`networking.firewall.`,
  nested attrsets, `config.`-prefixed paths, quoted segments like
  `services."my-svc".`) — previously only a single-segment trailing dot
  classified, because deeper mid-edit parses produce different error-tree
  shapes.

### Fixed — 2026-07-02

- Extension no longer passes `--stdio` to a server that rejected it (crash
  loop on activation).
- Embedded-shell regions no longer desync the host grammar: trailing `;` after
  `script = ''...'';` bindings is scoped as a normal binding terminator
  (was rendered invalid/red), and region delimiters carry the host's string
  scopes (were uncolored).
- Option hover headers no longer render `<name>` as a stripped HTML tag
  (`systemd.services..description`).
