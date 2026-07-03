# CLAUDE.md

`nixls` is a Go language server for Nix (whole-workspace analysis, flake-aware
diagnostics, editor features over LSP/JSON-RPC on stdio). The server lives under
`cmd/nixls` + `internal/`; the VS Code dev client is under `editors/vscode`. The
whole thing builds via a Nix flake (`flake.nix`).

## Environment

Go and Node tooling are available **only** through the Nix devshell. The bare
PATH has neither `go` nor `node` — always prefix commands with
`nix develop --command ...` (e.g. `nix develop --command go build ./...`). In
interactive terminals `direnv` normally loads the devshell automatically, so the
tools appear on PATH there; scripted/agent shells must use the explicit prefix.

## Agent workflow: Fable plans, Opus codes

### Roles

- **Main agent (Fable)** is the architect and integrator. It diagnoses, designs,
  writes the spec, reviews the diff line-by-line, runs the verification gate,
  and makes every commit. It never delegates thinking it hasn't finished.
- **Subagents (Opus, via the Agent tool)** are implementers. They get a
  self-contained spec and write code + tests. They NEVER commit, never touch
  git state, and never expand scope beyond the spec.

Why this split works: the main agent keeps the whole design in one head (so the
pieces fit), while implementation — token-heavy but decision-light once the
spec is precise — fans out.

### Writing the spec (the prompt for a coding agent)

A good spec includes:

1. The exact files to create/modify (explicit paths).
2. The behavior contract: inputs, outputs, edge cases, error messages verbatim.
3. What NOT to do: files that are off-limits, patterns to avoid, "do not run
   git", "do not touch examples/nixos/configuration.nix".
4. How the agent should verify locally (`nix develop --command go test ./...`)
   and the instruction to leave the tree buildable.
5. The statement that its final message is a report, not the deliverable —
   the files on disk are.

### Parallelism rules

- Run agents in parallel ONLY with explicitly disjoint file lists, stated in
  both prompts ("Agent A owns internal/server/completion.go; Agent B owns
  internal/analysis/scopes/ — do not edit each other's files").
- Add boundary warnings so one agent doesn't "helpfully" fix the other's area.
- If two agents' work interleaves anyway, prefer one combined commit over
  untangling (ask before deviating from the one-commit-per-fix rule).

### When an agent finishes (or dies)

- Review the diff yourself, line by line, before trusting anything.
- Re-run the full gate yourself; the agent's claim of passing tests is a claim.
- If an agent's connection drops, resume it with SendMessage first; if it
  crashed, AUDIT THE TREE before assuming work was lost — agents often crash
  after writing all files but before reporting.
- Delete agent scratch files (zz_scratch_test.go and friends) before committing.

## Verification gate (before every commit)

```sh
nix develop --command go build ./...
nix develop --command go vet ./...
nix develop --command go test ./... -count=1
nix develop --command go test -race ./internal/server/
```

- Rebuild the real binary with `-o` (`nix develop --command go build -o nixls
  ./cmd/nixls` — a bare `go build ./...` leaves the old binary in place) and
  smoke-test over real LSP stdio when the change touches the server. Use the
  reusable node LSP driver pattern from the session notes (`lspclient.js` /
  `diag-smoke.js`): spawn the binary, frame requests with a **byte**-counted
  `Content-Length`, and await each response — never fire-and-sleep, because
  requests run on goroutines and race the notifications otherwise.
- Proof-first for bug fixes: write the failing (red) test BEFORE the fix.

Note: `nix flake check` (and `nix build .#nixls`) runs the full `go test ./...`
suite in `buildGoModule`'s checkPhase, so it is also a verification gate — but
it needs new files to be **git-tracked** (git+file flakes only see tracked
files; `git add` before `nix build`).

## Hash bumps (Nix will fail loudly otherwise)

- Any `go.mod` / `go.sum` change invalidates `vendorHash` in `flake.nix`.
- Any `editors/vscode/package-lock.json` change invalidates `npmDepsHash`.

Recipe: set the stale hash to `nixpkgs.lib.fakeHash` in `flake.nix`, run
`nix build .#nixls` (or `.#vsix` for the npm hash), copy the `got: sha256-...`
value from the error back into `flake.nix`, and rebuild.

## Commit discipline

- One commit per fix/feature, committed by the main agent only.
- Stage explicit paths only — **never** `git add -A` or `git add .` from the
  repo root (a past incident staged `node_modules`).
- One-line commit message, no Co-Authored-By or any trailer.
- Never push unless explicitly asked.
- Check `git log` HEAD before AND after committing (parallel sessions may have
  moved it); verify the commit contains exactly the intended files.
- `examples/nixos/configuration.nix` carries the user's live-test edits; leave
  it alone unless the task is about it.

## Session handoff

Handoff notes live in `docs/session-notes/` (date-prefixed). Read the **newest**
first when picking up work; `CHANGELOG.md` tracks user-visible commits.
