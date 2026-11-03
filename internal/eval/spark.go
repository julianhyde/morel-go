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
	"sort"
	"sync"

	"github.com/hydromatic/morel-go/internal/types"
)

// The values of the Spark structure: a connection, the record that
// "Spark.connect" returns, and the catalog it browses.
//
// This port has no Spark adapter, so the only connection it can
// open is the offline one, "mock:", whose catalog is the session's
// foreign data sets. Connecting to a server, and preparing a plan,
// raise "Spark" with error class CONNECTION, as morel-java does when
// its adapter is absent.

// ExnSpark is the exception that every Spark failure raises.
const ExnSpark = "Spark"

// catalogLabel is the label of the catalog in the record that
// "Spark.connect" returns.
const catalogLabel = "catalog"

// MockSparkURI is the URI that opens the offline connection.
const MockSparkURI = "mock:"

// SparkAdapterMessage is the message of the error raised by an
// operation that needs the Spark adapter, which this port lacks.
const SparkAdapterMessage = "The Spark adapter is not available; it " +
	"needs JDK 17 or later and the 'spark' Maven profile"

// SparkError returns the Morel exception "Spark {errorClass,
// message}". It reports as "Spark [Spark: CLASS: message]".
func SparkError(errorClass, message string) *MorelError {
	return &MorelError{
		Exn:  ExnSpark,
		Desc: ExnSpark + ": " + errorClass + ": " + message,
		ExnValue: Con{
			Name: ExnSpark,
			// The payload record, its fields in label order.
			Arg: []Val{errorClass, message},
		},
	}
}

// Browsable is a value whose type is progressive: a record whose
// fields are discovered by browsing, as the file system that
// "file" denotes and a Spark catalog are. A name bound to one is
// typed afresh at each use, so that it sees what has been
// discovered since it was bound.
type Browsable interface {
	// Browse looks at the value itself, if it has not yet, so that
	// its type lists the entries that a field selection can name.
	Browse() error
	// Type returns the value's type as it is currently known.
	Type(sys *types.System) types.Type
	// Discover widens the type to include a field, and reports
	// whether it widened.
	Discover(name string) (bool, error)
	// Entry returns the entry of the given name if it is itself
	// browsable, otherwise nil.
	Entry(name string) Browsable
}

// FieldSelector is a value that computes its fields when they are
// selected, rather than holding them in a list.
type FieldSelector interface {
	// SelectField returns the value of the slot-th field, in
	// label order.
	SelectField(slot int) (Val, error)
	// FieldVals returns the values of the fields as a record value
	// prints them, in label order.
	FieldVals() []Val
}

// Browse implements Browsable.
func (f *File) Browse() error {
	f.Expand()
	return nil
}

// Discover implements Browsable.
func (f *File) Discover(name string) (bool, error) {
	return f.DiscoverField(name), nil
}

// Entry implements Browsable.
func (f *File) Entry(name string) Browsable {
	if child := f.Child(name); child != nil {
		return child
	}
	return nil
}

// SparkDataSet is a foreign data set of a session, which the
// offline connection presents as a database: a record type whose
// fields are tables, and a record value holding their rows.
type SparkDataSet struct {
	Type  *types.Record
	Value []Val
}

// SparkConnection is a connection to Spark. Only the offline one
// exists in this port; its catalog is the session's data sets.
type SparkConnection struct {
	uri      string
	dataSets map[string]SparkDataSet

	mu      sync.Mutex
	closed  bool
	catalog *SparkCatalog
}

// NewMockSparkConnection returns the offline connection over a
// session's data sets.
func NewMockSparkConnection(
	dataSets map[string]SparkDataSet,
) *SparkConnection {
	return &SparkConnection{uri: MockSparkURI, dataSets: dataSets}
}

// String returns how a connection prints.
func (c *SparkConnection) String() string {
	return "<connection " + c.uri + ">"
}

// Catalog returns the connection's catalog, creating it on first
// use, so that discovery accumulates.
func (c *SparkConnection) Catalog() *SparkCatalog {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.catalog == nil {
		c.catalog = &SparkCatalog{connection: c}
	}
	return c.catalog
}

// Close closes the connection and forgets its catalog; any later
// use raises "Spark" with error class CLOSED.
func (c *SparkConnection) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.catalog = nil
	c.closed = true
}

func (c *SparkConnection) checkOpen() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return SparkError("CLOSED",
			"Connection to "+c.uri+" is closed")
	}
	return nil
}

// databases returns the names of the databases, in label order.
func (c *SparkConnection) databases() ([]string, error) {
	err := c.checkOpen()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(c.dataSets))
	for name := range c.dataSets {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		return types.LabelLess(names[i], names[j])
	})
	return names, nil
}

