{
  description = "minimal nix-lsp fixture";

  outputs = { self }: {
    packages.x86_64-linux.default = self;
  };
}
