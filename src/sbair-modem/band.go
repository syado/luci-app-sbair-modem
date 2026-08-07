// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912

package main

import (
	"fmt"
	"strconv"
	"strings"
)

// バンドと、アンテナごとの電波品質。
//
// **ここで使う AT を別のものに置き換えないこと。** 一般的なバンド設定ツールが
// 使う口はこの機体では通らず、別名のコマンドだけが答える。どれが効くか、
// なぜこの名前なのかは sbair6-rs の docs/AT.md「バンド」。
type BandInfo struct {
	// いま掴んでいるバンド。
	ServingRAT   string `json:"serving_rat,omitempty"`
	ServingBands []int  `json:"serving_bands,omitempty"`
	ECBDINFO     string `json:"ecbdinfo,omitempty"`

	// 搬送波ごとの内訳 (CA を含む)。`AT+EDMFAPP=6,3` から。
	Carriers []Carrier `json:"carriers,omitempty"`
	NRActive bool      `json:"nr_active,omitempty"`
	DMFAPP   string    `json:"dmfapp,omitempty"`

	// 対応バンドと、そのうち有効にされているもの。
	LTESupported []int  `json:"lte_supported,omitempty"`
	LTEEnabled   []int  `json:"lte_enabled,omitempty"`
	NRSupported  []int  `json:"nr_supported,omitempty"`
	NREnabled    []int  `json:"nr_enabled,omitempty"`
	EPBSEH       string `json:"epbseh,omitempty"`

	// 直前の設定。変更したときだけ入る(画面の「元に戻す」用)。
	PrevLTE []int `json:"prev_lte,omitempty"`
	PrevNR  []int `json:"prev_nr,omitempty"`

	// アンテナごとの測定値。+CESQ より細かい。
	LTERSRP []int `json:"lte_rsrp,omitempty"`
	LTESINR []int `json:"lte_sinr,omitempty"`
	NRRSRP  []int `json:"nr_rsrp,omitempty"`
	NRSINR  []int `json:"nr_sinr,omitempty"`
}

// ecbdRAT maps the first field of +ECBDINFO.
//
// **4 = LTE だけが実測。** ほかの値は当てずっぽうを出さず生のまま見せる。
var ecbdRAT = map[string]string{"4": "LTE"}

// Carrier is one serving LTE carrier.
type Carrier struct {
	Role   string `json:"role"` // PCC / SCC n
	Band   int    `json:"band"`
	EARFCN int    `json:"earfcn,omitempty"`
	PCI    int    `json:"pci,omitempty"`
}

// parseDMFCA reads the serving-cell list out of AT+EDMFAPP=6,3.
//
// **`+ECBDINFO?` のバンド欄は空で返ることがあり、そうなると戻らない。**
// こちらは常に埋まり、CA の内訳も出るので、接続中のバンドはこちらを使う。
//
//	下り搬送波の数, [通し番号,?,バンド,5,帯域幅,帯域幅,PCI,EARFCN] × 数,
//	上り搬送波の数, [通し番号,?,バンド,5,PCI,EARFCN] × 数, NR の数, NR の数
//
// **役割 (PCC/SCC) は欄の値ではなく順番で決める** — 先頭が PCC。値にも
// それらしい欄があるが未確認。**NR の中身も未確認なので解かない** — 数が
// 0 でないことだけ見る。欄の並びを実測で確定した手順は
// sbair6-rs の docs/AT.md「接続中のバンドと CA」。
func parseDMFCA(v string) (cells []Carrier, nrActive bool) {
	f := splitAT(v)
	num := func(i int) (int, bool) {
		if i >= len(f) {
			return 0, false
		}
		n, err := strconv.Atoi(strings.TrimSpace(f[i]))
		return n, err == nil
	}
	// f[0],f[1] は "6","3"。
	i := 2
	nDL, ok := num(i)
	if !ok || nDL < 0 || nDL > 8 {
		return nil, false
	}
	i++
	for c := 0; c < nDL; c++ {
		band, okB := num(i + 2)
		if !okB {
			return nil, false
		}
		cell := Carrier{Band: band}
		cell.PCI, _ = num(i + 6)
		cell.EARFCN, _ = num(i + 7)
		if c == 0 {
			cell.Role = "PCC"
		} else {
			cell.Role = fmt.Sprintf("SCC %d", c)
		}
		cells = append(cells, cell)
		i += 8
	}
	// 上りは読み飛ばす。下りと同じセルなので新しい情報が無い。
	nUL, ok := num(i)
	if !ok {
		return cells, false
	}
	i += 1 + nUL*6
	if n, ok := num(i); ok && n > 0 {
		nrActive = true
	}
	return cells, nrActive
}

// bandMask decodes one EPBSEH band mask.
//
// ⚠ **32 ビット語ごとのリトルエンディアン。** 8 桁ずつ切って、
// **先頭の語を下位 32 ビットに置く**。bit0 = バンド 1。
//
// 1 個の大きな 16 進数として素直に読むと**まったく違うバンドが出る**。
// 見分け方は「接続中のバンドが有効側に入らない読み方は間違い」。
// `band_test.go` がその検査になっている。
//
// 長さが 8 の倍数でないものは詰め方を決められないので**捨てる**。
// どちらに詰めるか当てると、静かに別のバンドを表示することになる。
func bandMask(h string) []int {
	h = strings.TrimSpace(strings.Trim(h, "\""))
	if h == "" || len(h)%8 != 0 {
		return nil
	}
	var bands []int
	for w := 0; w*8 < len(h); w++ {
		v, err := strconv.ParseUint(h[w*8:w*8+8], 16, 32)
		if err != nil {
			return nil
		}
		for b := 0; b < 32; b++ {
			if v>>uint(b)&1 == 1 {
				bands = append(bands, w*32+b+1)
			}
		}
	}
	return bands
}

