// Licensed to Julian Hyde under one or more contributor license
// agreements.  See the NOTICE file distributed with this work
// for additional information regarding copyright ownership.
// Julian Hyde licenses this file to you under the Apache
// License, Version 2.0 (the "License"); you may not use this
// file except in compliance with the License.  You may obtain a
// copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND,
// either express or implied.  See the License for the specific
// language governing permissions and limitations under the
// License.

package eval

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hydromatic/morel-go/internal/csv"
	"github.com/hydromatic/morel-go/internal/types"
)

// File is an entry in the file system that the "file" value
// browses: a directory, a data file that parses into a list of
// records, or an entry we have not looked at yet.
//
// A File's type is progressive. It starts as "{...}" — a record
// whose fields are not yet known — and widens as fields are
// discovered. Only DiscoverField advances a File's state;
// materializing a File's value (ReadRows) never does. That is what
// keeps the type stable: browsing "file.scott" tells us the names
// of scott's files without committing to the row type of every one
// of them, and printing a directory does not silently widen it.
//
// A file that cannot be read stays unexpanded, so its type stays
// "{...}" and a later attempt may succeed. No file-system error
// reaches the user; a missing directory reads as an empty one.
type File struct {
	// Path is where the file is on disk.
	Path string
	// BaseName is the file name with the Kind's suffix removed. It
	// is the label under which the file appears as a field of its
	// parent directory's record type.
	BaseName string
	// Kind is what the file's name (and whether it is a directory)
	// says it is.
	Kind FileKind

	mu sync.Mutex
	// expanded says whether expand has run. Until then children and
	// row are unset, and the file's type is the empty progressive
	// record.
	expanded bool
	// children are a directory's entries, in label order. Entries
	// whose kind is PlainKind are omitted: we could never do
	// anything with them, and they would only clutter the type.
	children []*File
	// row describes a data file's columns, in label order. It is
	// nil for a directory, and for a data file whose header is
	// empty.
	row []column
}

// FileKind is what kind of file an entry is, deduced from its name.
type FileKind int

// The kinds of file. PlainKind is a file whose suffix we do not
// recognize; it never expands, and never appears in a directory's
// type.
const (
	DirKind FileKind = iota
	PlainKind
	CSVKind
	CSVGzKind
)

// IsData says whether files of this kind parse into a list of
// records. Such a file's type is "_ list"; a directory's is a
// record.
func (k FileKind) IsData() bool {
	return k == CSVKind || k == CSVGzKind
}

// suffix is the file-name suffix that marks this kind, including
// the leading dot; a directory and a plain file have none.
func (k FileKind) suffix() string {
	// lint: sort until '^\t}' where '^\tcase '
	switch k {
	case CSVGzKind:
		return ".csv.gz"
	case CSVKind:
		return ".csv"
	case DirKind, PlainKind:
		return ""
	}
	return ""
}

// dataKinds are the kinds that a file name can declare, longest
// suffix first so that "x.csv.gz" is a CSVGzKind rather than a
// CSVKind.
var dataKinds = []FileKind{CSVGzKind, CSVKind}

// column is one column of a data file: the record label it
// supplies, the parser its cells go through, and the position of
// those cells in the file's lines. Columns are held in label
// order, so index is not usually the column's own position in the
// slice.
type column struct {
	label string
	parse columnParser
	index int
}

// columnParser converts a field of a data file to a value, as the
// column's declared type says.
//
// The header of a file declares each column as "name:type", where type
// is "bool", "int", "real" (or its synonym "double"), "decimal",
// "date" or "string", optionally followed by "option"; a missing or
// unknown type is "string". In an option column, an empty field or
// "NULL" is NONE; in any other column, it raises Fail when the file is
// read, because the column's type has no room for it, except that in
// a "string" column an empty field is the empty string. A field that
// is not a value of its column's type (a bool other than "true" or
// "false", a malformed number or date) raises Fail. A field may be
// quoted, whatever its type. These are morel-java's rules
// (hydromatic/morel#517).
type columnParser struct {
	base   baseType
	option bool
	// typeName is the type as the header declared it, without
	// "option", for messages.
	typeName string
}

// baseType is the type of a column's values, or of its options'.
type baseType int

const (
	stringBase baseType = iota
	boolBase
	dateBase
	decimalBase
	intBase
	realBase
)

