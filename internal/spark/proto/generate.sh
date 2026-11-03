#!/bin/bash
# Licensed to Julian Hyde under one or more contributor license
# agreements.  See the NOTICE file distributed with this work
# for additional information regarding copyright ownership.
# Julian Hyde licenses this file to you under the Apache
# License, Version 2.0 (the "License"); you may not use this
# file except in compliance with the License.  You may obtain a
# copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing,
# software distributed under the License is distributed on an
# "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND,
# either express or implied.  See the License for the specific
# language governing permissions and limitations under the
# License.
#
# Regenerates the Go code of the Spark Connect protocol, package
# "connect", from the protos in spark/connect, which are Spark 4.0's
# and the same files morel-java vendors (src/main/protobuf). Needs
# protoc, protoc-gen-go and protoc-gen-go-grpc on the path:
#
#   go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
#   go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
#
# Run it from this directory, or via "go generate" in ../connect.
set -e
cd "$(dirname "$0")"
pkg=github.com/hydromatic/morel-go/internal/spark/connect
opts=()
for f in spark/connect/*.proto; do
  opts+=("--go_opt=M$f=$pkg;connect" "--go-grpc_opt=M$f=$pkg;connect")
done
out=$(mktemp -d)
protoc -I . --go_out="$out" --go_opt=paths=source_relative \
  --go-grpc_out="$out" --go-grpc_opt=paths=source_relative \
  "${opts[@]}" spark/connect/*.proto
cp "$out"/spark/connect/*.go ../connect/
rm -rf "$out"
