# nix-lsp

`nix-lsp` is a Go language server for Nix focused on whole-workspace analysis, flake-aware diagnostics, and fast interactive editor features.

This repository is being built in phases. The initial implementation starts with the Phase 0 foundation:

- JSON-RPC/LSP transport over stdio
- request scheduling and cancellation seams
- VFS overlay snapshots for open editor buffers
- syntax diagnostics surface area
- memoized analysis primitives

See [docs/implementation-plan.md](docs/implementation-plan.md) for the phased roadmap.

## Development

```sh
go test ./...
go run ./cmd/nixls
```
