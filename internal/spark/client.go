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

package spark

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"os"
	"os/user"
	"strings"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/hydromatic/morel-go/internal/eval"
	"github.com/hydromatic/morel-go/internal/spark/connect"
	"github.com/hydromatic/morel-go/internal/types"
)

// A connection to a Spark Connect server, over gRPC; morel-java's
// SparkConnection.

const (
	// defaultPort is the default port of a Spark Connect server.
	defaultPort = "15002"
	// clientType is what the client calls itself to the server.
	clientType = "morel"
	// nullableProperty is the table property that says which of a
	// table's columns may be null, because Spark keeps no such
	// constraint of its own.
	nullableProperty = "morel.nullable"
	// callTimeout bounds each call to the server, so that a server
	// that has stopped answering does not hang the session.
	callTimeout = 10 * time.Minute
	// maxMessageSize is the largest message the client accepts; an
	// Arrow batch is about 4 MiB, gRPC's default limit.
	maxMessageSize = 256 << 20
)

// client is a connection to a server.
type client struct {
	sys         *types.System
	uri         string
	sessionID   string
	userContext *connect.UserContext
	conn        *grpc.ClientConn
	stub        connect.SparkConnectServiceClient
	// nullableColumns are the columns the URI declares may be null,
	// as "database.table.column" in lower case.
	nullableColumns map[string]bool
}

// Connect opens a connection to a Spark Connect server. Opening
// costs nothing until the connection is used: the channel does not
// dial until a call, so a server that is not there is reported where
// it is noticed.
func Connect(sys *types.System, uri string) (*eval.SparkConnection,
	error,
) {
	c, err := newClient(sys, uri)
	if err != nil {
		return nil, err
	}
	return eval.NewSparkConnection(uri, c), nil
}

func newClient(sys *types.System, uri string) (*client, error) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "sc" || u.Hostname() == "" {
		return nil, eval.NewSparkException("CONNECTION",
			"Invalid Spark Connect URI: "+uri+
				" (expected sc://host[:port][/;param=value...])")
	}
	port := u.Port()
	if port == "" {
		port = defaultPort
	}
	userID := "morel"
	current, uerr := user.Current()
	if uerr == nil {
		userID = current.Username
	}
	nullable := map[string]bool{}
	// Parameters follow the path, separated by ';', as in
	// "sc://host:port/;user_id=fred;token=...".
	for param := range strings.SplitSeq(u.Path, ";") {
		key, value, found := strings.Cut(param, "=")
		if !found || key == "" {
			continue
		}
		// lint: sort until '^\t\t}' where '^\t\tcase '
		switch key {
		case "nullable":
			// Which columns may be null, as "database.table.column",
			// separated by commas. Spark cannot say: it calls every
			// column of a stored table nullable, whatever the DDL
			// said.
			for column := range strings.SplitSeq(value, ",") {
				if column != "" {
					nullable[strings.ToLower(column)] = true
				}
			}
		case "user_id":
			userID = value
		}
	}
	conn, err := grpc.NewClient(u.Hostname()+":"+port,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(maxMessageSize)))
	if err != nil {
		return nil, eval.NewSparkException("CONNECTION", err.Error())
	}
	return &client{
		sys:             sys,
		uri:             uri,
		sessionID:       newUUID(),
		userContext:     &connect.UserContext{UserId: userID},
		conn:            conn,
		stub:            connect.NewSparkConnectServiceClient(conn),
		nullableColumns: nullable,
	}, nil
}

// newUUID returns a random UUID, which identifies the session to the
// server.
func newUUID() string {
	const (
		size        = 16
		version4    = 0x40
		versionMask = 0x0f
		variant     = 0x80
		variantMask = 0x3f
	)
	b := make([]byte, size)
	_, _ = rand.Read(b)
	b[6] = b[6]&versionMask | version4
	b[8] = b[8]&variantMask | variant
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" +
		h[20:]
}

// Close implements eval.SparkBackend.
func (c *client) Close() { _ = c.conn.Close() }

