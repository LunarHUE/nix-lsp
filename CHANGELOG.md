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

- **Release packaging**: a GitHub Actions release workflow builds `nixls` per
  platform (linux x64/arm64, macOS x64/arm64, Windows x64 — native runners,
  since tree-sitter's cgo blocks cross-compilation) and packages
  platform-specific VSIXes with the binary bundled; the extension resolves the
  server as explicit `nixls.serverPath` → bundled binary → PATH. Standalone
  binaries attach to the release for non-VS Code editors.

### Fixed — 2026-07-03

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
