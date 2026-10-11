// Copyright (c) the go-ooxml authors.
// SPDX-License-Identifier: BSD-3-Clause

package opc

import "fmt"

// Limits bound what a package may ask for before anything is allocated.
//
// An OPC container is a ZIP, and a ZIP says how big it will be once opened.
// ⛔ What it says is a number in a FILE: it costs the author of that file
// nothing to claim four gigabytes, and a reader that believes it allocates
// them. Every ceiling here is checked against the DECLARED size first, so a
// refusal happens before a byte is inflated, and then the read itself is
// bounded again in case the declaration was a lie in the other direction.
//
// The zero value asks for the defaults, which are generous for real documents
// and small for hostile ones. They are the same ceilings go-odf/odf arrived at
// after measuring what a bomb costs: a 67 206-byte spreadsheet whose
// content.xml inflates to 64 MiB parsed without complaint and held 222.9 MiB —
// a thousandfold on disk, and better than three times the inflated size in the
// heap, because the bytes are read, decoded as UTF-8, then kept as runs.
type Limits struct {
	// MaxPartBytes is how large one part may be, opened. Zero means 64 MiB.
	MaxPartBytes int64
	// MaxPackageBytes is how large the file itself may be. Zero means 256 MiB.
	MaxPackageBytes int64
	// MaxUncompressedBytes is how large every part may come to TOGETHER. Zero
	// means 384 MiB.
	//
	// ⛔ One part under the ceiling says nothing about a thousand of them,
	// which is the shape of bomb a per-part limit alone invites: a package of
	// eight thousand parts each a byte under the limit.
	MaxUncompressedBytes int64
	// MaxParts is how many entries the ZIP may hold. Zero means 8192.
	//
	// It is checked before the parts are indexed, because that index is an
	// allocation made from a count the file chose.
	MaxParts int
	// MaxRels is how many relationships one part may declare. Zero means 4096.
	MaxRels int
}

// The defaults. They are var rather than const so that a test can lower them:
// a test that had to build a 64 MiB part to see a refusal would be a test
// nobody runs.
var (
	defaultMaxPartBytes         int64 = 64 << 20
	defaultMaxPackageBytes      int64 = 256 << 20
	defaultMaxUncompressedBytes int64 = 384 << 20
	defaultMaxParts                   = 8192
	defaultMaxRels                    = 4096
)

func (l Limits) orDefaults() Limits {
	if l.MaxPartBytes <= 0 {
		l.MaxPartBytes = defaultMaxPartBytes
	}
	if l.MaxPackageBytes <= 0 {
		l.MaxPackageBytes = defaultMaxPackageBytes
	}
	if l.MaxUncompressedBytes <= 0 {
		l.MaxUncompressedBytes = defaultMaxUncompressedBytes
	}
	if l.MaxParts <= 0 {
		l.MaxParts = defaultMaxParts
	}
	if l.MaxRels <= 0 {
		l.MaxRels = defaultMaxRels
	}
	return l
}

// errTooLarge is returned when a package asks for more than the ceilings
// allow. It names what wanted it, how much, and how much was allowed, because
// the whole value of a refusal is that the person reading it can tell a
// hostile file from a legitimately enormous one.
type errTooLarge struct {
	what    string
	want    int64
	limit   int64
	entries bool // want and limit are counts, not bytes
}

func (e errTooLarge) Error() string {
	if e.entries {
		return fmt.Sprintf("opc: %s holds %d entries, past the %d allowed", e.what, e.want, e.limit)
	}
	return fmt.Sprintf("opc: %s is %d bytes, past the %d allowed", e.what, e.want, e.limit)
}

// TooLarge says whether an error is a ceiling refusing a file, so that a
// caller can tell "this file is hostile or enormous" from "this file is
// broken" without reading the message.
func TooLarge(err error) bool {
	_, ok := err.(errTooLarge)
	return ok
}