func (c *SparkConnection) database(name string) (SparkDataSet, error) {
	err := c.checkOpen()
	if err != nil {
		return SparkDataSet{}, err
	}
	ds, ok := c.dataSets[name]
	if !ok {
		return SparkDataSet{}, SparkError("SCHEMA_NOT_FOUND",
			"Database "+name+" not found")
	}
	return ds, nil
}

// tables returns the names of a database's tables, in label order.
func (c *SparkConnection) tables(database string) ([]string, error) {
	ds, err := c.database(database)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(ds.Type.Fields))
	for i, f := range ds.Type.Fields {
		names[i] = f.Label
	}
	return names, nil
}

func (c *SparkConnection) tableIndex(database, table string) (
	SparkDataSet, int, error,
) {
	ds, err := c.database(database)
	if err != nil {
		return SparkDataSet{}, 0, err
	}
	for i, f := range ds.Type.Fields {
		if f.Label == table {
			return ds, i, nil
		}
	}
	return SparkDataSet{}, 0, SparkError("TABLE_OR_VIEW_NOT_FOUND",
		"Table "+database+"."+table+" not found")
}

// tableType returns the type of a table: a bag of records.
func (c *SparkConnection) tableType(database, table string) (
	types.Type, error,
) {
	ds, i, err := c.tableIndex(database, table)
	if err != nil {
		return nil, err
	}
	return ds.Type.Fields[i].Type, nil
}

// rows returns the rows of a table.
func (c *SparkConnection) rows(database, table string) ([]Val, error) {
	ds, i, err := c.tableIndex(database, table)
	if err != nil {
		return nil, err
	}
	if i >= len(ds.Value) {
		return nil, nil
	}
	return asList(ds.Value[i]), nil
}

// SparkConnectionRecord is the record "{catalog, connection}" that
// "Spark.connect" returns. Its type carries the catalog's current
// type, so that the type of a name bound to it grows as the catalog
// is browsed.
type SparkConnectionRecord struct {
	Connection *SparkConnection
	Catalog    *SparkCatalog
}

// Browse implements Browsable.
func (r *SparkConnectionRecord) Browse() error { return nil }

// Type implements Browsable.
func (r *SparkConnectionRecord) Type(sys *types.System) types.Type {
	return sys.ProgressiveRecord([]types.Field{
		{Label: catalogLabel, Type: r.Catalog.Type(sys)},
		{Label: "connection", Type: sys.Named("connection")},
	})
}

// Discover implements Browsable.
func (r *SparkConnectionRecord) Discover(name string) (bool, error) {
	if name != catalogLabel {
		return false, nil
	}
	return r.Catalog.browse()
}

// Entry implements Browsable.
func (r *SparkConnectionRecord) Entry(name string) Browsable {
	if name == catalogLabel {
		return r.Catalog
	}
	return nil
}

// SelectField implements FieldSelector.
func (r *SparkConnectionRecord) SelectField(slot int) (Val, error) {
	return r.FieldVals()[slot], nil
}

// FieldVals implements FieldSelector.
func (r *SparkConnectionRecord) FieldVals() []Val {
	return []Val{r.Catalog, r.Connection}
}

// SparkCatalog is a Spark catalog as a progressively typed record:
// databases, then tables, then a table's rows. Nothing is fetched
// until it is needed: the databases when the catalog is first
// browsed, a database's tables when the database is first
// discovered, a table's type when the table is first discovered,
// and its rows when it is read.
type SparkCatalog struct {
	connection *SparkConnection

	mu        sync.Mutex
	databases []*SparkDatabase
	fetched   bool
}

// Browse implements Browsable.
func (c *SparkCatalog) Browse() error {
	_, err := c.browse()
	return err
}

// Type implements Browsable.
func (c *SparkCatalog) Type(sys *types.System) types.Type {
	databases := c.list()
	fields := make([]types.Field, len(databases))
	for i, d := range databases {
		fields[i] = types.Field{Label: d.name, Type: d.Type(sys)}
	}
	return sys.ProgressiveRecord(fields)
}

// Discover implements Browsable.
func (c *SparkCatalog) Discover(name string) (bool, error) {
	widened, err := c.browse()
	if err != nil {
		return false, err
	}
	d := c.find(name)
	if d == nil {
		return widened, nil
	}
	fetched, err := d.browse()
	return widened || fetched, err
}

// Entry implements Browsable.
func (c *SparkCatalog) Entry(name string) Browsable {
	if d := c.find(name); d != nil {
		return d
	}
	return nil
}

