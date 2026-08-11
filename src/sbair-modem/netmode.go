// SPDX-License-Identifier: MIT
// Copyright (c) 2026 syado

package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// SIMルータ / 光回線AP化(ダンブAP)の切替。
//
// ロジックは `recovery/files/usr/sbin/sbair-netmode`(実機に導入済みのシェル
// スクリプト)にすでにあるので、ここでは**それをそのまま呼び出すだけ**にする。
// UCI操作をGo側とシェル側の二箇所に持つと食い違いが起きるため。
func netmodeStatus() map[string]any {
	out, err := exec.Command("sbair-netmode", "show").CombinedOutput()
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("sbair-netmode show: %v: %s", err, strings.TrimSpace(string(out)))}
	}

	fields := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		fields[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}

	mode := "sim"
	if fields["dhcp.lan.ignore"] == "1" {
		mode = "ap"
	}
	return map[string]any{"mode": mode, "detail": fields}
}

func netmodeSet(mode string) map[string]any {
	switch mode {
	case "sim", "ap":
	default:
		return map[string]any{"error": fmt.Sprintf("unknown mode %q", mode)}
	}
	out, err := exec.Command("sbair-netmode", mode).CombinedOutput()
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("sbair-netmode %s: %v: %s", mode, err, strings.TrimSpace(string(out)))}
	}
	return map[string]any{"result": "ok", "mode": mode, "output": strings.TrimSpace(string(out))}
}
