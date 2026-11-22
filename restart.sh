#!/usr/bin/env sh
set -e
DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$DIR/code/webfast"
go run .
