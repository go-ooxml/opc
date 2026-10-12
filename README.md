# opc

**The Open Packaging Conventions**: the ZIP, the content types and the
relationships that `.docx`, `.xlsx`, `.pptx` and `.xps` are all built on. Pure
Go, CGO-free, including `GOOS=js`. No dependencies outside the standard
library.

```go
p, err := opc.Open(bytes, opc.Limits{})
main, err := p.Main()              // "word/document.xml", "xl/workbook.xml", …
doc, err := p.Part(main)
rels, err := p.Rels(main)          // what that part points at
target, err := p.Resolve(main, rels[0])
```

and the other direction:

```go
b, err := opc.Build([]opc.NewPart{
    {Name: "word/document.xml", ContentType: wordMain, Data: doc},
    {Name: "word/styles.xml", Data: styles},      // .xml already has a Default
}, map[string][]opc.Rel{
    "":                  {{ID: "rId1", Type: opc.RelOfficeDocument, Target: "word/document.xml"}},
    "word/document.xml": {{ID: "rId1", Type: relStyles, Target: "styles.xml"}},
})
```

It is the layer **below** a format reader or writer. What it knows is how to
find a part and what a part says it is; what the parts *mean* is
WordprocessingML's business, or SpreadsheetML's, or PresentationML's.

## Building one

`Build` writes the content types part and every relationships part itself,
because where those go is a **rule and not a choice** — `_rels/.rels` beside
the package, `word/_rels/document.xml.rels` beside the part. It refuses the two
names it writes, since a second copy would contradict the first.

It also refuses four packages that would open and then fail to say anything, in
whatever program the file was written for rather than here:

| | |
|---|---|
| a part name with `..` in it | which escapes the package |
| the same part twice | where the second would be unreachable |
| a relationship from a part that is not there | a dangling edge nothing can resolve |
| no package relationships at all | a package nothing points into has no main part, and that is where every reader starts |

⛔ **The same package twice is the same bytes.** A map walk is random, and a
writer whose output changes between runs cannot be diffed, cached or signed.
The test builds eight relationship sources and compares five times: with three
sources a map walk comes out in the right order one time in six, and the test
reported a determinism that was not there.

⛔ **Two things are asserted by their spelling** rather than by being read
back, which is the opposite of how everything else here is checked: an
`Override`'s `PartName` starts with a slash, and the `.rels` Default is
present. Our own reader accepts a package without either, so reading it back
proves nothing — and the programs that refuse such a package are the ones
nothing here can run. The control is a `.docx` Word itself produced, which
carries both.

## ⛔ The control for the writer is the reader

Every other test builds a package and then opens it with `Open`. A writer
checked against a string of expected XML is checked against what its author
believed; the thing that has to be true is that it can be **read**.

## ⛔ Nothing here is found by its path

The main part of a package is reached through a **relationship**, never by
guessing a filename. Measured on three files LibreOffice wrote in the same
minute, the one relationship type that names it points at three different
places:

| | the `officeDocument` relationship targets |
| --- | --- |
| `.docx` | `word/document.xml` |
| `.xlsx` | `xl/workbook.xml` |
| `.pptx` | `ppt/presentation.xml` |

A reader that hardcoded `word/document.xml` would work on most Word documents
and on nothing else — and would still fail on the Word documents whose
producer chose another name, which the standard allows.

## ⛔ A relationship type is compared without regard to case

Not caution: a measurement. In the `.xlsx` of those three, the core-properties
relationship type came out as

```
http://schemas.openxmlformats.org/officedocument/2006/relationships/...
```

with **`officedocument` in lower case**, while the `.docx` and the `.pptx` from
the same program, the same version and the same minute wrote `officeDocument`.
A producer is inconsistent about the case of its own URIs, so a reader that
compares them exactly works on two files out of three. `SameRelType` is the
comparison, and `TestARealProducerWroteARelationshipTypeInTheWrongCase` fails
if a future fixture no longer shows it — so the rule cannot quietly become
folklore.

## What a target is relative to

A relationship's target is relative to the **directory of the part that
declared it**. From `word/document.xml`:

| target | resolves to |
| --- | --- |
| `styles.xml` | `word/styles.xml` |
| `../docProps/app.xml` | `docProps/app.xml` |
| `/docProps/core.xml` | `docProps/core.xml` |
| `media\image1.png` | `word/media/image1.png` |

⛔ An **external** target is an error rather than a path.
`http://example.com/x` cleaned into a part name becomes `http:/example.com/x`,
which is a lookup that fails for a reason with nothing to do with what went
wrong.

⛔ A target that **climbs out** of the package is refused rather than cleaned
into something harmless. This package writes no files, so there is no zip-slip
to suffer *here* — but it hands part names to its callers, and a caller that
joined one onto a directory would.

## What a package may ask for

An OPC container is a ZIP, and a ZIP says how big it will be once opened. What
it says is a number in a **file**: it costs the author of that file nothing to
claim four gigabytes, and a reader that believes it allocates them.

| | default | why |
| --- | --- | --- |
| one part, opened | 64 MiB | refused from its **declared** size, before a byte is inflated — then the read is bounded again, in case the declaration was a lie the other way |
| every part together | 384 MiB | one part under the ceiling says nothing about a thousand of them |
| the file itself | 256 MiB | |
| parts | 8 192 | it decides an allocation made *before anything is read* |
| relationships per part | 4 096 | the same, from a count the file chose |

The bomb in the tests is **measured, not asserted**: 8 638 bytes on disk open
to 8 MiB, a ratio of 971:1, and the test fails if the fixture stops being a
bomb — so a change that made it smaller would be visible rather than making the
test merely look stricter.

## What is deliberately *not* fatal

| | |
| --- | --- |
| no `[Content_Types].xml` | the package is malformed and every real reader opens it anyway: the parts are all still there. Refusing would turn "I cannot tell you what this part is" into "I cannot read this file" |
| a directory entry, or an odd name | not a part; skipped. Refusing a whole file because of one would make a reader that fails where every other reader opens — while still never handing the name to a caller |
| a part with no relationships | most parts of most packages have none |

## Checks

100 % statement coverage with **zero** uncovered blocks, `go vet` and `gofmt`
clean, race-clean, and it builds for ten targets including `js/wasm`,
`linux/loong64` and `linux/s390x`.

⛔ The coverage gate reads the **profile**, not the total line. `go tool cover
-func` prints one decimal, so a single uncovered statement out of two thousand
rounds to `100.0%` and a gate reading that total passes. This one fails on any
block whose execution count is zero, and the gate itself was tested in both
directions before being trusted.

## Licence

BSD-3-Clause.