// Execute implements eval.SparkBackend.
func (c *client) Execute(plan any, elemType types.Type) ([]eval.Val,
	error,
) {
	p, isPlan := plan.(*Plan)
	if !isPlan {
		return nil, eval.NewSparkException("UNSUPPORTED",
			"Not a Spark plan")
	}
	_, batches, err := c.batches(p.Proto().GetRoot())
	if err != nil {
		return nil, err
	}
	return DecodeArrow(batches, elemType)
}

// Databases implements eval.SparkBackend.
func (c *client) Databases() ([]string, error) {
	return c.column("SHOW DATABASES", "namespace")
}

// Tables implements eval.SparkBackend.
func (c *client) Tables(database string) ([]string, error) {
	return c.column("SHOW TABLES IN "+quote(database), "tableName")
}

// TableType implements eval.SparkBackend.
func (c *client) TableType(database, table string) (types.Type, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	response, err := c.stub.AnalyzePlan(ctx, &connect.AnalyzePlanRequest{
		SessionId:   c.sessionID,
		UserContext: c.userContext,
		ClientType:  proto.String(clientType),
		Analyze: &connect.AnalyzePlanRequest_Schema_{
			Schema: &connect.AnalyzePlanRequest_Schema{
				Plan: &connect.Plan{
					OpType: &connect.Plan_Root{Root: read(database, table)},
				},
			},
		},
	})
	if err != nil {
		return nil, toException(err)
	}
	nullable, err := c.nullableColumnsOf(database, table)
	if err != nil {
		return nil, err
	}
	rowType, err := RowType(c.sys, response.GetSchema().GetSchema(),
		nullable)
	if err != nil {
		return nil, err
	}
	return c.sys.Bag(rowType), nil
}

// Rows implements eval.SparkBackend. The rows are decoded as the
// type this connection says the table has, which is not always the
// type Spark's own schema would give: a declared column is an option
// here and a plain column there, and the rows must match what the
// program was typed against.
func (c *client) Rows(database, table string) ([]eval.Val, error) {
	t, err := c.TableType(database, table)
	if err != nil {
		return nil, err
	}
	_, batches, err := c.batches(read(database, table))
	if err != nil {
		return nil, err
	}
	return DecodeArrow(batches, types.ElemOf(t))
}

// sql runs a SQL query, and returns the type of its rows and the
// rows.
func (c *client) sql(query string) (types.Type, []eval.Val, error) {
	schema, batches, err := c.batches(&connect.Relation{
		RelType: &connect.Relation_Sql{Sql: &connect.SQL{Query: query}},
	})
	if err != nil {
		return nil, nil, err
	}
	rowType, err := RowType(c.sys, schema, nil)
	if err != nil {
		return nil, nil, err
	}
	rows, err := DecodeArrow(batches, rowType)
	return rowType, rows, err
}

// column runs a SQL query and returns a column of its result.
func (c *client) column(query, name string) ([]string, error) {
	rowType, rows, err := c.sql(query)
	if err != nil {
		return nil, err
	}
	names, _ := columns(rowType)
	i := -1
	for k, n := range names {
		if n == name {
			i = k
		}
	}
	if i < 0 {
		return nil, eval.NewSparkException("CONNECTION",
			"Result has no column "+name)
	}
	values := make([]string, len(rows))
	for k, row := range rows {
		fields, _ := row.([]eval.Val)
		values[k], _ = fields[i].(string)
	}
	return values, nil
}

// batches runs a plan whose root is the given relation, and returns
// what comes back undecoded: the schema, and the Arrow streams. A
// caller that knows what Morel type it wants decodes for itself.
func (c *client) batches(root *connect.Relation) (*connect.DataType,
	[][]byte, error,
) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	stream, err := c.stub.ExecutePlan(ctx, &connect.ExecutePlanRequest{
		SessionId:   c.sessionID,
		UserContext: c.userContext,
		ClientType:  proto.String(clientType),
		Plan:        &connect.Plan{OpType: &connect.Plan_Root{Root: root}},
	})
	if err != nil {
		return nil, nil, toException(err)
	}
	var schema *connect.DataType
	var batches [][]byte
	for {
		response, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, toException(err)
		}
		if s := response.GetSchema(); s != nil {
			schema = s
		}
		if b := response.GetArrowBatch(); b != nil {
			batches = append(batches, b.GetData())
		}
	}
	if schema == nil {
		return nil, nil, eval.NewSparkException("CONNECTION",
			"Spark returned no schema for the query")
	}
	return schema, batches, nil
}

