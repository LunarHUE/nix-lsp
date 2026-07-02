# nixls demo workspace

A small, self-contained workspace that exercises most of what `nixls` does today.

## Open it as its own workspace folder

Root detection picks the **nearest** `flake.nix` above the file you open, so open
**`examples/demo`** as the workspace folder (not the repository root). In the
devcontainer window: *File -> Open Folder... -> examples/demo*. Only then is
`examples/demo/flake.nix` treated as the workspace-root flake, which is the only
flake the flake-specific features fire on.

Then point the extension at your built server: set `nixls.serverPath` (Settings
-> search "nixls") to the absolute path of your `./nixls` binary, or run
**nixls: Restart Server** after (re)building it.

> If you instead open the repository root, diagnostics still appear for these
> files, but the flake features (hover, completion, flake diagnostics, follows
> quick fixes) fire only for the root flake at the repo root, not for this one.

## What to try

### `flake.nix` — flake features

| Where | Try | You should see |
| --- | --- | --- |
| `nixpkgs` name / `.url` / a `follows` target | Hover | locked source, 12-char rev, and last-modified date from `flake.lock` |
| `demo-lib` name | Hover | url plus `flake = false` |
| `home-manager`'s `inputs.nixpkgs.follows = "nixpkgs"` target | Ctrl-click / Go to Definition | jumps to the `nixpkgs` input declaration |
| `flake-utils`'s `follows = "nixpkgss"` (typo) | Look at the squiggle | `dangling-follows` error |
| same typo | Quick fix (Ctrl+.) | **Change follows target to 'nixpkgs'** |
| `unlocked-extra` name | Look at the squiggle | `input-not-locked` warning (it is missing from `flake.lock`) |
| `demo-lib` name | Look at the squiggle + Quick fix | `unused-input` warning, with **Remove input 'demo-lib'** and **Add 'demo-lib' to outputs** |
| the `inputs` attribute name | Look at the squiggle | `stale-lock-entry` warning: `flake.lock` has an `ancient` entry with no matching input |
| inside a `follows` string's quotes | Trigger completion | declared input names |
| inside the `outputs { ... }` formals | Trigger completion | declared input names plus `self` |

### `demo.nix` — static analysis + navigation

| Where | Try | You should see |
| --- | --- | --- |
| `unusedValue = 42;` | Look at the squiggle | `unused-binding` warning |
| `_scratch = 99;` | (nothing) | deliberately **not** flagged (leading underscore) |
| `inherit undefinedName;` | Look at the squiggle | `bad-inherit` error |
| the second `port = 9090;` | Look at the squiggle | `duplicate-binding` error |
| `import ./missing.nix` | Look at the squiggle | `missing-import` error |
| `greet` in `lib.greet "world"` | Ctrl-click / Go to Definition | jumps into `lib.nix` at `greet` |
| `import ./lib.nix` path | Go to Definition | jumps to the top of `lib.nix` |
| `users`, `makeUser` | Find All References / Highlight | uses within the file |
| anywhere | Open Outline / use folding | the nested `let`, function, list, and attrsets are structured |
| anywhere | Ctrl+T (workspace symbols) | search bindings across `demo.nix` and `lib.nix` |

### Untracked-import warning (needs a new file)

The untracked-import warning only fires for an import target that exists on disk
but is not git-tracked. To see it: create a new `extra.nix` (any valid Nix, e.g.
`{}`), add `extra = import ./extra.nix;` to the `let` in `demo.nix`, and — before
you `git add extra.nix` — you get a warning on the import path with a **Run git
add** quick fix. Accepting it stages the file and the warning clears on its own.

### Restart command

After rebuilding `./nixls`, run **nixls: Restart Server** from the Command
Palette. It re-reads `nixls.serverPath` and restarts the language server without
reloading the window.

Note: rename is **N/A** — cross-file rename is not implemented yet.
