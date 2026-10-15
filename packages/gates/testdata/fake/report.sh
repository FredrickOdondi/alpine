#!/bin/sh
# Fake gate tool that writes its report to a file (like jscpd --output).
# usage: report.sh <canned-report> <dest-relative-to-cwd> <exit-code>
mkdir -p "$(dirname "$2")"
cp "$1" "$2"
exit "$3"
