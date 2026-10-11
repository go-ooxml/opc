// Copyright (c) the go-ooxml authors.
// SPDX-License-Identifier: BSD-3-Clause

// Package opc reads an Open Packaging Conventions container: the ZIP, the
// content types and the relationships that docx, xlsx, pptx and xps are all
// built on.
//
// It is the layer below a format reader. What it knows is how to find a part
// and what a part says it is; what the parts MEAN is WordprocessingML's
// business, or SpreadsheetML's, or PresentationML's.
//
// # Nothing here is found by its path
//
// ⛔ The main part of a package is reached through a RELATIONSHIP, never by
// guessing a filename. Measured on files LibreOffice wrote, the one
// relationship type that names it points at three different places:
//
//	.docx → word/document.xml
//	.xlsx → xl/workbook.xml
//	.pptx → ppt/presentation.xml
//
// A reader that hardcoded "word/document.xml" would work on most Word
// documents and on nothing else, and would still fail on the Word documents
// whose producer chose another name — which the standard allows.
package opc

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strings"
)

// The relationship types this package knows by name. A type is the whole URI,
// and it is compared with [SameRelType] rather than with ==.
const (
	// RelOfficeDocument names the package's main part: the Word document, the
	// Excel workbook, the PowerPoint presentation.
	RelOfficeDocument = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument"
	// RelCoreProperties names the Dublin Core metadata part.
	RelCoreProperties = "http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties"
)

// SameRelType says whether two relationship type URIs mean the same thing.
//
// ⛔ It is NOT a string comparison, and this is measured rather than cautious.
// In one .xlsx that LibreOffice wrote, the core-properties relationship type
// came out as
//
//	http://schemas.openxmlformats.org/officedocument/2006/relationships/...
//
// with "officedocument" in lower case, while the .docx and .pptx from the same
// program, the same version and the same minute wrote "officeDocument". A
// producer is inconsistent about the case of its own URIs, so a reader that
// compares them exactly works on two files out of three.
func SameRelType(a, b string) bool { return strings.EqualFold(a, b) }

// A Rel is one relationship: a named edge from a part (or from the package) to
// somewhere else.
type Rel struct {
	ID     string
	Type   string
	Target string
	// External is true when Target is a URI rather than a part of this
	// package. ⛔ It must be checked before Target is used as a part name: an
	// external target is a link to the web, and treating it as a path is how a
	// reader ends up asking the package for "https:/example.com/x".
	External bool
}

// A Package is an OPC container opened for reading. It is safe for concurrent
// reads.
type Package struct {
	zr       *zip.Reader
	parts    map[string]*zip.File // by part name, without the leading slash
	lower    map[string]string    // lower-cased part name → the real one
	defaults map[string]string    // extension (lower case) → content type
	override map[string]string    // part name → content type
	limits   Limits
}

// Open reads a package from bytes.
func Open(b []byte, lim Limits) (*Package, error) {
	lim = lim.orDefaults()
	if int64(len(b)) > lim.MaxPackageBytes {
		return nil, errTooLarge{what: "the package", want: int64(len(b)), limit: lim.MaxPackageBytes}
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, err
	}
	if n := len(zr.File); n > lim.MaxParts {
		return nil, errTooLarge{what: "the package", want: int64(n), limit: int64(lim.MaxParts), entries: true}
	}
	p := &Package{
		zr:       zr,
		parts:    make(map[string]*zip.File, len(zr.File)),
		lower:    make(map[string]string, len(zr.File)),
		defaults: map[string]string{},
		override: map[string]string{},
		limits:   lim,
	}
	var total uint64
	for _, f := range zr.File {
		name, ok := cleanPartName(f.Name)
		if !ok {
			// ⛔ Skipped rather than refused. A ZIP can carry a directory
			// entry, a name that climbs out of the package, or an absolute
			// path; none of them is a part, and refusing the whole package
			// because of one would make a reader that fails on files every
			// other reader opens.
			continue
		}
		total += f.UncompressedSize64
		p.parts[name] = f
		p.lower[strings.ToLower(name)] = name
	}
	if total > uint64(lim.MaxUncompressedBytes) {
		return nil, errTooLarge{what: "the package, opened", want: int64(total), limit: lim.MaxUncompressedBytes}
	}
	if err := p.readContentTypes(); err != nil {
		return nil, err
	}
	return p, nil
}

