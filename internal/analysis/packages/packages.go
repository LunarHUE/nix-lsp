// Package packages parses the official NixOS channel packages.json artifact into
// an attr-keyed index and renders per-package documentation for hover. Like the
// rest of the analysis code it is a pure function of its inputs: it performs no
// network access and no nix execution, reading only from the io.Reader it is
// handed. The artifact is enormous (hundreds of MB decompressed), so ParseStream
// consumes it as a token stream and never holds the whole document in memory;
// only the small trimmed form and the tiny per-package Doc values are retained.
package packages

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Doc is the documentation for one package, reduced to the few fields hover
// shows. Attr is the attribute-path key it was found under; the remaining fields
// come from the artifact entry and may each be empty.
type Doc struct {
	Attr        string
	Pname       string
	Version     string
	Description string
	Homepage    string
}

// Index is a lookup over parsed packages keyed by attribute path (the artifact's
// own dotted keys, e.g. "python312Packages.requests", verbatim).
type Index struct {
	docs map[string]*Doc
}

// rawPackage mirrors the on-disk shape of a single packages.json entry, decoding
// only the handful of fields hover needs. Homepage is kept raw because the data
// encodes it as either a string or a list of strings.
type rawPackage struct {
	Name    string `json:"name"`
	Pname   string `json:"pname"`
	Version string `json:"version"`
	Meta    struct {
		Description string          `json:"description"`
		Homepage    json.RawMessage `json:"homepage"`
	} `json:"meta"`
}

// ParseStream decodes the official packages.json artifact from r into an Index
// using a JSON token stream, so the full (hundreds-of-MB) document is never held
// in memory: it drains every top-level key except "packages", and within
// "packages" it decodes one small entry value at a time. A malformed individual
// entry is skipped rather than failing the whole parse. It returns an error only
// when the outer document is not a JSON object or the stream is truncated.
func ParseStream(r io.Reader) (*Index, error) {
	dec := json.NewDecoder(r)

	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("packages: top level is not a JSON object")
	}

	ix := &Index{docs: make(map[string]*Doc)}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("packages: non-string top-level key")
		}
		if key != "packages" {
			// version and any other sibling key: drain without materializing.
			if err := skipValue(dec); err != nil {
				return nil, err
			}
			continue
		}
		if err := parsePackages(dec, ix); err != nil {
			return nil, err
		}
	}
	return ix, nil
}

// parsePackages consumes the "packages" object from the stream, decoding one
// entry at a time into a small struct. Each entry's bytes are grabbed as a lone
// RawMessage so a malformed (non-object) value can be skipped while the decoder
// stays correctly positioned for the next entry.
func parsePackages(dec *json.Decoder, ix *Index) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("packages: \"packages\" is not a JSON object")
	}

	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		attr, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("packages: non-string package key")
		}

		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		var e rawPackage
		if err := json.Unmarshal(raw, &e); err != nil {
			// A malformed (non-object) entry is skipped, not fatal.
			continue
		}
		ix.docs[attr] = &Doc{
			Attr:        attr,
			Pname:       e.Pname,
			Version:     e.Version,
			Description: e.Meta.Description,
			Homepage:    firstHomepage(e.Meta.Homepage),
		}
	}

	// Consume the closing '}' of the packages object.
	if _, err := dec.Token(); err != nil {
		return err
	}
	return nil
}

// skipValue drains the next JSON value from the stream. A scalar is a single
// token; a container is drained by matching open and close delimiters. It never
// buffers the value, so a large sibling value would still stream past.
func skipValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if _, ok := tok.(json.Delim); !ok {
		return nil
	}
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := tok.(json.Delim); ok {
			if d == '{' || d == '[' {
				depth++
			} else {
				depth--
			}
		}
	}
	return nil
}

// firstHomepage normalizes the raw homepage value, which the data encodes as
// either a string or a list of strings. It returns the string, the first
// non-empty list element, or "" when absent or an unexpected shape.
func firstHomepage(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		for _, v := range list {
			if v != "" {
				return v
			}
		}
	}
	return ""
}

// Lookup resolves an attribute path to its Doc.
func (ix *Index) Lookup(attr string) (*Doc, bool) {
	if ix == nil || ix.docs == nil {
		return nil, false
	}
	doc, ok := ix.docs[attr]
	return doc, ok
}

// Len reports the number of packages held in the index.
func (ix *Index) Len() int {
	if ix == nil {
		return 0
	}
	return len(ix.docs)
}

// trimmedDoc is the compact on-disk cache shape for one package: only the fields
// hover renders, under short keys to keep the cache (tens of MB) small.
type trimmedDoc struct {
	P string `json:"p,omitempty"`
	V string `json:"v,omitempty"`
	D string `json:"d,omitempty"`
	H string `json:"h,omitempty"`
}

// MarshalTrimmed serializes the index to the compact cache form
// {attr: {"p":pname,"v":version,"d":description,"h":homepage}} as compact JSON.
// This is what the loader caches, never the raw multi-hundred-MB artifact.
func (ix *Index) MarshalTrimmed() ([]byte, error) {
	m := make(map[string]trimmedDoc, len(ix.docs))
	for attr, d := range ix.docs {
		m[attr] = trimmedDoc{P: d.Pname, V: d.Version, D: d.Description, H: d.Homepage}
	}
	return json.Marshal(m)
}

// ParseTrimmed rebuilds an Index from the compact cache form produced by
// MarshalTrimmed.
func ParseTrimmed(data []byte) (*Index, error) {
	var m map[string]trimmedDoc
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	ix := &Index{docs: make(map[string]*Doc, len(m))}
	for attr, t := range m {
		ix.docs[attr] = &Doc{
			Attr:        attr,
			Pname:       t.P,
			Version:     t.V,
			Description: t.D,
			Homepage:    t.H,
		}
	}
	return ix, nil
}

// Markdown renders the Doc as hover markdown: the package name in bold followed
// by its version in backticks, the description, and a Homepage line, separated by
// blank lines. The name falls back to the attribute path when pname is empty; the
// version, description, and homepage lines are omitted when empty.
func (d *Doc) Markdown() string {
	name := d.Pname
	if name == "" {
		name = d.Attr
	}
	head := "**" + name + "**"
	if d.Version != "" {
		head += " `" + d.Version + "`"
	}
	blocks := []string{head}

	if desc := strings.TrimRight(d.Description, " \t\r\n"); desc != "" {
		blocks = append(blocks, desc)
	}
	if d.Homepage != "" {
		blocks = append(blocks, "*Homepage:* "+d.Homepage)
	}
	return strings.Join(blocks, "\n\n")
}
