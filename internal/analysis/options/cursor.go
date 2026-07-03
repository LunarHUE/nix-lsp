package options

// cursor.go exposes a minimal read-only descent over the option trie for the
// dataset unknown-option diagnostic (internal/analysis/datadiag). That walk moves
// along a binding attrpath segment by segment and must, at each node, tell a group
// (interior, no doc) from an option leaf, spot a "<name>"/"*" wildcard that
// swallows an arbitrary instance segment, and read the concrete child names to
// suggest a correction. Lookup and Children answer whole-path questions with
// wildcard backtracking, which is the wrong shape for a deterministic
// step-by-step walk, so this Cursor is offered alongside them.

// Cursor is a read-only position at one node of the option trie. Its zero value
// is unusable; obtain the root with Index.Root and move with Child or Wildcard.
type Cursor struct {
	n *node
}

// Root returns a cursor at the trie root, reporting ok=false when the index is
// nil or unbuilt. nil-receiver safe.
func (ix *Index) Root() (Cursor, bool) {
	if ix == nil || ix.root == nil {
		return Cursor{}, false
	}
	return Cursor{n: ix.root}, true
}

// HasDoc reports whether this node documents a concrete option, i.e. it is an
// option leaf or a freeform/attrsOf option whose value shape is open. The
// unknown-option walk stops here: segments beyond a documented option are normal
// values, never unknown options.
func (c Cursor) HasDoc() bool {
	return c.n != nil && c.n.doc != nil
}

// Wildcard returns the cursor reached through this node's instance-name
// placeholder child ("<name>", falling back to "*"), reporting ok=false when the
// node has neither. Such a node accepts an arbitrary instance segment (a service
// name, a user name), so the walk descends through it without validating that
// segment against a fixed vocabulary.
func (c Cursor) Wildcard() (Cursor, bool) {
	if c.n == nil {
		return Cursor{}, false
	}
	if child, ok := c.n.children["<name>"]; ok {
		return Cursor{n: child}, true
	}
	if child, ok := c.n.children["*"]; ok {
		return Cursor{n: child}, true
	}
	return Cursor{}, false
}

// Child returns the cursor for the concrete (non-placeholder) child named seg,
// reporting ok=false when no such child exists.
func (c Cursor) Child(seg string) (Cursor, bool) {
	if c.n == nil || seg == "<name>" || seg == "*" {
		return Cursor{}, false
	}
	child, ok := c.n.children[seg]
	if !ok {
		return Cursor{}, false
	}
	return Cursor{n: child}, true
}

// ChildNames returns this node's concrete child segment names (the "<name>"/"*"
// placeholders excluded), unsorted. The unknown-option walk uses them as the
// candidate set for a did-you-mean suggestion.
func (c Cursor) ChildNames() []string {
	if c.n == nil || len(c.n.children) == 0 {
		return nil
	}
	names := make([]string, 0, len(c.n.children))
	for name := range c.n.children {
		if name == "<name>" || name == "*" {
			continue
		}
		names = append(names, name)
	}
	return names
}