// parserOf returns the parser for a type named in a header, e.g.
// the "int" of "deptno:int", or the "int option" of "mgr:int option".
func parserOf(name string) columnParser {
	words := strings.Fields(name)
	if len(words) == 2 && words[1] == "option" {
		p := parserOf(words[0])
		p.option = true
		return p
	}
	typeName := ""
	if len(words) > 0 {
		typeName = words[0]
	}
	p := columnParser{typeName: typeName}
	// lint: sort until '^\t}' where '^\tcase '
	switch typeName {
	case "bool":
		p.base = boolBase
	case "date":
		p.base = dateBase
	case "decimal":
		p.base = decimalBase
	case "double", "real":
		p.base = realBase
	case "int":
		p.base = intBase
	default:
		p.typeName = "string"
	}
	return p
}

// typ is the Morel type of the values this parser produces.
func (p columnParser) typ(sys *types.System) types.Type {
	var t types.Type
	// lint: sort until '^\t}' where '^\tcase '
	switch p.base {
	case boolBase:
		t = sys.Bool
	case dateBase:
		t = sys.Named("date")
	case decimalBase:
		t = sys.Named("decimal")
	case intBase:
		t = sys.Int
	case realBase:
		t = sys.Real
	case stringBase:
		t = sys.String
	}
	if p.option {
		return sys.Named("option", t)
	}
	return t
}

// parse converts one field of column name.
func (p columnParser) parse(name, s string) (Val, error) {
	isNull := s == "" || s == "NULL"
	if p.option {
		if isNull {
			return noneVal, nil
		}
		v, err := p.value(name, s)
		if err != nil {
			return nil, err
		}
		return someVal(v), nil
	}
	if p.base == stringBase && s != "NULL" ||
		p.base != stringBase && !isNull {
		return p.value(name, s)
	}
	return nil, fileFail(
		"null in column %s, whose type %s has no room for it",
		name, p.typeName)
}

// value converts a field that is not null.
func (p columnParser) value(name, s string) (Val, error) {
	u := csv.Unquote(s)
	invalid := func() (Val, error) {
		return nil, fileFail(
			"value %s in column %s is not a valid %s",
			s, name, p.typeName)
	}
	// lint: sort until '^\t}' where '^\tcase '
	switch p.base {
	case boolBase:
		switch u {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
		return invalid()
	case dateBase:
		// Midnight UTC on that day; that is also how a Spark date is
		// read.
		t, err := time.Parse("2006-01-02", u)
		if err != nil {
			return invalid()
		}
		return t, nil
	case decimalBase:
		d, ok := DecParseExact(u)
		if !ok {
			return invalid()
		}
		return d, nil
	case intBase:
		n, err := strconv.ParseInt(u, 10, 32)
		if err != nil {
			return invalid()
		}
		return int32(n), nil
	case realBase:
		f, err := strconv.ParseFloat(u, 32)
		if err != nil {
			return invalid()
		}
		return float32(f), nil
	case stringBase:
		return u, nil
	}
	return u, nil
}

// fileFail is the Fail that reading a file raises, which has no
// position.
func fileFail(format string, args ...any) *MorelError {
	return &MorelError{
		Exn: ExnFail, NoPos: true,
		ExnValue: Con{Name: ExnFail, Arg: fmt.Sprintf(format, args...)},
	}
}

// splitFields splits a line of a data file into its fields, each as
// written, quotes and all. A field that starts with a quote runs to
// the closing quote, so it may contain a comma; a doubled quote within
// it stands for one quote, and is made single. Anything between the
// closing quote and the comma is part of the field.
func splitFields(line string) []string {
	var fields []string
	n := len(line)
	i := 0
	for {
		var field string
		if i < n && line[i] == '\'' {
			field, i = quotedField(line, i)
		} else {
			start := i
			comma := strings.IndexByte(line[i:], ',')
			if comma < 0 {
				i = n
			} else {
				i += comma
			}
			field = line[start:i]
		}
		fields = append(fields, field)
		if i >= n {
			return fields
		}
		i++ // skip the comma
	}
}

// quotedField reads the quoted field that starts at line[i], up to the
// comma after it, and returns it, with each doubled quote made single,
// and the position of that comma (or of the end of the line).
func quotedField(line string, i int) (string, int) {
	n := len(line)
	var b strings.Builder
	b.WriteByte('\'')
	i++
	for i < n {
		c := line[i]
		i++
		if c != '\'' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('\'')
		if i < n && line[i] == '\'' {
			i++
			continue
		}
		break
	}
	for i < n && line[i] != ',' {
		b.WriteByte(line[i])
		i++
	}
	return b.String(), i
}

// NewFile returns the File at a path, unexpanded. Its kind comes
// from the path: whether it is a directory, and what its name ends
// with.
func NewFile(path string) *File {
	kind := kindOf(path)
	name := filepath.Base(path)
	return &File{
		Path:     path,
		BaseName: strings.TrimSuffix(name, kind.suffix()),
		Kind:     kind,
	}
}

// kindOf deduces a file's kind from its path.
func kindOf(path string) FileKind {
	info, err := os.Stat(path)
	if err == nil && info.IsDir() {
		return DirKind
	}
	name := filepath.Base(path)
	for _, k := range dataKinds {
		if strings.HasSuffix(name, k.suffix()) {
			return k
		}
	}
	return PlainKind
}

// Expand looks at the file — listing a directory, or reading a
// data file's header — and records what it found. It runs at most
// once per file; later calls do nothing.
//
// Expanding is what widens a file's type, so it happens only when
// a field is named (see DiscoverField), never when a value is
// printed or read.
func (f *File) Expand() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.expand()
}