// toException converts a gRPC failure into a Spark exception,
// carrying Spark's error class if the server sent one: Spark attaches
// a google.rpc.ErrorInfo to the status, whose metadata includes
// "errorClass", Spark's error condition (e.g. "DIVIDE_BY_ZERO"),
// when the error has one, and whose reason is the Java exception
// class.
func toException(err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return eval.NewSparkException("CONNECTION", err.Error())
	}
	errorClass := "CONNECTION"
	for _, detail := range st.Details() {
		if info, isInfo := detail.(*errdetails.ErrorInfo); isInfo {
			if condition, found := info.GetMetadata()["errorClass"]; found {
				errorClass = condition
			} else {
				errorClass = info.GetReason()
			}
		}
	}
	message := st.Message()
	if message == "" {
		message = st.String()
	}
	return eval.NewSparkException(errorClass, message)
}

// read returns a relation that reads a table.
func read(database, table string) *connect.Relation {
	name := quote(database) + "." + quote(table)
	return &connect.Relation{
		RelType: &connect.Relation_Read{
			Read: &connect.Read{
				ReadType: &connect.Read_NamedTable_{
					NamedTable: &connect.Read_NamedTable{
						UnparsedIdentifier: name,
					},
				},
			},
		},
	}
}

// quote quotes an identifier with back-ticks, as Spark SQL does.
func quote(identifier string) string {
	return "`" + strings.ReplaceAll(identifier, "`", "``") + "`"
}

// nullableColumnsOf returns which of a table's columns may be null,
// from whichever source says.
//
// The connection's own declaration comes first, because whoever
// wrote the URI is looking at this session; then the table's
// property, which is what the table says about itself; and if
// neither speaks, no column is optional, and a null in one is an
// error that names it.
func (c *client) nullableColumnsOf(database, table string) (
	map[string]bool, error,
) {
	if c.nullableColumns["*"] {
		return map[string]bool{"*": true}, nil
	}
	prefix := strings.ToLower(database + "." + table + ".")
	columns := map[string]bool{}
	for column := range c.nullableColumns {
		if rest, found := strings.CutPrefix(column, prefix); found {
			columns[rest] = true
		}
	}
	if len(columns) > 0 {
		return columns, nil
	}
	return c.propertyNullableColumns(database, table)
}

// propertyNullableColumns returns the columns that a table's own
// metadata declares nullable. The property is a complete answer for
// the table it is on: a column it names may be null, and a column it
// does not name may not.
func (c *client) propertyNullableColumns(database, table string) (
	map[string]bool, error,
) {
	_, rows, err := c.sql("SHOW TBLPROPERTIES " + quote(database) + "." +
		quote(table))
	if err != nil {
		return nil, err
	}
	columns := map[string]bool{}
	for _, row := range rows {
		pair, _ := row.([]eval.Val)
		const keyValue = 2
		if len(pair) != keyValue {
			continue
		}
		// The record's fields are in label order: key, value.
		key, _ := pair[0].(string)
		value, isString := pair[1].(string)
		if key != nullableProperty || !isString {
			continue
		}
		for column := range strings.SplitSeq(value, ",") {
			if trimmed := strings.ToLower(strings.TrimSpace(column)); trimmed != "" {
				columns[trimmed] = true
			}
		}
	}
	return columns, nil
}

// DefaultURI returns the URI of the default server, which the
// environment variable SPARK_REMOTE names.
func DefaultURI() (string, error) {
	uri := os.Getenv("SPARK_REMOTE")
	if uri == "" {
		return "", eval.NewSparkException("CONNECTION",
			"No default Spark Connect server: SPARK_REMOTE is not set")
	}
	return uri, nil
}

// End client.go
