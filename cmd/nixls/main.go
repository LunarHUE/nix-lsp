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
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	handler := server.NewHandler()
	defer handler.Close()

	lspServer := lsp.NewServer(os.Stdin, os.Stdout, handler)
	if err := lspServer.Run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "nixls: %v\n", err)
		os.Exit(1)
	}
}