// Type returns the file's type as it is currently known.
//
// An unexpanded file's type says only what its name does: a data
// file is a list of records whose fields are not yet known, and
// anything else is a record whose fields are not yet known. An
// expanded directory's type has a field per entry, itself
// progressive; an expanded data file's type is a list of a plain
// record, because a data file's fields are all known at once.
func (f *File) Type(sys *types.System) types.Type {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.typ(sys)
}

// DiscoverField widens the file's type to include a field, by
// expanding the entry of that name. It reports whether the type
// widened, so that a caller deducing a type knows to try again
// against the wider one.
//
// This is the only way a file's type grows.
func (f *File) DiscoverField(name string) bool {
	f.mu.Lock()
	widened := !f.expanded
	f.expand()
	child := f.child(name)
	f.mu.Unlock()
	if child == nil {
		return widened
	}
	child.mu.Lock()
	defer child.mu.Unlock()
	if child.expanded {
		return widened
	}
	child.expand()
	return child.expanded || widened
}

// Child returns the entry of the given name, or nil if the file is
// not a directory or has no such entry.
func (f *File) Child(name string) *File {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.child(name)
}

// ChildAt returns the entry at a slot of the directory's record
// type, or nil if there is none. Compiled code selects a field by
// slot, so this is the runtime counterpart of Type's field order.
func (f *File) ChildAt(slot int) *File {
	f.mu.Lock()
	defer f.mu.Unlock()
	if slot < 0 || slot >= len(f.children) {
		return nil
	}
	return f.children[slot]
}

// Children returns the directory's entries, in label order.
func (f *File) Children() []*File {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*File(nil), f.children...)
}

// Field returns the value of the directory's slot-th entry, as
// selecting that field yields it: a data file gives its rows, and
// a sub-directory gives itself, to be browsed further. A slot with
// no entry gives the empty record, as an unbrowsed file does.
func (f *File) Field(slot int) (Val, error) {
	child := f.ChildAt(slot)
	if child == nil {
		return []Val{}, nil
	}
	if child.Kind.IsData() {
		rows, err := child.ReadRows()
		if err != nil {
			return nil, err
		}
		return rows, nil
	}
	return child, nil
}

// ReadRows reads a data file and returns its rows, each a record
// value whose fields are in label order. It returns nil for a
// directory, and for a file it cannot read.
//
// Reading does not expand the file: a file's type must not change
// just because its value was printed. The header is parsed afresh
// here when the file has not been expanded, so that the rows line
// up with whatever type the file is currently reported to have.
func (f *File) ReadRows() ([]Val, error) {
	if !f.Kind.IsData() {
		return nil, nil
	}
	f.mu.Lock()
	row, expanded := f.row, f.expanded
	f.mu.Unlock()
	if !expanded {
		var ok bool
		row, ok = readHeader(f.Path, f.Kind)
		if !ok {
			return nil, nil
		}
	}
	if len(row) == 0 {
		return nil, nil
	}
	r, closer := open(f.Path, f.Kind)
	if r == nil {
		return nil, nil
	}
	defer closer.Close()
	// Skip the header.
	_, err := r.ReadString('\n')
	if err != nil {
		return nil, nil
	}
	var rows []Val
	for {
		line, err := r.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if line != "" {
			v, perr := parseRow(line, row)
			if perr != nil {
				return nil, perr
			}
			rows = append(rows, v)
		}
		if err != nil {
			return rows, nil
		}
	}
}

