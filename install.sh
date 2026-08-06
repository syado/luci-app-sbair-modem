#!/bin/sh
# SPDX-License-Identifier: MIT
# Copyright (c) 2026 soralis0912
# install.sh — root/ と htdocs/ を配置し、out/sbair-modem を入れる。
#
#   ./install.sh <展開済み rootfs ツリー>     イメージに焼き込む
#   ./install.sh /                            動いている実機に直接
#
# ⚠ **`install(1)` を使わない。** この機体の busybox に applet が無く、
#    実機で `install: not found` になる。mkdir/cp/chmod だけで組む。
#
# 実機に直接入れたときだけ rpcd を再起動する。**ubus のオブジェクトは
# rpcd の起動時にしか列挙されない**ので、これを飛ばすと画面が
# "Object not found" になる。
set -eu
SELF=$(cd "$(dirname "$0")" && pwd)
ROOT="${1:-}"

[ -n "$ROOT" ] || { echo "使い方: $0 <rootfs ツリー | />" >&2; exit 2; }
[ -d "$ROOT" ] || { echo "!! $ROOT が無い" >&2; exit 1; }
[ -f "$SELF/out/sbair-modem" ] || { echo "!! out/sbair-modem が無い。先に ./build.sh" >&2; exit 1; }

mkdir -p "$ROOT/usr/bin" \
         "$ROOT/usr/libexec/rpcd" \
         "$ROOT/usr/share/rpcd/acl.d" \
         "$ROOT/usr/share/luci/menu.d" \
         "$ROOT/www/luci-static/resources/view/sbair" \
         "$ROOT/www/luci-static/resources/tools" \
         "$ROOT/www/luci-static/resources/protocol" \
         "$ROOT/etc/init.d"

put() {   # put <src> <dst> <mode>
	cp "$1" "$2"
	chmod "$3" "$2"
}

put "$SELF/out/sbair-modem"             "$ROOT/usr/bin/sbair-modem"          0755
put "$SELF/root/usr/libexec/rpcd/sbair" "$ROOT/usr/libexec/rpcd/sbair"       0755
put "$SELF/root/usr/share/rpcd/acl.d/luci-app-sbair-modem.json" \
    "$ROOT/usr/share/rpcd/acl.d/luci-app-sbair-modem.json"                   0644
put "$SELF/root/usr/share/luci/menu.d/luci-app-sbair-modem.json" \
    "$ROOT/usr/share/luci/menu.d/luci-app-sbair-modem.json"                  0644
put "$SELF/htdocs/luci-static/resources/tools/sbair.js" \
    "$ROOT/www/luci-static/resources/tools/sbair.js"                         0644
for v in signal sim device; do
	put "$SELF/htdocs/luci-static/resources/view/sbair/$v.js" \
	    "$ROOT/www/luci-static/resources/view/sbair/$v.js"                   0644
done

# netifd の ql_datacall proto を LuCI の Interfaces から触れるようにする。
# **ベンダは proto スクリプトだけ入れて LuCI 側の JS を入れていない。**
put "$SELF/htdocs/luci-static/resources/protocol/ql_datacall.js" \
    "$ROOT/www/luci-static/resources/protocol/ql_datacall.js"                0644
put "$SELF/root/etc/init.d/sbair-apn"    "$ROOT/etc/init.d/sbair-apn"          0755

echo "配置した: $ROOT"

if [ "$ROOT" = "/" ]; then
	# menu.d を読み直させる。消しておけば LuCI が作り直す。
	rm -f /tmp/luci-indexcache 2>/dev/null || true
	rm -rf /tmp/luci-modulecache 2>/dev/null || true
	/etc/init.d/rpcd restart 2>/dev/null || true
	/etc/init.d/sbair-apn enable 2>/dev/null || true
	echo "rpcd を再起動し、LuCI のキャッシュを消した"
	echo "確認: ubus call sbair overview"
fi
