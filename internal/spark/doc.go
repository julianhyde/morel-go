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

// Package spark is Morel's adapter for Apache Spark: it translates a
// query, as the relational tree holds it, to a Spark Connect plan,
// prints the plan as morel-java prints it, and runs it on a server.
//
// It is morel-java's "spark" package. The plan is a protobuf message
// of the Spark Connect protocol, whose Go code is package "connect";
// the function table, "functions.sml", is shared with the other
// implementations of Morel.
package spark

// End doc.go
