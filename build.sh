#!/bin/sh
# SPDX-License-Identifier: MIT
# Copyright (c) 2026 soralis0912
# build.sh — 同梱ソースから aarch64 のバイナリを作る。
#
# ⚠ **static / CGO 無し**で作る。ビルドホストが glibc でも
#    OpenWrt (musl) でそのまま動かすため。
set -eu
SELF=$(cd "$(dirname "$0")" && pwd)
OUT="$SELF/out"; mkdir -p "$OUT"

command -v go >/dev/null || {
	echo "!! go が無い。1.25+ が要る" >&2
	echo "   (OpenWrt 21.02 の golang は 1.18 で足りない)" >&2
	echo "   goenv を使うなら:" >&2
	echo "     export PATH=\$HOME/.goenv/bin:\$HOME/.goenv/shims:\$PATH" >&2
	exit 1; }

( cd "$SELF/src/sbair-modem" && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
	go build -trimpath -ldflags "-s -w" -o "$OUT/sbair-modem" . )
echo "built: out/sbair-modem  ($(wc -c < "$OUT/sbair-modem") bytes)"