// cleanPartName turns a ZIP entry name into a part name, and says whether it
// is one at all.
//
// ⛔ A name that climbs above the package is refused rather than cleaned into
// something harmless. This package never writes a file, so there is no
// zip-slip to suffer here — but it hands part names to its callers, and a
// caller that joins one onto a directory would. Refusing is the only answer
// that cannot be got wrong downstream.
func cleanPartName(name string) (string, bool) {
	if name == "" || strings.HasSuffix(name, "/") {
		return "", false // a directory entry is not a part
	}
	// Backslashes appear in ZIPs written on Windows by programs that should
	// know better; the separator in a part name is a slash.
	name = strings.ReplaceAll(name, `\`, "/")
	name = strings.TrimPrefix(name, "/")
	clean := path.Clean(name)
	if clean != name || clean == "." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}

func (p *Package) readContentTypes() error {
	b, err := p.Part("[Content_Types].xml")
	if err != nil {
		// ⛔ Not an error. A package without [Content_Types].xml is malformed,
		// and every real reader opens it anyway: the parts are all still
		// there and a format reader asks for them by name. Refusing here
		// would turn "I cannot tell you what this part is" into "I cannot
		// read this file".
		return nil
	}
	dec := xml.NewDecoder(bytes.NewReader(b))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("[Content_Types].xml: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "Default":
			ext, ct := attr(se, "Extension"), attr(se, "ContentType")
			if ext != "" {
				p.defaults[strings.ToLower(ext)] = ct
			}
		case "Override":
			name, ct := attr(se, "PartName"), attr(se, "ContentType")
			if clean, ok := cleanPartName(name); ok {
				p.override[clean] = ct
			}
		}
	}
}

// Part returns a part's bytes.
//
// The lookup is exact first and case-insensitive second, because OPC compares
// part names without regard to case while a ZIP does not: a package whose
// [Content_Types].xml says "/word/Document.xml" and whose entry is
// "word/document.xml" is one file, and both names have to find it.
func (p *Package) Part(name string) ([]byte, error) {
	clean, ok := cleanPartName(name)
	if !ok {
		return nil, fmt.Errorf("opc: %q is not a part name", name)
	}
	f := p.parts[clean]
	if f == nil {
		if real, ok := p.lower[strings.ToLower(clean)]; ok {
			f = p.parts[real]
		}
	}
	if f == nil {
		return nil, fmt.Errorf("opc: the package has no part %q", clean)
	}
	if f.UncompressedSize64 > uint64(p.limits.MaxPartBytes) {
		return nil, errTooLarge{what: clean, want: int64(f.UncompressedSize64), limit: p.limits.MaxPartBytes}
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	// ⛔ Bounded by the ceiling and not by the declared size: a ZIP's declared
	// size is a number in somebody's file, and the whole point of reading it
	// first is that it may be a lie in either direction.
	return io.ReadAll(io.LimitReader(rc, p.limits.MaxPartBytes+1))
}

// Has says whether a part is in the package, without reading it.
func (p *Package) Has(name string) bool {
	clean, ok := cleanPartName(name)
	if !ok {
		return false
	}
	if _, ok := p.parts[clean]; ok {
		return true
	}
	_, ok = p.lower[strings.ToLower(clean)]
	return ok
}

// Parts lists every part in the package, in the order the ZIP holds them.
func (p *Package) Parts() []string {
	out := make([]string, 0, len(p.parts))
	for _, f := range p.zr.File {
		if name, ok := cleanPartName(f.Name); ok {
			out = append(out, name)
		}
	}
	return out
}

// ContentType says what a part declares itself to be: its Override if it has
// one, otherwise the Default for its extension, otherwise "".
func (p *Package) ContentType(name string) string {
	clean, ok := cleanPartName(name)
	if !ok {
		return ""
	}
	if ct, ok := p.override[clean]; ok {
		return ct
	}
	return p.defaults[strings.ToLower(strings.TrimPrefix(path.Ext(clean), "."))]
}

// relsPartFor is where a part's relationships live: beside it, in a _rels
// directory, under its own name with .rels added. The package's own
// relationships are at _rels/.rels, which is the same rule with an empty name.
func relsPartFor(part string) string {
	dir, base := path.Split(part)
	return path.Join(dir, "_rels", base+".rels")
}

// Rels returns the relationships of a part. An empty name asks for the
// package's own, at _rels/.rels.
//
// A part with no relationships part has no relationships, which is not an
// error: most parts of most packages have none.
func (p *Package) Rels(part string) ([]Rel, error) {
	name := relsPartFor(part)
	if !p.Has(name) {
		return nil, nil
	}
	b, err := p.Part(name)
	if err != nil {
		return nil, err
	}
	dec := xml.NewDecoder(bytes.NewReader(b))
	var out []Rel
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "Relationship" {
			continue
		}
		if len(out) >= p.limits.MaxRels {
			return nil, errTooLarge{what: name, want: int64(len(out) + 1), limit: int64(p.limits.MaxRels), entries: true}
		}
		out = append(out, Rel{
			ID:       attr(se, "Id"),
			Type:     attr(se, "Type"),
			Target:   attr(se, "Target"),
			External: strings.EqualFold(attr(se, "TargetMode"), "External"),
		})
	}
}

// RelOfType returns the first relationship of a part with the given type.
//
// ⛔ "First", and the order is the file's. A package may name two parts with
// the same relationship type — several slides, several worksheets — and for
// those the caller wants [Package.Rels] and all of them. This is for the types
// of which there is one: the main part, the core properties.
func (p *Package) RelOfType(part, relType string) (Rel, bool, error) {
	rels, err := p.Rels(part)
	if err != nil {
		return Rel{}, false, err
	}
	for _, r := range rels {
		if SameRelType(r.Type, relType) {
			return r, true, nil
		}
	}
	return Rel{}, false, nil
}

// Resolve turns a relationship's target into a part name of this package.
//
// A target is relative to the DIRECTORY of the part that declared it — from
// word/document.xml, "styles.xml" is word/styles.xml and "../docProps/app.xml"
// is docProps/app.xml. A target beginning with a slash is from the root of the
// package instead.
//
// ⛔ An external target is an error rather than a path. "http://example.com/x"
// cleaned into a part name becomes "http:/example.com/x", which is a lookup
// that fails for a reason that has nothing to do with what went wrong.
func (p *Package) Resolve(part string, r Rel) (string, error) {
	if r.External {
		return "", fmt.Errorf("opc: relationship %q points outside the package, at %q", r.ID, r.Target)
	}
	if r.Target == "" {
		return "", fmt.Errorf("opc: relationship %q has no target", r.ID)
	}
	target := strings.ReplaceAll(r.Target, `\`, "/")
	var joined string
	if strings.HasPrefix(target, "/") {
		joined = strings.TrimPrefix(target, "/")
	} else {
		joined = path.Join(path.Dir(part), target)
	}
	clean, ok := cleanPartName(joined)
	if !ok {
		return "", fmt.Errorf("opc: relationship %q points at %q, which is not inside the package", r.ID, r.Target)
	}
	return clean, nil
}

// Main returns the name of the package's main part: the Word document, the
// Excel workbook, the PowerPoint presentation.
//
// ⛔ This is the whole reason this package exists. The part is found through
// the officeDocument relationship in _rels/.rels, and the three formats put it
// in three different places — word/document.xml, xl/workbook.xml,
// ppt/presentation.xml — so a path cannot be guessed even if a producer always
// chose the usual one, which it need not.
func (p *Package) Main() (string, error) {
	r, ok, err := p.RelOfType("", RelOfficeDocument)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("opc: the package names no main part: " +
			"_rels/.rels has no officeDocument relationship")
	}
	return p.Resolve("", r)
}

func attr(se xml.StartElement, local string) string {
	for _, a := range se.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}