// qnwcfgInts reads the numeric tail of a +QNWCFG reply.
//
//	+QNWCFG: "lte_rsrp",-84,-81
//
// ⚠ **nr5g_* は 5G に載っていないとき全部 0 を返す。** 「0 dBm」ではなく
// 欠測なので、**全部 0 のときは値として扱わない**。
func qnwcfgInts(lines []string, key string) []int {
	v, ok := Last(lines, "+QNWCFG:")
	if !ok {
		return nil
	}
	f := splitAT(v)
	if len(f) < 2 || f[0] != key {
		return nil
	}
	var out []int
	nonZero := false
	for _, s := range f[1:] {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return nil
		}
		if n != 0 {
			nonZero = true
		}
		out = append(out, n)
	}
	if !nonZero {
		return nil
	}
	return out
}

// collectBand fills in the band panel. Every probe is best-effort, like the
// rest of collectOverview: a modem that will not answer one of these still has
// to produce a usable screen.
func collectBand(o *Overview, ask func(string) []string) {
	b := &BandInfo{}

	// **接続中のバンドはこちらが本命。** `+ECBDINFO?` はバンド欄が空で返って
	// そのまま戻らないことがあるが、これは常に埋まり、CA の内訳まで出る。
	if v, ok := Last(ask("AT+EDMFAPP=6,3"), "+EDMFAPP:"); ok {
		b.DMFAPP = v
		b.Carriers, b.NRActive = parseDMFCA(v)
		seen := map[int]bool{}
		for _, c := range b.Carriers {
			if c.Band > 0 && !seen[c.Band] {
				seen[c.Band] = true
				b.ServingBands = append(b.ServingBands, c.Band)
			}
		}
		if len(b.ServingBands) > 0 {
			b.ServingRAT = "LTE"
		}
	}

	// +ECBDINFO: <rat>,<count>,<band>[,<band>...]
	//
	// 実測は `4,1,41,,,,` — 末尾の空欄は CA の副バンド枠と思われるが**未確認**。
	// 空欄は読み飛ばし、埋まっている数字だけを採る。
	if v, ok := Last(ask("AT+ECBDINFO?"), "+ECBDINFO:"); ok {
		b.ECBDINFO = v
		f := splitAT(v)
		// **上で取れていれば上書きしない。**
		if len(b.ServingBands) == 0 {
			if len(f) > 0 {
				if s, hit := ecbdRAT[f[0]]; hit {
					b.ServingRAT = s
				} else {
					b.ServingRAT = "RAT=" + f[0]
				}
			}
			for _, s := range f[min(2, len(f)):] {
				if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n > 0 {
					b.ServingBands = append(b.ServingBands, n)
				}
			}
		}
	}

	// +EPBSEH: "<gsm>","<umts>","<lte>","<nr>"[,"<...>"]
	//
	// **`?` が現在の設定、`=?` が対応能力。** 逆ではない。
	masks := func(cmd string) (lte, nr []int, raw string) {
		v, ok := Last(ask(cmd), "+EPBSEH:")
		if !ok {
			return nil, nil, ""
		}
		f := splitAT(v)
		if len(f) < 4 {
			return nil, nil, v
		}
		return bandMask(f[2]), bandMask(f[3]), v
	}
	b.LTEEnabled, b.NREnabled, b.EPBSEH = masks("AT+EPBSEH?")
	b.LTESupported, b.NRSupported, _ = masks("AT+EPBSEH=?")

	// 直前の設定は uci にある。**変更後と同じなら出さない** —
	// 「元に戻す」が何も変えないボタンになる。
	if p := bandPrevious(); len(p) == 4 {
		lte, nr := bandMask(p[2]), bandMask(p[3])
		if !sameInts(lte, b.LTEEnabled) || !sameInts(nr, b.NREnabled) {
			b.PrevLTE, b.PrevNR = lte, nr
		}
	}

	b.LTERSRP = qnwcfgInts(ask(`AT+QNWCFG="lte_rsrp"`), "lte_rsrp")
	b.LTESINR = qnwcfgInts(ask(`AT+QNWCFG="lte_sinr"`), "lte_sinr")
	b.NRRSRP = qnwcfgInts(ask(`AT+QNWCFG="nr5g_ssb_rsrp"`), "nr5g_ssb_rsrp")
	b.NRSINR = qnwcfgInts(ask(`AT+QNWCFG="nr5g_ssb_sinr"`), "nr5g_ssb_sinr")

	// 1 つも取れなかったなら丸ごと省く。空の表を出すより黙るほうがよい。
	if b.ECBDINFO == "" && b.DMFAPP == "" && b.EPBSEH == "" && b.LTERSRP == nil && b.LTESINR == nil {
		return
	}
	o.Band = b
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
