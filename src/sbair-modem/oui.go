// SPDX-License-Identifier: MIT
// Copyright (c) 2026 syado

package main

import (
	_ "embed"
	"strconv"
	"strings"
	"sync"
)

// IEEE公開のOUI(組織固有識別子)データベース。MACアドレス先頭3バイトから
// メーカー名を引くためだけに使う。取得元: https://standards-oui.ieee.org/oui/oui.csv
// (2026-08-09時点、約4万件)。 data/oui.tsv は "AABBCC\tベンダー名" の2列。
//
//go:embed data/oui.tsv
var ouiData string

var (
	ouiOnce sync.Once
	ouiMap  map[string]string
)

func loadOUI() {
	ouiMap = make(map[string]string, 40000)
	for _, line := range strings.Split(ouiData, "\n") {
		line = strings.TrimRight(line, "\r")
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			continue
		}
		ouiMap[line[:tab]] = line[tab+1:]
	}
}

// macVendor は "aa:bb:cc:dd:ee:ff" 形式のMACからベンダー名を引く。
//
// **ローカル管理アドレス(第1オクテットの下位2bit目が1)は対象外。**
// iOSのプライベートWi-Fiアドレスのようにランダム生成されたMACはOUIに
// 意味が無く、たまたま一致した実在ベンダー名を出すと誤解を招くため。
func macVendor(mac string) string {
	ouiOnce.Do(loadOUI)
	clean := strings.ToUpper(strings.ReplaceAll(mac, ":", ""))
	if len(clean) < 6 {
		return ""
	}
	first, err := strconv.ParseUint(clean[0:2], 16, 8)
	if err != nil {
		return ""
	}
	if first&0x02 != 0 {
		return "" // locally administered = ランダムMAC
	}
	return ouiMap[clean[:6]]
}
