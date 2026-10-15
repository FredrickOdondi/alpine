#!/bin/sh
# record.sh regenerates testdata/parsers/* from real tool runs on the
# fixture repos in testdata/fixtures. It is a dev-time helper: tests only
# read the recorded files and never need these tools.
#
# Usage: TOOLS_BIN=/path/to/go/bin NODE_TOOLS=/path/with/node_modules sh testdata/record.sh
#   TOOLS_BIN   dir containing golangci-lint, gocyclo, deadcode
#   NODE_TOOLS  dir whose node_modules has eslint, @eslint/js, typescript,
#               typescript-eslint, jscpd, knip, vitest, jest
# Absolute paths in the output are rewritten to /repo and /tools so the
# recordings are machine-independent. See testdata/README.md for versions.
set -u
here=$(cd "$(dirname "$0")" && pwd)
out=$here/parsers
: "${TOOLS_BIN:?set TOOLS_BIN}" "${NODE_TOOLS:?set NODE_TOOLS}"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
export GOWORK=off GOFLAGS=-mod=mod GOTOOLCHAIN=local CI=true NO_COLOR=1 FORCE_COLOR=0
PATH=$TOOLS_BIN:$NODE_TOOLS/node_modules/.bin:$PATH

norm() { # norm <dest>: rewrite machine paths, from stdin
	sed -e "s#$work/[a-z-]*#/repo#g" -e "s#$NODE_TOOLS#/tools#g" -e "s#$TOOLS_BIN#/tools/bin#g" -e "s#$HOME#/home#g" >"$out/$1"
}
fresh() { # fresh <fixture> <name>: copy a fixture into $work/<name>
	cp -R "$here/fixtures/$1" "$work/$2"
	if [ "$1" = ts ]; then ln -s "$NODE_TOOLS/node_modules" "$work/$2/node_modules"; fi
	echo "$work/$2"
}

# --- Go ---
g=$(fresh go go)
(cd "$g" && golangci-lint run --output.json.path=stdout --output.text.path=stderr --show-stats=false ./... 2>/dev/null) | norm golangci_fail.json
(cd "$g" && golangci-lint run --output.json.path=stdout --output.text.path=stderr --show-stats=false ./clean/... 2>/dev/null) | norm golangci_pass.json
(cd "$g" && gocyclo .) | norm gocyclo.txt
(cd "$g" && deadcode ./... 2>&1) | norm deadcode.txt
(cd "$g" && go test -json ./calc/ 2>&1) | norm gotest_fail.jsonl
(cd "$g" && go test -json ./clean/ 2>&1) | norm gotest_pass.jsonl
(cd "$g" && go test -json ./panicky/ 2>&1) | norm gotest_panic.jsonl
b=$(fresh go go-build)
printf 'package calc\n\nfunc broken() int {\n\treturn foo\n}\n' >"$b/calc/broken.go"
(cd "$b" && go test -json ./calc/ 2>&1) | norm gotest_buildfail.jsonl

# --- TypeScript / JavaScript ---
t=$(fresh ts ts)
(cd "$t" && eslint -f json . 2>/dev/null) | norm eslint_fail.json
(cd "$t" && eslint -f json src/dates.ts 2>/dev/null) | norm eslint_pass.json
(cd "$t" && tsc --noEmit --pretty false -p . 2>&1) | norm tsc_fail.txt
(cd "$t" && tsc --noEmit --pretty false -p tsconfig.pass.json 2>&1) | norm tsc_pass.txt
# jscpd 5 names files relative to the scanned path ("a.ts" for src/a.ts), so
# gates should pass --absolute; both forms are recorded.
(cd "$t" && jscpd --silent --absolute --reporters json --output out --min-lines 5 --min-tokens 30 src >/dev/null 2>&1; cat out/jscpd-report.json) | norm jscpd_report.json
(cd "$t" && rm -rf out && jscpd --silent --reporters json --output out --min-lines 5 --min-tokens 30 src >/dev/null 2>&1; cat out/jscpd-report.json) | norm jscpd_relative.json
(cd "$t" && rm -rf out && jscpd --silent --absolute --reporters json --output out src/dates.ts >/dev/null 2>&1; cat out/jscpd-report.json) | norm jscpd_clean.json
(cd "$t" && rm -rf out && knip --reporter json --no-progress 2>/dev/null) | norm knip.json
(cd "$t" && knip --reporter json --no-progress --include files --exclude files 2>/dev/null) | norm knip_clean.json
# vitest 5's json reporter writes .vitest/json/output.json and only prints a
# notice on stdout; record both the notice and the report.
(cd "$t" && vitest run --reporter=json vitest/ 2>/dev/null) | norm vitest_stdout_notice.txt
(cd "$t" && cat .vitest/json/output.json) | norm vitest_fail.json
(cd "$t" && rm -rf .vitest && vitest run --reporter=json --outputFile=vitest.json vitest/sum.test.mjs >/dev/null 2>&1; cat vitest.json) | norm vitest_pass.json
(cd "$t" && jest --json --rootDir jest 2>/dev/null) | norm jest_fail.json
# eslint through `npm run`, which prints a banner before the JSON.
(cd "$t" && npm pkg set scripts.lint="eslint -f json src/big.ts" >/dev/null && npm run lint 2>/dev/null) | norm eslint_npm_banner.txt
echo "recorded into $out"