// expand is Expand, with the lock already held.
func (f *File) expand() {
	if f.expanded {
		return
	}
	// lint: sort until '^\t}' where '^\tcase '
	switch f.Kind {
	case CSVKind, CSVGzKind:
		row, ok := readHeader(f.Path, f.Kind)
		if !ok {
			return
		}
		f.row = row
		f.expanded = true
	case DirKind:
		f.children = readDir(f.Path)
		f.expanded = true
	case PlainKind:
		// Never expands; it has no useful contents.
	}
}

// typ is Type, with the lock already held.
func (f *File) typ(sys *types.System) types.Type {
	if !f.expanded {
		empty := sys.ProgressiveRecord(nil)
		if f.Kind.IsData() {
			return sys.List(empty)
		}
		return empty
	}
	if f.Kind.IsData() {
		if len(f.row) == 0 {
			return sys.List(sys.Unit)
		}
		fields := make([]types.Field, len(f.row))
		for i, c := range f.row {
			fields[i] = types.Field{
				Label: c.label, Type: c.parse.typ(sys),
			}
		}
		return sys.List(sys.Record(fields))
	}
	fields := make([]types.Field, len(f.children))
	for i, child := range f.children {
		fields[i] = types.Field{
			Label: child.BaseName, Type: child.Type(sys),
		}
	}
	return sys.ProgressiveRecord(fields)
}

// child is Child, with the lock already held.
func (f *File) child(name string) *File {
	for _, c := range f.children {
		if c.BaseName == name {
			return c
		}
	}
	return nil
}

// readDir lists a directory's entries as unexpanded files, in
// label order, skipping the ones whose kind we do not recognize.
func readDir(path string) []*File {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil
	}
	var children []*File
	for _, entry := range entries {
		child := NewFile(filepath.Join(path, entry.Name()))
		if child.Kind == PlainKind {
			continue
		}
		children = append(children, child)
	}
	sort.Slice(children, func(i, j int) bool {
		return types.LabelLess(children[i].BaseName,
			children[j].BaseName)
	})
	return children
}

// readHeader reads a data file's first line and converts it to
// columns in label order, each with the parser its declared type
// calls for. The flag is false if the file could not be read, true
// (with no columns) if it is empty.
func readHeader(path string, kind FileKind) ([]column, bool) {
	r, closer := open(path, kind)
	if r == nil {
		return nil, false
	}
	defer closer.Close()
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return nil, false
	}
	header := csv.ParseHeader(strings.TrimRight(line, "\r\n"))
	if len(header) == 0 {
		return nil, true
	}
	columns := make([]column, len(header))
	for i, c := range header {
		columns[i] = column{
			label: c.Name,
			parse: parserOf(c.Type),
			index: c.Index,
		}
	}
	sort.Slice(columns, func(i, j int) bool {
		return types.LabelLess(columns[i].label, columns[j].label)
	})
	return columns, true
}

// open opens a file for reading, decompressing it if its kind says
// it is compressed. It returns a nil reader if the file cannot be
// read; otherwise the caller must close the returned closer, which
// closes the file and the decompressor.
func open(path string, kind FileKind) (*bufio.Reader, io.Closer) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil
	}
	if kind != CSVGzKind {
		return bufio.NewReader(file), file
	}
	gz, err := gzip.NewReader(file)
	if err != nil {
		_ = file.Close()
		return nil, nil
	}
	return bufio.NewReader(gz), closers{gz, file}
}

// closers closes several things, in order.
type closers []io.Closer

func (cs closers) Close() error {
	var err error
	for _, c := range cs {
		e := c.Close()
		if e != nil && err == nil {
			err = e
		}
	}
	return err
}

// parseRow converts one line to a record value: one field per
// column, in label order, each cell taken from the position the
// column's header had and put through its parser. A line too short
// to supply a cell gives that field its type's zero.
func parseRow(line string, row []column) (Val, error) {
	cells := splitFields(line)
	fields := make([]Val, len(row))
	for i, c := range row {
		if c.index >= len(cells) {
			return nil, fileFail(
				"line has %d fields, but the header has %d: %s",
				len(cells), len(row), line)
		}
		v, err := c.parse.parse(c.label, cells[c.index])
		if err != nil {
			return nil, err
		}
		fields[i] = v
	}
	return fields, nil
}

// End file.go
