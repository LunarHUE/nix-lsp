package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/wesleybaldwin/nix-lsp/internal/lsp"
	"github.com/wesleybaldwin/nix-lsp/internal/server"
)

var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	// Accepted for compatibility with LSP clients that pass --stdio; stdio is
	// the only transport the server supports, so the flag is a no-op.
	flag.Bool("stdio", true, "communicate over stdin/stdout (default and only transport)")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	handler := server.NewHandler()
	// Enable the NixOS option-hover dataset auto-download for the real server; the
	// default (empty optionsPath) mode fetches and caches it for the locked
	// nixpkgs channel. Explicit-path and "off" modes are unaffected.
	handler.EnableOptionsDownload()
	defer handler.Close()

	lspServer := lsp.NewServer(os.Stdin, os.Stdout, handler)
	if err := lspServer.Run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "nixls: %v\n", err)
		os.Exit(1)
	}
}
