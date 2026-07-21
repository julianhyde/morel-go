#!/usr/bin/env python3
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
"""Survey morel-go vs morel-java .smli divergences.

For each shared .smli file, replay morel-java's copy through the
morel-go binary and compare per statement. For every statement
morel-go does not reproduce, record the statement's first token(s)
and morel-go's actual output (an error, or empty), so divergences
can be bucketed and classified.
"""
import os
import re
import sys
import collections

sys.path.insert(0, os.path.join(os.path.dirname(__file__)))
# Import helpers from etc/pull-passing.py.
import importlib.util
spec = importlib.util.spec_from_file_location(
    "pp",
    os.path.expanduser("~/dev/morel-go.0/etc/pull-passing.py"))
pp = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pp)

JAVA = os.path.expanduser(
    "~/dev/morel.0/src/test/resources/script")
GO = os.path.expanduser("~/dev/morel-go.0/testdata/script")


def norm(lines):
    return "\n".join(lines).strip()


def first_code_line(body_lines):
    """The statement's first non-blank code line, trimmed."""
    for line in body_lines:
        s = line.strip()
        if s and not s.startswith("(*"):
            return s
    return ""


def signature(stmt, go_out):
    """A bucket key for a divergence: the go error kind, or the
    statement's leading construct when go is silent."""
    go = norm(go_out)
    if go:
        # An error/output line: bucket by the error gist.
        m = re.search(r"Error: (.*)", go)
        if m:
            g = m.group(1)
            g = re.sub(r"'[^']*'", "'X'", g)
            g = re.sub(r"[0-9]+", "N", g)
            return "ERR: " + g[:60]
        return "OUT: " + go.splitlines()[0][:60]
    # Silent: bucket by the leading token/construct.
    code = first_code_line(stmt)
    tok = re.match(r"[`A-Za-z_][\w.]*|\S", code)
    return "SILENT: " + (tok.group(0) if tok else "?")


def main():
    binary = pp.build_morel()
    run = pp.make_runner(binary)
    shared = []
    for rel in sorted(pp.smli_files(GO)):
        if os.path.exists(os.path.join(JAVA, rel)):
            shared.append(rel)

    buckets = collections.defaultdict(list)
    for rel in shared:
        with open(os.path.join(JAVA, rel)) as f:
            java_text = f.read()
        units = pp.segment(java_text)
        try:
            replay = run(java_text)
            go_outs = pp.go_outputs(replay, units)
        except RuntimeError:
            buckets["REPLAY-MISMATCH (whole file)"].append(rel)
            continue
        for (inp, jout), gout in zip(units, go_outs):
            if not norm(jout):
                continue  # not a statement with output
            if norm(jout) == norm(gout):
                continue  # reproduced
            sig = signature(inp, gout)
            buckets[sig].append(
                (rel, first_code_line(inp)[:70]))

    # Report, most frequent first.
    items = sorted(buckets.items(),
                   key=lambda kv: -len(kv[1]))
    total = sum(len(v) for v in buckets.values())
    print(f"=== {total} divergent statements across "
          f"{len(shared)} shared files ===\n")
    for sig, occ in items:
        print(f"[{len(occ):4}] {sig}")
    print("\n\n=== per-bucket examples (file: statement) ===")
    for sig, occ in items:
        print(f"\n[{len(occ)}] {sig}")
        seen_files = collections.Counter(
            o[0] for o in occ if isinstance(o, tuple))
        for f, c in seen_files.most_common(6):
            ex = next(o[1] for o in occ
                      if isinstance(o, tuple) and o[0] == f)
            print(f"    {f} (x{c}): {ex}")


if __name__ == "__main__":
    main()
