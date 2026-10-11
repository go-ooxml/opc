// Copyright (c) the go-ooxml authors.
// SPDX-License-Identifier: BSD-3-Clause

package opc

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

// zipOf builds a package from a map of part name to contents.
func zipOf(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range parts {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// ⛔ MEASURED, and the measurement is the point of the ceilings. Deflate likes
// repetition: a part of one repeated byte compresses to almost nothing, so the
// ratio a hostile file can reach is bounded only by the ceiling. This asserts
// the ratio is real on the fixture, so that a change which made the bomb
// smaller would be visible rather than making the test look stricter.
func TestABombIsRefusedFromItsDeclaredSizeBeforeAnythingIsInflated(t *testing.T) {
	const inflated = 8 << 20 // 8 MiB of one byte
	pkg := zipOf(t, map[string]string{
		"[Content_Types].xml": `<?xml version="1.0"?><Types/>`,
		"word/document.xml":   strings.Repeat("A", inflated),
	})
	ratio := float64(inflated) / float64(len(pkg))
	if ratio < 100 {
		t.Fatalf("the fixture only compresses %.0f:1, so it does not test a bomb", ratio)
	}
	t.Logf("%d bytes on disk, %d opened: %.0f:1", len(pkg), inflated, ratio)

	// The part's DECLARED size is what refuses it, and the ceiling is set
	// below the part so the refusal cannot come from anywhere else.
	p, err := Open(pkg, Limits{MaxPartBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Part("word/document.xml")
	if err == nil {
		t.Fatal("an 8 MiB part was read under a 1 MiB ceiling")
	}
	if !TooLarge(err) {
		t.Errorf("the refusal is not a ceiling: %v", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "word/document.xml") {
		t.Errorf("the refusal does not name the part: %q", msg)
	}
}

// ⛔ One part under the ceiling says nothing about a thousand of them. This is
// the shape of bomb a per-part limit alone invites.
func TestManyPartsEachUnderTheCeilingAreRefusedTogether(t *testing.T) {
	parts := map[string]string{}
	for i := range 40 {
		parts["word/p"+string(rune('a'+i%26))+string(rune('a'+i/26))+".xml"] = strings.Repeat("B", 1<<16)
	}
	pkg := zipOf(t, parts)
	// Every part is 64 KiB and the per-part ceiling is 128 KiB, so nothing is
	// refused for its own size; the total is what refuses.
	_, err := Open(pkg, Limits{MaxPartBytes: 128 << 10, MaxUncompressedBytes: 1 << 20})
	if err == nil {
		t.Fatal("40 parts of 64 KiB opened under a 1 MiB total ceiling")
	}
	if !TooLarge(err) {
		t.Errorf("the refusal is not a ceiling: %v", err)
	}
	// And the control: the same package opens when the total allows it, so the
	// refusal above is the total and not something else about the fixture.
	if _, err := Open(pkg, Limits{MaxPartBytes: 128 << 10, MaxUncompressedBytes: 8 << 20}); err != nil {
		t.Errorf("the same package under a larger total ceiling: %v", err)
	}
}

func TestTooManyPartsIsRefusedBeforeTheyAreIndexed(t *testing.T) {
	parts := map[string]string{}
	for i := range 50 {
		parts["x/"+strings.Repeat("a", i+1)+".xml"] = "."
	}
	_, err := Open(zipOf(t, parts), Limits{MaxParts: 10})
	if err == nil {
		t.Fatal("50 parts opened under a ceiling of 10")
	}
	if !TooLarge(err) {
		t.Errorf("the refusal is not a ceiling: %v", err)
	}
}

func TestThePackageItselfIsBounded(t *testing.T) {
	pkg := zipOf(t, map[string]string{"a.xml": strings.Repeat("c", 1<<16)})
	_, err := Open(pkg, Limits{MaxPackageBytes: 16})
	if err == nil {
		t.Fatal("a package larger than the ceiling was opened")
	}
	if !TooLarge(err) {
		t.Errorf("the refusal is not a ceiling: %v", err)
	}
}

// A relationships part is read like any other, so it is bounded like any
// other — but the COUNT of relationships is its own allocation, from a number
// the file chose.
func TestTooManyRelationshipsIsRefused(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0"?><Relationships>`)
	for i := range 200 {
		sb.WriteString(`<Relationship Id="r` + string(rune('a'+i%26)) +
			`" Type="t" Target="x.xml"/>`)
	}
	sb.WriteString(`</Relationships>`)
	pkg := zipOf(t, map[string]string{"_rels/.rels": sb.String()})
	p, err := Open(pkg, Limits{MaxRels: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Rels(""); err == nil {
		t.Fatal("200 relationships were read under a ceiling of 10")
	} else if !TooLarge(err) {
		t.Errorf("the refusal is not a ceiling: %v", err)
	}
}

// ⛔ A ZIP entry that climbs out of the package is SKIPPED, not fatal. A
// package can carry a directory entry or an odd name, and refusing the whole
// file because of one would make a reader that fails where every other reader
// opens — while still never handing the name to a caller.
func TestOddEntryNamesAreSkippedAndNotFatal(t *testing.T) {
	pkg := zipOf(t, map[string]string{
		"../escaped.xml":    "no",
		"/absolute.xml":     "no",
		"word/document.xml": "yes",
	})
	p, err := Open(pkg, Limits{})
	if err != nil {
		t.Fatalf("a package with odd entry names could not be opened: %v", err)
	}
	for _, n := range p.Parts() {
		if strings.Contains(n, "..") {
			t.Errorf("Parts() lists %q", n)
		}
	}
	if !p.Has("word/document.xml") {
		t.Error("the real part was lost with the odd ones")
	}
	// "/absolute.xml" is a legitimate part name once its slash is dropped,
	// which is what OPC says a part name looks like in the first place.
	if !p.Has("absolute.xml") {
		t.Error("an entry named with a leading slash was dropped entirely")
	}
}

// Malformed XML in the two parts this package parses itself must be an error
// and not a silent empty answer.
func TestMalformedXmlIsAnError(t *testing.T) {
	bad := `<?xml version="1.0"?><Relationships><Relationship`
	p, err := Open(zipOf(t, map[string]string{"_rels/.rels": bad}), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Rels(""); err == nil {
		t.Error("a truncated relationships part was read without an error")
	}
	if _, err := Open(zipOf(t, map[string]string{
		"[Content_Types].xml": `<?xml version="1.0"?><Types><Default`,
	}), Limits{}); err == nil {
		t.Error("a truncated [Content_Types].xml was read without an error")
	}
}

// ⛔ A package with no [Content_Types].xml is malformed, and every real reader
// opens it anyway: the parts are all still there. Refusing would turn "I
// cannot tell you what this part is" into "I cannot read this file".
func TestAPackageWithNoContentTypesStillOpens(t *testing.T) {
	p, err := Open(zipOf(t, map[string]string{"word/document.xml": "x"}), Limits{})
	if err != nil {
		t.Fatalf("a package with no [Content_Types].xml was refused: %v", err)
	}
	if !p.Has("word/document.xml") {
		t.Error("its parts are not readable")
	}
	if ct := p.ContentType("word/document.xml"); ct != "" {
		t.Errorf("it claims a content type %q", ct)
	}
}

func TestResolveRefusesAnEmptyTarget(t *testing.T) {
	p := open(t, docxPath)
	if _, err := p.Resolve("word/document.xml", Rel{ID: "rId1"}); err == nil {
		t.Error("a relationship with no target resolved")
	}
}

func TestTooLargeSaysNoToOtherErrors(t *testing.T) {
	if TooLarge(nil) {
		t.Error("TooLarge(nil)")
	}
	_, err := Open([]byte("not a zip"), Limits{})
	if TooLarge(err) {
		t.Errorf("TooLarge says yes to %v", err)
	}
}

// The ceiling messages, which TooLarge's callers do not read but a person
// does. ⛔ Asserted because the entries-counting branch of the message was
// formatted by no test at all: every ceiling test asked TooLarge and none read
// the sentence.
func TestACeilingSaysWhatItCounted(t *testing.T) {
	parts := map[string]string{}
	for i := range 20 {
		parts["x/"+strings.Repeat("a", i+1)+".xml"] = "."
	}
	_, err := Open(zipOf(t, parts), Limits{MaxParts: 3})
	if err == nil {
		t.Fatal("not refused")
	}
	if msg := err.Error(); !strings.Contains(msg, "entries") || !strings.Contains(msg, "past the 3") {
		t.Errorf("a count was reported as bytes: %q", msg)
	}
	pkg := zipOf(t, map[string]string{"a.xml": strings.Repeat("c", 1<<16)})
	if _, err := Open(pkg, Limits{MaxPackageBytes: 16}); err == nil {
		t.Fatal("not refused")
	} else if msg := err.Error(); !strings.Contains(msg, "bytes") || strings.Contains(msg, "entries") {
		t.Errorf("bytes were reported as a count: %q", msg)
	}
}

// A ZIP may carry a directory entry. It is not a part, and it must not reach
// Parts() as one.
func TestADirectoryEntryIsNotAPart(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if _, err := zw.Create("word/"); err != nil { // a directory entry
		t.Fatal(err)
	}
	w, _ := zw.Create("word/document.xml")
	w.Write([]byte("x"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	p, err := Open(buf.Bytes(), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range p.Parts() {
		if strings.HasSuffix(n, "/") || n == "word" {
			t.Errorf("Parts() lists the directory entry as %q", n)
		}
	}
	if got := len(p.Parts()); got != 1 {
		t.Errorf("%d parts, want 1: %v", got, p.Parts())
	}
}

// ⛔ A part whose compression this build cannot undo is an error from the
// read, not a panic and not empty bytes. A ZIP may name any method number it
// likes; 99 is the one AES-encrypted ZIPs use, and archive/zip has no decoder
// for it.
func TestAPartInAnUnknownCompressionIsAnError(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateRaw(&zip.FileHeader{
		Name: "word/document.xml", Method: 99,
		CompressedSize64: 4, UncompressedSize64: 4, CRC32: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("junk"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	p, err := Open(buf.Bytes(), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Part("word/document.xml"); err == nil {
		t.Error("a part in an unknown compression method was read")
	}
}

func TestContentTypeOfSomethingThatIsNotAPartName(t *testing.T) {
	if got := open(t, docxPath).ContentType("../escaped"); got != "" {
		t.Errorf("ContentType of a name that climbs out = %q", got)
	}
}

// The three places a relationships part is read, each of which can fail for
// the same reason — the part is past the ceiling — and each of which has its
// own error return.
func TestARelationshipsPartPastTheCeilingIsReportedEverywhere(t *testing.T) {
	b := read(t, docxPath)
	tiny := Limits{MaxPartBytes: 8}
	p, err := Open(b, tiny)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Rels(""); err == nil {
		t.Error("Rels read a part past the ceiling")
	}
	if _, _, err := p.RelOfType("", RelOfficeDocument); err == nil {
		t.Error("RelOfType read a part past the ceiling")
	}
	if _, err := p.Main(); err == nil {
		t.Error("Main read a part past the ceiling")
	}
}
