package opc

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"path"
	"sort"
	"strings"
)

// contentTypes is the part that says what every other part is. Its name is
// fixed by the standard, and it is the one name a caller may not use.
const contentTypes = "[Content_Types].xml"

// A NewPart is one part of a package being built: where it goes, what it is,
// and what is in it.
type NewPart struct {
	// Name is the part name, with or without a leading slash — "/" is how the
	// content types part spells one and the ZIP does not, and a caller should
	// not have to remember which this is.
	Name string

	// ContentType is what the content types part will say this is. It may be
	// empty for a part whose extension already has a Default: every .rels and
	// every .xml part does, which is why most packages override only a few.
	ContentType string

	// Data is the part's bytes.
	Data []byte
}

// Build writes an OPC container: the parts, the content types that say what
// each is, and the relationships that say how anything is found.
//
// rels maps a part name to the relationships that start AT it. The empty name
// is the package's own, which goes in _rels/.rels and is where a reader
// starts — see [Package.Main]. The relationships parts are written for you, in
// the place [Package.Rels] looks for them, because that place is a rule and
// not a choice.
//
// ⛔ It refuses rather than writing a container that cannot be read back:
//
//	a part name with ".." in it      which escapes the package
//	two parts with the same name     where the second would be unreachable
//	[Content_Types].xml, or a .rels  which this writes itself, and a second
//	                                 copy would contradict the first
//	a relationship from a part       that is not in the package, which is a
//	                                 dangling edge nothing can resolve
//	no package relationships         because a package nothing points into has
//	                                 no main part, and every reader starts there
//
// Each of those is a container that opens and then fails to say anything, and
// the failure turns up in whatever program the file was written for rather
// than here.
func Build(parts []NewPart, rels map[string][]Rel) ([]byte, error) {
	clean, err := cleanParts(parts)
	if err != nil {
		return nil, err
	}
	if len(rels[""]) == 0 {
		return nil, fmt.Errorf("opc: a package with no relationships of its own has no main part, and that is where every reader starts")
	}
	have := make(map[string]bool, len(clean))
	for _, p := range clean {
		have[p.Name] = true
	}
	for _, from := range sorted(rels) {
		if from != "" && !have[from] {
			return nil, fmt.Errorf("opc: relationships from %q, which is not in the package", from)
		}
	}

	// ⛔ NOTHING BELOW IS CHECKED, and nothing below can fail. Every write
	// goes into a bytes.Buffer, whose Write is documented never to return an
	// error, and archive/zip refuses only a name it has already used or a
	// writer already closed — a duplicate is refused above and this closes
	// once, at the end. Plumbing those errors through added SIXTEEN branches
	// no test could reach, which is sixteen places a reader has to wonder
	// what happens and sixteen the coverage gate cannot account for.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	// The content types part goes FIRST. The standard does not require it and
	// a reader that scans the directory does not care, but one that streams
	// has to know what a part is before it reaches it, and this costs nothing.
	add(zw, contentTypes, contentTypesXML(clean))
	for _, from := range sorted(rels) {
		add(zw, relsPartFor(from), relsXML(rels[from]))
	}
	for _, p := range clean {
		add(zw, p.Name, p.Data)
	}
	zw.Close()
	return buf.Bytes(), nil
}

// cleanParts normalises the part names and refuses the packages that cannot
// be read back.
func cleanParts(parts []NewPart) ([]NewPart, error) {
	out := make([]NewPart, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, p := range parts {
		name, ok := cleanPartName(p.Name)
		if !ok {
			return nil, fmt.Errorf("opc: %q is not a part name", p.Name)
		}
		if name == contentTypes || strings.HasSuffix(strings.ToLower(name), ".rels") {
			return nil, fmt.Errorf("opc: %q is written for you, and a second copy would contradict the first", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("opc: %q is in the package twice, and the second one would be unreachable", name)
		}
		seen[name] = true
		p.Name = name
		out = append(out, p)
	}
	return out, nil
}

// sorted is the keys of a relationship map in a fixed order, so that building
// the same package twice gives the same bytes. A map walk is random, and a
// writer whose output changes between runs cannot be diffed, cached or signed.
func sorted(rels map[string][]Rel) []string {
	out := make([]string, 0, len(rels))
	for from := range rels {
		out = append(out, from)
	}
	sort.Strings(out)
	return out
}

// add writes one part into the archive. See the note in [Build] for why it
// returns nothing.
func add(zw *zip.Writer, name string, data []byte) {
	w, _ := zw.Create(name)
	w.Write(data)
}

// contentTypesXML writes the part that says what every other part is.
//
// The two Defaults are the ones every OPC package has: .rels for the
// relationship parts this writes itself, and .xml because almost everything in
// an OOXML package is XML. A part whose ContentType is set gets an Override,
// which is how a .xml part says it is a Word document rather than just XML.
func contentTypesXML(parts []NewPart) []byte {
	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString(`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">`)
	b.WriteString(`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>`)
	b.WriteString(`<Default Extension="xml" ContentType="application/xml"/>`)
	// A Default for each extension that is not XML, written once however many
	// parts share it, taken from the first part that names one.
	var exts []string
	byExt := map[string]string{}
	for _, p := range parts {
		ext := extOf(p.Name)
		if p.ContentType == "" || ext == "" || ext == "xml" || ext == "rels" {
			continue
		}
		if _, there := byExt[ext]; !there {
			byExt[ext] = p.ContentType
			exts = append(exts, ext)
		}
	}
	sort.Strings(exts)
	for _, ext := range exts {
		b.WriteString(`<Default Extension="`)
		esc(&b, ext)
		b.WriteString(`" ContentType="`)
		esc(&b, byExt[ext])
		b.WriteString(`"/>`)
	}
	// An Override for every part the Defaults do not already cover.
	for _, p := range parts {
		if p.ContentType == "" || byExt[extOf(p.Name)] == p.ContentType {
			continue
		}
		b.WriteString(`<Override PartName="/`)
		esc(&b, p.Name)
		b.WriteString(`" ContentType="`)
		esc(&b, p.ContentType)
		b.WriteString(`"/>`)
	}
	b.WriteString(`</Types>`)
	return b.Bytes()
}

// extOf is a part name's extension, lower-cased and without its dot.
func extOf(name string) string {
	return strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
}

// relsXML writes one relationships part.
func relsXML(list []Rel) []byte {
	var b bytes.Buffer
	b.WriteString(xml.Header)
	b.WriteString(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	for _, r := range list {
		b.WriteString(`<Relationship Id="`)
		esc(&b, r.ID)
		b.WriteString(`" Type="`)
		esc(&b, r.Type)
		b.WriteString(`" Target="`)
		esc(&b, r.Target)
		b.WriteString(`"`)
		if r.External {
			// ⛔ Without this, a target that is a URI is read back as a part
			// name and the reader goes looking for "https:/example.com/x"
			// inside the package. [Rel.External] says so for the same reason.
			b.WriteString(` TargetMode="External"`)
		}
		b.WriteString(`/>`)
	}
	b.WriteString(`</Relationships>`)
	return b.Bytes()
}

// esc appends text escaped for XML.
//
// [xml.EscapeText] returns whatever error the writer it was given returned,
// and a [bytes.Buffer] never returns one. So the error is dropped HERE, in one
// place with the reason beside it, rather than checked at each of the seven
// calls above where no test could make it happen.
func esc(b *bytes.Buffer, s string) {
	_ = xml.EscapeText(b, []byte(s))
}
