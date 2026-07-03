package server

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/facts"
	"github.com/wesleybaldwin/nix-lsp/internal/analysis/flake"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
	"github.com/wesleybaldwin/nix-lsp/internal/vfs"
)

// Hover is the LSP textDocument/hover result: rendered markdown plus the range
// the hover applies to.
type Hover struct {
	Contents MarkupContent `json:"contents"`
	Range    protocolRange `json:"range"`
}

// MarkupContent is an LSP markup string; here always markdown.
type MarkupContent struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// hover answers textDocument/hover. It serves flake-input hovers for the
// workspace root flake.nix and NixOS option-documentation hovers for any .nix
// file; every other position returns null.
func (h *Handler) hover(ctx context.Context, params json.RawMessage) (any, error) {
	var decoded textDocumentPositionParams
	if err := json.Unmarshal(params, &decoded); err != nil {
		return nil, nil
	}
	pos := syntax.Position{Line: decoded.Position.Line, Character: decoded.Position.Character}

	// Flake-input hover fires only for the workspace root flake.nix; keep it first
	// so input hovers there never regress. A miss falls through to option hover.
	if fileID, ok := h.rootFlakeInputForURI(decoded.TextDocument.URI); ok {
		if model, err := facts.FlakeModel(ctx, h.memo, fileID); err == nil && model != nil {
			if lock, hasLock, err := facts.FlakeLock(ctx, h.memo); err == nil {
				if hover := flakeHoverAt(model, lock, hasLock, pos); hover != nil {
					return hover, nil
				}
			}
		}
	}

	// Path-literal hover reports where a static path expression resolves. A path
	// literal never coincides with a package select, option path, or bound
	// identifier, so its position among those hovers is behavior-neutral; it sits
	// just after the root-flake input hover so the one genuine overlap (a path used
	// as an input url on the root flake.nix) still resolves to the input hover.
	if hover := h.pathHover(ctx, decoded.TextDocument.URI, pos); hover != nil {
		return hover, nil
	}

	// Package-version hover for a `pkgs.<attr>` select, then NixOS
	// option-documentation hover. The two cannot both match a position: option
	// paths never start at a `pkgs` select base.
	if hover := h.packageHover(ctx, decoded.TextDocument.URI, pos); hover != nil {
		return hover, nil
	}
	if hover := h.optionHover(ctx, decoded.TextDocument.URI, pos); hover != nil {
		return hover, nil
	}
	// Binding-value hover is the final fallback: it shows what a locally bound
	// identifier is bound to. It must run last so the flake, package, and option
	// hovers above always win a shared position.
	if hover := h.valueHover(ctx, decoded.TextDocument.URI, pos); hover != nil {
		return hover, nil
	}
	return nil, nil
}

// flakeDefinition resolves gd on a follows target or an outputs formal in the
// root flake.nix to the matching input's declaration, or nil.
func (h *Handler) flakeDefinition(ctx context.Context, fileID, uri string, pos syntax.Position) *Location {
	if !h.isRootFlakeURI(uri) {
		return nil
	}
	model, err := facts.FlakeModel(ctx, h.memo, fileID)
	if err != nil || model == nil {
		return nil
	}
	return flakeDefinitionAt(model, uri, pos)
}

// rootFlakeInputForURI resolves uri, registers its file input on the memo
// engine, and returns the fileID only when the file is the workspace root
// flake.nix. It returns ok=false for any other file so flake features never fire
// elsewhere.
func (h *Handler) rootFlakeInputForURI(uri string) (string, bool) {
	path, err := vfs.URIToPath(uri)
	if err != nil {
		return "", false
	}
	file, err := h.vfs.Snapshot().ReadFile(path)
	if err != nil {
		return "", false
	}
	if !h.isRootFlakePath(file.Path) {
		return "", false
	}
	fileID := facts.FileID(file.Path, file.Hash)
	facts.SetFileInput(h.memo, fileID, facts.FileInput{Path: file.Path, Content: file.Content})
	return fileID, true
}

// isRootFlakeURI reports whether uri resolves to the workspace root flake.nix.
func (h *Handler) isRootFlakeURI(uri string) bool {
	path, err := vfs.URIToPath(uri)
	if err != nil {
		return false
	}
	file, err := h.vfs.Snapshot().ReadFile(path)
	if err != nil {
		return false
	}
	return h.isRootFlakePath(file.Path)
}

// isRootFlakePath reports whether the (normalized) path is the workspace root
// flake.nix, mirroring the same check the diagnostics query applies.
func (h *Handler) isRootFlakePath(path string) bool {
	workspace, ok := h.Workspace()
	if !ok || workspace.Root == "" {
		return false
	}
	return path == filepath.Join(workspace.Root, "flake.nix")
}

// flakeHoverAt returns the hover for the input under pos, or nil. It matches an
// input name or url range directly, and a follows target (top-level or nested)
// by describing the input it points at.
func flakeHoverAt(file *flake.File, lock *flake.Lock, hasLock bool, pos syntax.Position) *Hover {
	if file == nil {
		return nil
	}
	byName := inputsByName(file)

	for _, in := range file.Inputs {
		if rangeContainsPosition(in.NameRange, pos) {
			return inputHover(in, lock, hasLock, in.NameRange)
		}
		if in.HasURL && rangeContainsPosition(in.URLRange, pos) {
			return inputHover(in, lock, hasLock, in.URLRange)
		}
	}

	for _, in := range file.Inputs {
		if in.HasTopFollows && rangeContainsPosition(in.TopFollowsRange, pos) {
			return followsTargetHover(in.TopFollows, in.TopFollowsRange, byName, lock, hasLock)
		}
		for _, edge := range in.Follows {
			if rangeContainsPosition(edge.TargetRange, pos) {
				return followsTargetHover(edge.Target, edge.TargetRange, byName, lock, hasLock)
			}
		}
	}
	return nil
}

