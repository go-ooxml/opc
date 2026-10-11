// Copyright (c) the go-ooxml authors.
// SPDX-License-Identifier: BSD-3-Clause

package opc

import (
	"archive/zip"
	"bytes"
	"os"
	"strings"
	"testing"
)

// ⛔ These three files were WRITTEN BY LIBREOFFICE, not composed here. Two of
// the rules in this package exist only because a real file disagreed with the
// obvious reading of the standard, and one of them — the case of a
// relationship type URI — is a disagreement BETWEEN two of these three files,
// written by the same program in the same minute.
const (
	docxPath = "testdata/doc.docx"
	xlsxPath = "testdata/book.xlsx"
	pptxPath = "testdata/deck.pptx"
)

func read(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func open(t *testing.T, path string) *Package {
	t.Helper()
	p, err := Open(read(t, path), Limits{})
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return p
}

// ⛔ The reason this package exists, asserted on real files: the one
// relationship that names the main part points at three different places.
func TestTheMainPartIsFoundThroughARelationship(t *testing.T) {
	for path, want := range map[string]string{
		docxPath: "word/document.xml",
		xlsxPath: "xl/workbook.xml",
		pptxPath: "ppt/presentation.xml",
	} {
		got, err := open(t, path).Main()
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if got != want {
			t.Errorf("%s: Main() = %q, want %q", path, got, want)
		}
	}
}

// And the negative half of the same claim: the three targets really are
// different, so a hardcoded path could not have served all three. Without
// this, the table above would pass on an implementation that returned a
// constant per file extension.
func TestTheThreeFormatsPutTheirMainPartInThreePlaces(t *testing.T) {
	seen := map[string]bool{}
	for _, path := range []string{docxPath, xlsxPath, pptxPath} {
		m, err := open(t, path).Main()
		if err != nil {
			t.Fatal(err)
		}
		seen[m] = true
	}
	if len(seen) != 3 {
		t.Errorf("the three formats name %d distinct main parts, want 3: %v", len(seen), seen)
	}
}

// ⛔ The measured inconsistency, pinned to the file that shows it. If a future
// fixture no longer has it, this fails and says the rule may be re-examined —
// rather than the rule quietly becoming folklore.
func TestARealProducerWroteARelationshipTypeInTheWrongCase(t *testing.T) {
	rels, err := open(t, xlsxPath).Rels("")
	if err != nil {
		t.Fatal(err)
	}
	var odd []string
	for _, r := range rels {
		if strings.Contains(r.Type, "/officedocument/") {
			odd = append(odd, r.Type)
		}
	}
	if len(odd) == 0 {
		t.Error("no relationship type in this .xlsx uses the lower-case " +
			"'officedocument' spelling any more; SameRelType's case-insensitive " +
			"comparison was argued from this file, so re-measure before relying on it")
	}
	// And the comparison that makes it harmless.
	if !SameRelType(
		"http://schemas.openxmlformats.org/officedocument/2006/relationships/x",
		"http://schemas.openxmlformats.org/officeDocument/2006/relationships/x") {
		t.Error("SameRelType is case-sensitive, so a real .xlsx would not be read")
	}
}

func TestContentTypesOverrideBeatsTheDefault(t *testing.T) {
	p := open(t, docxPath)
	// The main part has an Override naming it a WordprocessingML document.
	if ct := p.ContentType("word/document.xml"); !strings.Contains(ct, "wordprocessingml") {
		t.Errorf("word/document.xml is %q, want a wordprocessingml type", ct)
	}
	// ⛔ And a part with no Override falls back to the Default for its
	// extension. .rels is the one every package has.
	if ct := p.ContentType("_rels/.rels"); !strings.Contains(ct, "relationships") {
		t.Errorf("_rels/.rels is %q, want the relationships type from a Default", ct)
	}
	if ct := p.ContentType("no/such/part.wat"); ct != "" {
		t.Errorf("an unknown extension is %q, want empty", ct)
	}
}

// A relationship target is relative to the DIRECTORY of the part that declared
// it, which is the rule most easily got wrong: from word/document.xml,
// "styles.xml" is word/styles.xml and not styles.xml.
func TestATargetIsRelativeToItsOwnPart(t *testing.T) {
	p := open(t, docxPath)
	rels, err := p.Rels("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) == 0 {
		t.Fatal("word/document.xml declares no relationships, so this proves nothing")
	}
	for _, r := range rels {
		if r.External {
			continue
		}
		got, err := p.Resolve("word/document.xml", r)
		if err != nil {
			t.Errorf("%s: %v", r.ID, err)
			continue
		}
		if !p.Has(got) {
			t.Errorf("%s → %q, which is not a part of the package", r.Target, got)
		}
	}
}

func TestResolveTheThreeShapesOfTarget(t *testing.T) {
	p := open(t, docxPath)
	for _, c := range []struct{ part, target, want string }{
		{"word/document.xml", "styles.xml", "word/styles.xml"},
		{"word/document.xml", "../docProps/app.xml", "docProps/app.xml"},
		{"word/document.xml", "/docProps/core.xml", "docProps/core.xml"},
		{"", "word/document.xml", "word/document.xml"},
		// A Windows-written target. The separator in a part name is a slash.
		{"word/document.xml", `media\image1.png`, "word/media/image1.png"},
	} {
		got, err := p.Resolve(c.part, Rel{ID: "r", Target: c.target})
		if err != nil {
			t.Errorf("%q from %q: %v", c.target, c.part, err)
			continue
		}
		if got != c.want {
			t.Errorf("%q from %q = %q, want %q", c.target, c.part, got, c.want)
		}
	}
}

// ⛔ An external target is an ERROR, not a path. Cleaned into a part name,
// "http://example.com/x" becomes "http:/example.com/x" — a lookup that fails
// for a reason with nothing to do with what went wrong.
func TestAnExternalTargetIsRefusedRatherThanCleaned(t *testing.T) {
	p := open(t, docxPath)
	_, err := p.Resolve("word/document.xml", Rel{
		ID: "rId9", Target: "http://example.com/x", External: true,
	})
	if err == nil {
		t.Fatal("an external target was turned into a part name")
	}
	if !strings.Contains(err.Error(), "outside the package") {
		t.Errorf("the refusal does not say why: %q", err)
	}
}

// ⛔ A target that climbs out of the package is refused. This package writes
// no files, so there is no zip-slip to suffer here — but it hands part names
// to callers, and a caller that joined one onto a directory would.
func TestATargetThatClimbsOutIsRefused(t *testing.T) {
	p := open(t, docxPath)
	for _, target := range []string{"../../etc/passwd", "../../../x", "/../x"} {
		if got, err := p.Resolve("word/document.xml", Rel{ID: "r", Target: target}); err == nil {
			t.Errorf("%q resolved to %q", target, got)
		}
	}
}

func TestRelsOfAPartWithNoneIsNotAnError(t *testing.T) {
	rels, err := open(t, docxPath).Rels("word/styles.xml")
	if err != nil {
		t.Errorf("a part with no relationships part gave an error: %v", err)
	}
	if len(rels) != 0 {
		t.Errorf("got %d relationships, want none", len(rels))
	}
}

func TestRelOfTypeFindsTheMainPartAndMissesWhatIsNotThere(t *testing.T) {
	p := open(t, pptxPath)
	r, ok, err := p.RelOfType("", RelOfficeDocument)
	if err != nil || !ok {
		t.Fatalf("ok %v err %v", ok, err)
	}
	if r.Target != "ppt/presentation.xml" {
		t.Errorf("target %q", r.Target)
	}
	if _, ok, err := p.RelOfType("", "http://example.com/nope"); err != nil || ok {
		t.Errorf("a type that is not there: ok %v err %v", ok, err)
	}
}

func TestPartsAndHas(t *testing.T) {
	p := open(t, docxPath)
	names := p.Parts()
	if len(names) < 5 {
		t.Fatalf("only %d parts", len(names))
	}
	for _, n := range names {
		if !p.Has(n) {
			t.Errorf("Parts() lists %q and Has says no", n)
		}
	}
	if p.Has("no/such/part.xml") {
		t.Error("Has says yes to a part that is not there")
	}
	if p.Has("../escaped") {
		t.Error("Has says yes to a name that is not a part name")
	}
	// The case-insensitive half: OPC compares part names without regard to
	// case while a ZIP does not.
	if !p.Has("WORD/DOCUMENT.XML") {
		t.Error("a part cannot be found by a differently-cased name")
	}
	if _, err := p.Part("WORD/DOCUMENT.XML"); err != nil {
		t.Errorf("reading by a differently-cased name: %v", err)
	}
}

func TestPartRefusals(t *testing.T) {
	p := open(t, docxPath)
	if _, err := p.Part("../escaped"); err == nil {
		t.Error("a name that climbs out was read")
	}
	if _, err := p.Part("no/such/part.xml"); err == nil {
		t.Error("a part that is not there was read")
	}
}

func TestOpenRefusesWhatIsNotAZip(t *testing.T) {
	if _, err := Open([]byte("PK but not really"), Limits{}); err == nil {
		t.Error("rubbish was opened")
	}
}

// ⛔ A package that names no main part says so, rather than returning a path
// that happens to be empty.
func TestAPackageWithNoMainPartSaysSo(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("_rels/.rels")
	w.Write([]byte(`<?xml version="1.0"?><Relationships/>`))
	zw.Close()
	p, err := Open(buf.Bytes(), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Main()
	if err == nil {
		t.Fatal("a package with no officeDocument relationship named a main part")
	}
	if !strings.Contains(err.Error(), "officeDocument") {
		t.Errorf("the refusal does not say what is missing: %q", err)
	}
}
