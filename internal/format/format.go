// Package format turns the files of a source's snapshot into definitions:
// objects of the object model as one source describes them
// (docs/adr/0009-source-kinds-and-format-adapters.md). Each format, such as
// devicetype-library's, is an adapter in a package of its own below this
// one. This package holds what formats share: definitions, the problems a
// file can have, a YAML reader that reads files as NetBox's own import reads
// them, within limits, and JSON Schema validation with remote references
// refused (docs/adr/0024-read-source-files-as-pyyaml-does.md).
//
// Everything a source holds is untrusted input (boundary B1 in
// docs/architecture.md). Problems quote what they found cut short, but can
// still hold any characters a file holds: escape them before showing them.
package format

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/source"
)

// Format reads one layout of files, such as devicetype-library's.
type Format interface {
	// Name is the format's name in a source's configuration, such as
	// devicetype-library.
	Name() string
	// Needs lists patterns of files the format needs whatever a source's
	// include patterns choose, such as devicetype-library's JSON schemas. A
	// command adds them to a source's include patterns, when it has any.
	Needs() []string
	// Reads lists patterns of the files the format reads, so that a command
	// can narrow a snapshot to them (source.Spec.Only).
	Reads() []string
	// Read reads a snapshot's files into definitions. A file that fails to
	// read is a problem in the result, and the rest are still read. Read
	// returns an error only when it can't read the snapshot at all, such as
	// when the snapshot lacks a file the format needs, or ctx ends.
	Read(ctx context.Context, snap *source.Snapshot) (*Result, error)
}

// Result is what a format read from a snapshot.
type Result struct {
	// Definitions are the objects the snapshot defines, in an order that
	// puts each after those it depends on: for devicetype-library,
	// manufacturers by name, then device types by path.
	Definitions []Definition
	// Problems are what is wrong with the files, sorted by path and
	// position, with problems that concern no one file, such as two derived
	// manufacturers that would get the same slug, first.
	Problems []Problem
	// Files is how many files the format took as definitions, counting
	// those, or directories of them, that the source layer left out; and
	// Invalid how many of them it skipped with an error.
	Files, Invalid int
}

// Definition is one object as one source defines it, such as the device
// type in device-types/Dell/PowerEdge-R670.yaml. A source can define the
// same object as another, and NetBox may hold it already.
type Definition struct {
	// Object is the object's type and the fields the definition sets,
	// normalized as the type's spec says. A field it doesn't set, it doesn't
	// manage (docs/adr/0006-manage-only-linked-objects.md).
	Object model.Object
	// Identity holds the values of the spec's identity fields, which tell
	// the object from others of its type: a device type's manufacturer and
	// model.
	Identity map[string]model.Value
	// Path is the file that defines the object, or "" for an object the
	// format derives rather than reads, such as a manufacturer that
	// devicetype-library only names.
	Path string
	// DerivedFrom lists, sorted, the files a derived object comes from: for
	// a manufacturer, every valid file that names it. The object goes away
	// with the last of them.
	DerivedFrom []string
	// Derived lists the fields the format derived rather than read, such as
	// a manufacturer's slug. They are used only when TrueUp creates the
	// object, and aren't managed on an object NetBox has already
	// (docs/adr/0009-source-kinds-and-format-adapters.md).
	Derived []string
	// Metadata holds what the file says that isn't a NetBox field, such as
	// devicetype-library's front_image. It is kept, and never sent to
	// NetBox.
	Metadata map[string]model.Value
	// Unmapped counts, by key, the lists of children the file holds that
	// TrueUp doesn't read as objects yet, such as a device type's 48
	// interfaces: the specs of component templates come in roadmap steps 11
	// and 12.
	Unmapped map[string]int
}

// Severity says what a problem means for its file.
type Severity string

// Severities. Their values are part of the CLI's JSON output.
const (
	// Error means the file was skipped: nothing in it is used.
	Error Severity = "error"
	// Warning means the file was read, but something in it, or in what the
	// format derived from it, needs a look.
	Warning Severity = "warning"
)

// Problem is something wrong with a file, or with what a format derived
// from files.
type Problem struct {
	Severity Severity
	// Path is the file, or "" for a problem that concerns no one file, such
	// as two derived manufacturers that would get the same slug.
	Path string
	// Line and Column locate the problem in the file, from 1, or are 0 when
	// it concerns the whole file.
	Line, Column int
	// Field says where in the file's structure the problem is, such as
	// interfaces[3].type, or is "" for the whole file.
	Field string
	// Reason says what is wrong. It quotes values cut short, but can hold
	// any characters a file holds: escape it before showing it.
	Reason string
}

// String returns the problem as path:line:column: field: reason, leaving
// out the parts it doesn't have.
func (p Problem) String() string {
	var b strings.Builder
	if p.Path != "" {
		b.WriteString(p.Path)
		if p.Line > 0 {
			fmt.Fprintf(&b, ":%d", p.Line)
			if p.Column > 0 {
				fmt.Fprintf(&b, ":%d", p.Column)
			}
		}
		b.WriteString(": ")
	}
	if p.Field != "" {
		b.WriteString(p.Field)
		b.WriteString(": ")
	}
	b.WriteString(p.Reason)
	return b.String()
}

// Quote quotes a value from a file for a problem's reason: cut to 40
// bytes, and with anything that isn't printable escaped.
func Quote(s string) string {
	const limit = 40
	if len(s) <= limit {
		return strconv.Quote(s)
	}
	return strconv.Quote(cutBytes(s, limit)) + "…"
}

// cutText cuts text, such as another library's error message, to at most
// limit bytes and an ellipsis.
func cutText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return strings.ToValidUTF8(cutBytes(s, limit), "") + "…"
}

// cutBytes returns s cut to at most limit bytes, without splitting a
// character.
func cutBytes(s string, limit int) string {
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit]
}
