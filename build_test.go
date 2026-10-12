package opc

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

const wordMain = "application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"

// ⛔ THE CONTROL FOR A WRITER IS THE READER. Everything below builds a package
// and then opens it with [Open]: a writer checked against a string of expected
// XML is checked against what its author believed, and the thing that has to
// be true is that it can be read.
func TestWhatIsBuiltCanBeOpened(t *testing.T) {
	b, err := Build([]NewPart{
		{Name: "word/document.xml", ContentType: wordMain, Data: []byte("<w:document/>")},
		{Name: "word/styles.xml", Data: []byte("<w:styles/>")},
	}, map[string][]Rel{
		"": {{ID: "rId1", Type: RelOfficeDocument, Target: "word/document.xml"}},
		"word/document.xml": {
			{ID: "rId1", Type: "http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles",
				Target: "styles.xml"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Open(b, Limits{})
	if err != nil {
		t.Fatalf("what was built could not be opened: %v", err)
	}
	// The main part is found the way every reader finds it.
	main, err := p.Main()
	if err != nil {
		t.Fatalf("no main part: %v", err)
	}
	if main != "word/document.xml" {
		t.Errorf("the main part is %q", main)
	}
	if got := p.ContentType(main); got != wordMain {
		t.Errorf("the main part is a %q", got)
	}
	data, err := p.Part(main)
	if err != nil || string(data) != "<w:document/>" {
		t.Errorf("the main part reads %q, %v", data, err)
	}
	// And a relationship FROM a part resolves against that part's directory,
	// which is the rule a sibling target depends on.
	rel, ok, err := p.RelOfType(main,
		"http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles")
	if err != nil || !ok {
		t.Fatalf("no styles relationship: %v, %v", ok, err)
	}
	target, err := p.Resolve(main, rel)
	if err != nil || target != "word/styles.xml" {
		t.Errorf("styles.xml resolved to %q, %v", target, err)
	}
	// A part with no content type of its own is XML by the Default.
	if got := p.ContentType("word/styles.xml"); got != "application/xml" {
		t.Errorf("styles.xml is a %q", got)
	}
}

// A leading slash is how the content types part spells a part name and the
// ZIP does not. A caller should not have to remember which this is.
func TestAPartNameWithALeadingSlash(t *testing.T) {
	b, err := Build([]NewPart{{Name: "/word/document.xml", ContentType: wordMain, Data: []byte("<x/>")}},
		map[string][]Rel{"": {{ID: "rId1", Type: RelOfficeDocument, Target: "/word/document.xml"}}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Open(b, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Has("word/document.xml") {
		t.Errorf("the parts are %v", p.Parts())
	}
}

// ⛔ The same package twice has to be the same BYTES. A map walk is random,
// and a writer whose output changes between runs cannot be diffed, cached or
// signed.
// ⛔ EIGHT relationship sources, and compared five times. Three sources come
// out of a map walk in the right order one time in six, and this test runs
// once — so with three it reported determinism that was not there. Eight
// orderings agree by chance once in forty thousand.
func TestBuildingTheSamePackageTwiceGivesTheSameBytes(t *testing.T) {
	parts := []NewPart{
		{Name: "word/document.xml", ContentType: wordMain, Data: []byte("<x/>")},
		{Name: "media/a.png", ContentType: "image/png", Data: []byte("\x89PNG")},
		{Name: "media/b.jpeg", ContentType: "image/jpeg", Data: []byte("\xff\xd8")},
	}
	rels := map[string][]Rel{
		"": {{ID: "rId1", Type: RelOfficeDocument, Target: "word/document.xml"}},
	}
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		name := "word/" + n + ".xml"
		parts = append(parts, NewPart{Name: name, Data: []byte("<" + n + "/>")})
		rels[name] = []Rel{{ID: "rId1", Type: "t", Target: "../media/a.png"}}
	}
	build := func() []byte {
		b, err := Build(parts, rels)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	first := build()
	for i := 0; i < 5; i++ {
		if !bytes.Equal(first, build()) {
			t.Fatalf("build %d differs from the first", i+2)
		}
	}
}

// ⛔ An Override's PartName is an ABSOLUTE part name — it starts with a slash.
// This is the one place the spelling has to be asserted rather than the
// behaviour, because our own reader accepts it either way and the programs
// that do not are the ones nothing here can run. The control is a .docx Word
// itself produced, whose content types part reads
// `<Override ContentType="…" PartName="/word/document.xml"/>`.
func TestAnOverridePartNameIsAbsolute(t *testing.T) {
	b, err := Build([]NewPart{{Name: "word/document.xml", ContentType: wordMain, Data: []byte("<x/>")}},
		map[string][]Rel{"": {{ID: "rId1", Type: RelOfficeDocument, Target: "word/document.xml"}}})
	if err != nil {
		t.Fatal(err)
	}
	types := partOf(t, b, "[Content_Types].xml")
	if !strings.Contains(types, `PartName="/word/document.xml"`) {
		t.Errorf("the Override is not an absolute part name:\n%s", types)
	}
}

// An extension that is not XML gets a Default, written once however many
// parts share it, and a part whose type the Default already covers gets no
// Override.
func TestAnExtensionThatIsNotXMLGetsADefault(t *testing.T) {
	b, err := Build([]NewPart{
		{Name: "word/document.xml", ContentType: wordMain, Data: []byte("<x/>")},
		{Name: "media/a.png", ContentType: "image/png", Data: []byte("\x89PNG")},
		{Name: "media/b.png", ContentType: "image/png", Data: []byte("\x89PNG")},
	}, map[string][]Rel{"": {{ID: "rId1", Type: RelOfficeDocument, Target: "word/document.xml"}}})
	if err != nil {
		t.Fatal(err)
	}
	types := partOf(t, b, "[Content_Types].xml")
	if n := strings.Count(types, `Extension="png"`); n != 1 {
		t.Errorf("%d png Defaults, want 1:\n%s", n, types)
	}
	if strings.Contains(types, `PartName="/media/a.png"`) {
		t.Errorf("a part the Default already covers got an Override:\n%s", types)
	}
	p, err := Open(b, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.ContentType("media/b.png"); got != "image/png" {
		t.Errorf("b.png is a %q", got)
	}
}

// Two parts with the same extension and DIFFERENT types: the first gets the
// Default and the second has to say so itself, or it reads back as the first.
func TestTwoTypesForOneExtension(t *testing.T) {
	b, err := Build([]NewPart{
		{Name: "word/document.xml", ContentType: wordMain, Data: []byte("<x/>")},
		{Name: "a.bin", ContentType: "application/octet-stream", Data: []byte("1")},
		{Name: "b.bin", ContentType: "application/vnd.special", Data: []byte("2")},
	}, map[string][]Rel{"": {{ID: "rId1", Type: RelOfficeDocument, Target: "word/document.xml"}}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Open(b, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.ContentType("b.bin"); got != "application/vnd.special" {
		t.Errorf("b.bin is a %q, want application/vnd.special", got)
	}
	if got := p.ContentType("a.bin"); got != "application/octet-stream" {
		t.Errorf("a.bin is a %q", got)
	}
}

// An external target is marked as one, or a reader goes looking for
// "https:/example.com/x" inside the package.
func TestAnExternalTarget(t *testing.T) {
	b, err := Build([]NewPart{{Name: "word/document.xml", ContentType: wordMain, Data: []byte("<x/>")}},
		map[string][]Rel{
			"": {{ID: "rId1", Type: RelOfficeDocument, Target: "word/document.xml"}},
			"word/document.xml": {{ID: "rId1", Type: "http://example.invalid/link",
				Target: "https://example.invalid/x", External: true}},
		})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Open(b, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	rels, err := p.Rels("word/document.xml")
	if err != nil || len(rels) != 1 {
		t.Fatalf("%d rels, %v", len(rels), err)
	}
	if !rels[0].External {
		t.Errorf("the target came back as a part name: %+v", rels[0])
	}
}

// Text that is XML goes through escaped, in every place this writes one.
func TestTextThatIsXML(t *testing.T) {
	b, err := Build([]NewPart{
		{Name: "word/document.xml", ContentType: `application/x-"quoted"&<>`, Data: []byte("<x/>")},
	}, map[string][]Rel{
		"": {{ID: `r"1`, Type: `t&t`, Target: `word/document.xml?a=1&b=<2>`}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// It reads back as what went in, which is the only test that matters:
	// asserting the escaping would assert the spelling instead.
	p, err := Open(b, Limits{})
	if err != nil {
		t.Fatalf("escaping broke the package: %v", err)
	}
	rels, err := p.Rels("")
	if err != nil || len(rels) != 1 {
		t.Fatalf("%d rels, %v", len(rels), err)
	}
	if rels[0].ID != `r"1` || rels[0].Type != `t&t` || rels[0].Target != `word/document.xml?a=1&b=<2>` {
		t.Errorf("the relationship came back as %+v", rels[0])
	}
	if got := p.ContentType("word/document.xml"); got != `application/x-"quoted"&<>` {
		t.Errorf("the content type came back as %q", got)
	}
}

// ⛔ Four packages it refuses to write, each of which opens and then fails to
// say anything — in whatever program the file was written for, not here.
func TestWhatItRefusesToBuild(t *testing.T) {
	ok := []NewPart{{Name: "word/document.xml", ContentType: wordMain, Data: []byte("<x/>")}}
	main := map[string][]Rel{"": {{ID: "rId1", Type: RelOfficeDocument, Target: "word/document.xml"}}}
	cases := []struct {
		name  string
		parts []NewPart
		rels  map[string][]Rel
		says  string
	}{
		{
			"a part name that escapes the package",
			[]NewPart{{Name: "../outside.xml", Data: []byte("x")}}, main,
			"is not a part name",
		},
		{
			"the same part twice",
			append(append([]NewPart{}, ok...), ok...), main,
			"twice",
		},
		{
			"no package relationships at all",
			ok, nil,
			"no main part",
		},
		{
			"relationships from a part that is not there",
			ok,
			map[string][]Rel{
				"":              {{ID: "rId1", Type: RelOfficeDocument, Target: "word/document.xml"}},
				"word/gone.xml": {{ID: "rId1", Type: "t", Target: "x.xml"}},
			},
			"not in the package",
		},
	}
	for _, c := range cases {
		_, err := Build(c.parts, c.rels)
		if err == nil {
			t.Errorf("%s: it was written anyway", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: it said %q, which does not mention %q", c.name, err, c.says)
		}
	}
}

// An empty relationship list for the package is the same as none: a reader
// still has nowhere to start.
func TestAnEmptyPackageRelationshipList(t *testing.T) {
	_, err := Build([]NewPart{{Name: "a.xml", Data: []byte("<x/>")}},
		map[string][]Rel{"": {}})
	if err == nil {
		t.Error("a package with an empty relationship list was written")
	}
}

// partOf reads one part back out of a built package.
func partOf(t *testing.T, b []byte, name string) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		var out bytes.Buffer
		if _, err := out.ReadFrom(rc); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	t.Fatalf("%s is not in the package", name)
	return ""
}

// ⛔ The content types part is FIRST in the archive. A reader that scans the
// directory does not care; one that streams has to know what a part is before
// it reaches it.
func TestTheContentTypesPartComesFirst(t *testing.T) {
	b, err := Build([]NewPart{{Name: "word/document.xml", ContentType: wordMain, Data: []byte("<x/>")}},
		map[string][]Rel{"": {{ID: "rId1", Type: RelOfficeDocument, Target: "word/document.xml"}}})
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if zr.File[0].Name != "[Content_Types].xml" {
		t.Errorf("the archive starts with %q", zr.File[0].Name)
	}
}

// ⛔ The two names this writer writes itself are refused, because a second
// copy would contradict the first — and archive/zip would be the one to say
// so, from inside a writer that has no way to explain it.
func TestTheNamesItWritesItselfAreRefused(t *testing.T) {
	main := map[string][]Rel{"": {{ID: "rId1", Type: RelOfficeDocument, Target: "a.xml"}}}
	for _, name := range []string{
		"[Content_Types].xml",
		"_rels/.rels",
		"word/_rels/document.xml.rels",
		"word/_rels/document.xml.RELS", // the suffix is not case sensitive
	} {
		_, err := Build([]NewPart{
			{Name: "a.xml", Data: []byte("<x/>")},
			{Name: name, Data: []byte("<y/>")},
		}, main)
		if err == nil {
			t.Errorf("%q was accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), "written for you") {
			t.Errorf("%q: it said %q", name, err)
		}
	}
}

// ⛔ Both Defaults every OPC package has. Our own reader does not need the
// .rels one — it reads a relationships part by name — so this asserts the
// spelling for the same reason the absolute PartName does: the programs that
// refuse a package without it are the ones nothing here can run. The control
// is again a .docx Word produced, which carries both.
func TestTheTwoDefaultsEveryPackageHas(t *testing.T) {
	b, err := Build([]NewPart{{Name: "word/document.xml", ContentType: wordMain, Data: []byte("<x/>")}},
		map[string][]Rel{"": {{ID: "rId1", Type: RelOfficeDocument, Target: "word/document.xml"}}})
	if err != nil {
		t.Fatal(err)
	}
	types := partOf(t, b, "[Content_Types].xml")
	for _, want := range []string{
		`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>`,
		`<Default Extension="xml" ContentType="application/xml"/>`,
	} {
		if !strings.Contains(types, want) {
			t.Errorf("%s\nis missing from\n%s", want, types)
		}
	}
}

// An extension differing only in case is the same extension: a package with
// A.PNG and b.png needs ONE Default, and two would be a package that
// contradicts itself about what .png is.
func TestAnExtensionDifferingOnlyInCase(t *testing.T) {
	b, err := Build([]NewPart{
		{Name: "word/document.xml", ContentType: wordMain, Data: []byte("<x/>")},
		{Name: "media/A.PNG", ContentType: "image/png", Data: []byte("\x89PNG")},
		{Name: "media/b.png", ContentType: "image/png", Data: []byte("\x89PNG")},
	}, map[string][]Rel{"": {{ID: "rId1", Type: RelOfficeDocument, Target: "word/document.xml"}}})
	if err != nil {
		t.Fatal(err)
	}
	types := partOf(t, b, "[Content_Types].xml")
	if n := strings.Count(types, `ContentType="image/png"`); n != 1 {
		t.Errorf("%d png declarations, want 1:\n%s", n, types)
	}
	if strings.Contains(types, `Extension="PNG"`) {
		t.Errorf("the Default keeps the case of the first part that used it:\n%s", types)
	}
}
