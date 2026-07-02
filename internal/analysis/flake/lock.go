// Package flake models a flake.nix's inputs and the flake.lock file and emits
// conservative flake diagnostics. Like the rest of the analysis code it is a
// pure function of its inputs and never touches the filesystem or memo engine.
package flake

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Lock is a parsed flake.lock file.
type Lock struct {
	Version int
	Root    string
	Nodes   map[string]Node
}

// Node is a single entry in a flake.lock's nodes map.
type Node struct {
	Inputs   map[string]Ref `json:"inputs"`
	Locked   *SourceRef     `json:"locked"`
	Original *SourceRef     `json:"original"`
	Flake    *bool          `json:"flake"`
}

// Ref is a node's reference to another input. It is EITHER a direct node key
// (encoded as a JSON string) or a follows path (encoded as a JSON array).
type Ref struct {
	Key     string
	Follows []string
}

// UnmarshalJSON decodes a Ref from either a JSON string (a direct node key) or a
// JSON array of path segments (a follows path).
func (r *Ref) UnmarshalJSON(data []byte) error {
	var key string
	if err := json.Unmarshal(data, &key); err == nil {
		r.Key = key
		r.Follows = nil
		return nil
	}
	var follows []string
	if err := json.Unmarshal(data, &follows); err != nil {
		return err
	}
	r.Key = ""
	r.Follows = follows
	return nil
}

// SourceRef describes a locked or original flake source.
type SourceRef struct {
	Type         string `json:"type"`
	Owner        string `json:"owner"`
	Repo         string `json:"repo"`
	URL          string `json:"url"`
	Ref          string `json:"ref"`
	Rev          string `json:"rev"`
	NarHash      string `json:"narHash"`
	LastModified int64  `json:"lastModified"`
}

// ParseLock parses flake.lock content. It rejects input that lacks a nodes map
// or a root key present in that map, but tolerates unknown fields and any
// version so a newer lock format still parses.
func ParseLock(content []byte) (*Lock, error) {
	var raw struct {
		Version int                        `json:"version"`
		Root    string                     `json:"root"`
		Nodes   map[string]json.RawMessage `json:"nodes"`
	}
	if err := json.Unmarshal(content, &raw); err != nil {
		return nil, err
	}
	if len(raw.Nodes) == 0 {
		return nil, errors.New("flake.lock: no nodes")
	}
	if raw.Root == "" {
		return nil, errors.New("flake.lock: missing root")
	}
	if _, ok := raw.Nodes[raw.Root]; !ok {
		return nil, fmt.Errorf("flake.lock: root %q not present in nodes", raw.Root)
	}

	nodes := make(map[string]Node, len(raw.Nodes))
	for key, rawNode := range raw.Nodes {
		var node Node
		if err := json.Unmarshal(rawNode, &node); err != nil {
			return nil, err
		}
		nodes[key] = node
	}
	return &Lock{Version: raw.Version, Root: raw.Root, Nodes: nodes}, nil
}

// RootInputs returns the inputs of the lock's root node, or nil.
func (l *Lock) RootInputs() map[string]Ref {
	if l == nil {
		return nil
	}
	node, ok := l.Nodes[l.Root]
	if !ok {
		return nil
	}
	return node.Inputs
}
