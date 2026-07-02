package flake

import "testing"

const realisticLock = `{
  "nodes": {
    "root": {
      "inputs": {
        "nixpkgs": "nixpkgs",
        "home-manager": "home-manager"
      }
    },
    "nixpkgs": {
      "locked": {
        "type": "github",
        "owner": "NixOS",
        "repo": "nixpkgs",
        "rev": "abc123",
        "narHash": "sha256-xxx",
        "lastModified": 1700000000
      },
      "original": {
        "type": "github",
        "owner": "NixOS",
        "repo": "nixpkgs",
        "ref": "nixos-unstable"
      }
    },
    "home-manager": {
      "inputs": {
        "nixpkgs": [
          "nixpkgs"
        ]
      },
      "locked": {
        "type": "github",
        "owner": "nix-community",
        "repo": "home-manager"
      }
    }
  },
  "root": "root",
  "version": 7
}`

func TestParseLockRealisticMultiInput(t *testing.T) {
	lock, err := ParseLock([]byte(realisticLock))
	if err != nil {
		t.Fatalf("ParseLock error = %v", err)
	}
	if lock.Version != 7 {
		t.Errorf("version = %d, want 7", lock.Version)
	}
	if lock.Root != "root" {
		t.Errorf("root = %q, want root", lock.Root)
	}

	roots := lock.RootInputs()
	if len(roots) != 2 {
		t.Fatalf("root inputs = %d, want 2", len(roots))
	}
	// Direct node key.
	if got := roots["nixpkgs"]; got.Key != "nixpkgs" || len(got.Follows) != 0 {
		t.Errorf("nixpkgs ref = %+v, want direct key", got)
	}

	// A follows array on a transitive node.
	hm := lock.Nodes["home-manager"]
	sub := hm.Inputs["nixpkgs"]
	if sub.Key != "" || len(sub.Follows) != 1 || sub.Follows[0] != "nixpkgs" {
		t.Errorf("home-manager.nixpkgs ref = %+v, want follows [nixpkgs]", sub)
	}

	// Optional fields absent on home-manager.locked stay zero-valued.
	if hm.Locked == nil || hm.Locked.Rev != "" {
		t.Errorf("home-manager locked = %+v, want present with empty rev", hm.Locked)
	}
	nixpkgs := lock.Nodes["nixpkgs"]
	if nixpkgs.Locked == nil || nixpkgs.Locked.Rev != "abc123" || nixpkgs.Locked.LastModified != 1700000000 {
		t.Errorf("nixpkgs locked = %+v, want rev+lastModified populated", nixpkgs.Locked)
	}
}

func TestParseLockRejectsGarbage(t *testing.T) {
	cases := map[string]string{
		"not json":       "this is not json",
		"empty object":   "{}",
		"no nodes":       `{"root":"root","version":7}`,
		"empty nodes":    `{"root":"root","nodes":{},"version":7}`,
		"missing root":   `{"nodes":{"root":{}},"version":7}`,
		"root not found": `{"root":"missing","nodes":{"root":{}},"version":7}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseLock([]byte(content)); err == nil {
				t.Fatalf("ParseLock(%q) = nil error, want rejection", content)
			}
		})
	}
}

func TestParseLockToleratesUnknownFieldsAndVersion(t *testing.T) {
	content := `{"root":"root","version":99,"extra":true,"nodes":{"root":{"weird":1}}}`
	lock, err := ParseLock([]byte(content))
	if err != nil {
		t.Fatalf("ParseLock error = %v", err)
	}
	if lock.Version != 99 {
		t.Errorf("version = %d, want 99", lock.Version)
	}
}

func TestRootInputsNilSafe(t *testing.T) {
	var nilLock *Lock
	if got := nilLock.RootInputs(); got != nil {
		t.Errorf("nil lock RootInputs = %+v, want nil", got)
	}
	// A lock whose root node has no inputs returns nil, not a panic.
	lock := &Lock{Root: "root", Nodes: map[string]Node{"root": {}}}
	if got := lock.RootInputs(); got != nil {
		t.Errorf("no-inputs RootInputs = %+v, want nil", got)
	}
}
