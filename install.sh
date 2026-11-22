#!/usr/bin/env sh
set -e
DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$DIR/code/webfast"
go mod download
go build -o "$DIR/billing-system.exe" .
echo "构建完成：$DIR/billing-system.exe"
