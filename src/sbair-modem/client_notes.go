// SPDX-License-Identifier: MIT
// Copyright (c) 2026 syado

package main

import (
	"fmt"
	"strings"
)

// MACアドレスごとの自由メモ。/etc/config/sbair に保存する(APNエントリと同じ
// 設定ファイルを共有し、セクション型を "client_note" にして分ける)。

func macSectionName(mac string) string {
	return "m" + notUCIName.ReplaceAllString(strings.ToLower(mac), "")
}

// clientNotes は mac → メモ の対応表を返す。
func clientNotes() map[string]string {
	raw, err := uci("show", apnConfig)
	if err != nil {
		return map[string]string{}
	}

	type entry struct{ mac, note string }
	sections := map[string]*entry{}
	for _, line := range strings.Split(raw, "\n") {
		const prefix = apnConfig + "."
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		rest := line[len(prefix):]
		dot := strings.IndexByte(rest, '.')
		if dot < 0 {
			continue
		}
		section, field := rest[:dot], rest[dot+1:]
		if !strings.HasPrefix(section, "m") {
			continue
		}
		eq := strings.IndexByte(field, '=')
		if eq < 0 {
			continue
		}
		key, val := field[:eq], strings.Trim(field[eq+1:], "'")
		if sections[section] == nil {
			sections[section] = &entry{}
		}
		switch key {
		case "mac":
			sections[section].mac = val
		case "note":
			sections[section].note = val
		}
	}

	out := map[string]string{}
	for _, e := range sections {
		if e.mac != "" && e.note != "" {
			out[e.mac] = e.note
		}
	}
	return out
}

func clientNoteSet(mac, note string) map[string]any {
	mac = strings.ToLower(strings.TrimSpace(mac))
	if mac == "" {
		return map[string]any{"error": "mac is required"}
	}
	if err := ensureConfig(); err != nil {
		return map[string]any{"error": fmt.Sprintf("/etc/config/%s を作れません: %v", apnConfig, err)}
	}
	sec := apnConfig + "." + macSectionName(mac)
	if note == "" {
		_, _ = uci("delete", sec)
		_, _ = uci("commit", apnConfig)
		return map[string]any{"result": "cleared"}
	}
	if _, err := uci("set", sec+"=client_note"); err != nil {
		return map[string]any{"error": fmt.Sprintf("uci set: %v", err)}
	}
	if _, err := uci("set", sec+".mac="+mac); err != nil {
		return map[string]any{"error": fmt.Sprintf("uci set: %v", err)}
	}
	if _, err := uci("set", sec+".note="+note); err != nil {
		return map[string]any{"error": fmt.Sprintf("uci set: %v", err)}
	}
	if _, err := uci("commit", apnConfig); err != nil {
		return map[string]any{"error": fmt.Sprintf("uci commit: %v", err)}
	}
	return map[string]any{"result": "ok"}
}