// followsTargetHover renders the hover for the input named by target's first
// segment, anchored on r. A target that names no declared input yields nil (the
// dangling-follows diagnostic already covers it).
func followsTargetHover(target string, r syntax.Range, byName map[string]*flake.Input, lock *flake.Lock, hasLock bool) *Hover {
	in, ok := byName[firstFollowsSegment(target)]
	if !ok {
		return nil
	}
	return inputHover(in, lock, hasLock, r)
}

// inputHover renders the markdown hover for one input, anchored on r. The lock
// section is included only when a lock is present.
func inputHover(in *flake.Input, lock *flake.Lock, hasLock bool, r syntax.Range) *Hover {
	var b strings.Builder
	fmt.Fprintf(&b, "**input** `%s`", in.Name)
	if in.HasURL {
		fmt.Fprintf(&b, "\n- url: `%s`", in.URL)
	}
	if in.HasTopFollows {
		fmt.Fprintf(&b, "\n- follows: `%s`", in.TopFollows)
	}
	if in.Flake != nil && !*in.Flake {
		b.WriteString("\n- flake = false")
	}
	if hasLock && lock != nil {
		writeLockSection(&b, in.Name, lock)
	}
	return &Hover{
		Contents: MarkupContent{Kind: "markdown", Value: b.String()},
		Range:    toProtocolRange(r),
	}
}

// writeLockSection appends the lock detail for input name. It resolves the root
// lock ref: a direct key renders the locked source, rev and lastModified; a
// follows path is reported but not chased. An input absent from the lock's root
// inputs is flagged.
func writeLockSection(b *strings.Builder, name string, lock *flake.Lock) {
	ref, ok := lock.RootInputs()[name]
	if !ok {
		b.WriteString("\n- not in flake.lock")
		return
	}
	if len(ref.Follows) > 0 {
		fmt.Fprintf(b, "\n- locked via follows: `%s`", strings.Join(ref.Follows, "/"))
		return
	}
	if ref.Key == "" {
		return
	}
	node, ok := lock.Nodes[ref.Key]
	if !ok {
		return
	}
	// A flake.lock pins a revision, not a version; the original ref is the honest
	// answer to "what does this input track" when the pin names a channel or tag
	// (e.g. `nixos-25.05`, `v1.17.4`).
	if node.Original != nil && node.Original.Ref != "" {
		fmt.Fprintf(b, "\n- ref: `%s`", node.Original.Ref)
	}
	if node.Locked == nil {
		return
	}
	locked := node.Locked
	if src := lockedSource(locked); src != "" {
		fmt.Fprintf(b, "\n- locked: `%s`", src)
	}
	if locked.Rev != "" {
		rev := locked.Rev
		if len(rev) > 12 {
			rev = rev[:12]
		}
		fmt.Fprintf(b, "\n- rev: `%s`", rev)
	}
	if locked.LastModified > 0 {
		fmt.Fprintf(b, "\n- lastModified: %s", time.Unix(locked.LastModified, 0).UTC().Format("2006-01-02"))
	}
}

// lockedSource renders a locked source as `<type>:<owner>/<repo>` when owner and
// repo are present, else the raw URL, else empty.
func lockedSource(src *flake.SourceRef) string {
	if src.Owner != "" && src.Repo != "" {
		return fmt.Sprintf("%s:%s/%s", src.Type, src.Owner, src.Repo)
	}
	return src.URL
}

// flakeDefinitionAt resolves a follows target or an outputs formal under pos to
// the declaration of the input it names, or nil.
func flakeDefinitionAt(file *flake.File, uri string, pos syntax.Position) *Location {
	byName := inputsByName(file)

	for _, in := range file.Inputs {
		if in.HasTopFollows && rangeContainsPosition(in.TopFollowsRange, pos) {
			return inputNameLocation(byName, in.TopFollows, uri)
		}
		for _, edge := range in.Follows {
			if rangeContainsPosition(edge.TargetRange, pos) {
				return inputNameLocation(byName, edge.Target, uri)
			}
		}
	}

	if file.Outputs != nil {
		for name, r := range file.Outputs.Formals {
			if name == "self" || !rangeContainsPosition(r, pos) {
				continue
			}
			if in, ok := byName[name]; ok {
				return &Location{URI: uri, Range: toProtocolRange(in.NameRange)}
			}
			return nil
		}
	}
	return nil
}

// inputNameLocation returns the declaration location of the input named by
// target's first segment, or nil.
func inputNameLocation(byName map[string]*flake.Input, target, uri string) *Location {
	if in, ok := byName[firstFollowsSegment(target)]; ok {
		return &Location{URI: uri, Range: toProtocolRange(in.NameRange)}
	}
	return nil
}

// inputsByName indexes a flake file's inputs by name.
func inputsByName(file *flake.File) map[string]*flake.Input {
	byName := make(map[string]*flake.Input, len(file.Inputs))
	for _, in := range file.Inputs {
		byName[in.Name] = in
	}
	return byName
}

// firstFollowsSegment returns the first slash-separated segment of a follows
// target, which names the top-level input the target resolves through.
func firstFollowsSegment(target string) string {
	if i := strings.IndexByte(target, '/'); i >= 0 {
		return target[:i]
	}
	return target
}
