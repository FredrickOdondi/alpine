#!/bin/sh
# Fake gate tool: print a canned output file to stdout and exit with a code.
# usage: emit.sh <output-file> <exit-code> [stderr-text]
cat "$1"
[ -n "$3" ] && printf '%s\n' "$3" >&2
exit "$2"