// SelectField implements FieldSelector.
func (c *SparkCatalog) SelectField(slot int) (Val, error) {
	_, err := c.browse()
	if err != nil {
		return nil, err
	}
	databases := c.list()
	if slot < 0 || slot >= len(databases) {
		return []Val{}, nil
	}
	return databases[slot], nil
}

// FieldVals implements FieldSelector.
func (c *SparkCatalog) FieldVals() []Val {
	databases := c.list()
	vals := make([]Val, len(databases))
	for i, d := range databases {
		vals[i] = d
	}
	return vals
}

// browse fetches the databases, if they have not been fetched, and
// reports whether it did.
func (c *SparkCatalog) browse() (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fetched {
		return false, nil
	}
	names, err := c.connection.databases()
	if err != nil {
		return false, err
	}
	for _, name := range names {
		c.databases = append(c.databases,
			&SparkDatabase{connection: c.connection, name: name})
	}
	c.fetched = true
	return true, nil
}

func (c *SparkCatalog) list() []*SparkDatabase {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.databases
}

func (c *SparkCatalog) find(name string) *SparkDatabase {
	for _, d := range c.list() {
		if d.name == name {
			return d
		}
	}
	return nil
}

// SparkDatabase is a database of a catalog: a progressive record of
// tables.
type SparkDatabase struct {
	connection *SparkConnection
	name       string

	mu      sync.Mutex
	tables  []*SparkTable
	fetched bool
}

// Browse implements Browsable.
func (d *SparkDatabase) Browse() error {
	_, err := d.browse()
	return err
}

// Type implements Browsable. A database whose tables have not been
// fetched is "{...}".
func (d *SparkDatabase) Type(sys *types.System) types.Type {
	tables := d.list()
	fields := make([]types.Field, len(tables))
	for i, t := range tables {
		fields[i] = types.Field{Label: t.name, Type: t.Type(sys)}
	}
	return sys.ProgressiveRecord(fields)
}

// Discover implements Browsable.
func (d *SparkDatabase) Discover(name string) (bool, error) {
	widened, err := d.browse()
	if err != nil {
		return false, err
	}
	t := d.find(name)
	if t == nil {
		return widened, nil
	}
	fetched, err := t.browse()
	return widened || fetched, err
}

// Entry implements Browsable. A table is not browsable: its type is
// known all at once.
func (d *SparkDatabase) Entry(string) Browsable { return nil }

// SelectField implements FieldSelector. Selecting a table reads its
// rows.
func (d *SparkDatabase) SelectField(slot int) (Val, error) {
	_, err := d.browse()
	if err != nil {
		return nil, err
	}
	tables := d.list()
	if slot < 0 || slot >= len(tables) {
		return []Val{}, nil
	}
	return tables[slot].Rows()
}

// FieldVals implements FieldSelector.
func (d *SparkDatabase) FieldVals() []Val {
	tables := d.list()
	vals := make([]Val, len(tables))
	for i, t := range tables {
		vals[i] = t
	}
	return vals
}

// browse fetches the tables, if they have not been fetched, and
// reports whether it did.
func (d *SparkDatabase) browse() (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.fetched {
		return false, nil
	}
	names, err := d.connection.tables(d.name)
	if err != nil {
		return false, err
	}
	for _, name := range names {
		d.tables = append(d.tables, &SparkTable{database: d, name: name})
	}
	d.fetched = true
	return true, nil
}

func (d *SparkDatabase) list() []*SparkTable {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.tables
}

func (d *SparkDatabase) find(name string) *SparkTable {
	for _, t := range d.list() {
		if t.name == name {
			return t
		}
	}
	return nil
}

// SparkTable is a table of a database: a bag of records, whose
// type is fetched when it is first discovered and whose rows are
// fetched when they are first read. Inside a record it prints as
// "<relation>".
type SparkTable struct {
	database *SparkDatabase
	name     string

	mu   sync.Mutex
	typ  types.Type
	rows []Val
	read bool
}

// Type returns the table's type: "{...} bag" until it is
// discovered.
func (t *SparkTable) Type(sys *types.System) types.Type {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.typ == nil {
		return sys.Bag(sys.ProgressiveRecord(nil))
	}
	return t.typ
}

// Rows returns the table's rows, fetching them on first use.
func (t *SparkTable) Rows() (Val, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.read {
		rows, err := t.database.connection.rows(t.database.name,
			t.name)
		if err != nil {
			return nil, err
		}
		t.rows = rows
		t.read = true
	}
	return t.rows, nil
}

// browse fetches the table's type, if it has not been fetched, and
// reports whether it did.
func (t *SparkTable) browse() (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.typ != nil {
		return false, nil
	}
	typ, err := t.database.connection.tableType(t.database.name, t.name)
	if err != nil {
		return false, err
	}
	t.typ = typ
	return true, nil
}

// End spark.go
