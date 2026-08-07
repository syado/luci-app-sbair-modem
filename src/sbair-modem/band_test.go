// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912

package main

import (
	"reflect"
	"strings"
	"testing"
)

// **語順を取り違えたときに落ちることが、このテストの目的。** ベクタは実機の
// +EPBSEH。素直に 1 個の 16 進数として読むと別のバンドが出て、しかも
// **接続中のバンド 41 が有効側から消える** — それが判別のしかた。
func TestBandMask(t *testing.T) {
	cases := []struct {
		name string
		hex  string
		want []int
	}{
		{"LTE 現在", "0000000100000300", []int{1, 41, 42}},
		{"LTE 対応", "0000008100000300", []int{1, 8, 41, 42}},
		{"NR 現在", "080000040000000000001000", []int{3, 28, 77}},
		{"NR 対応", "080000040000000000005000", []int{3, 28, 77, 79}},
		{"引用符付き", "\"0000000100000300\"", []int{1, 41, 42}},
		{"空", "", nil},
		{"8 の倍数でない", "000001", nil},
		{"16 進でない", "zzzzzzzz", nil},
	}
	for _, c := range cases {
		if got := bandMask(c.hex); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: bandMask(%q) = %v, want %v", c.name, c.hex, got, c.want)
		}
	}
}

// 接続中のバンドが有効側に含まれることを、実測の組で確かめる。
// **読み方が正しいことの根拠そのもの**なので、独立した検査として置く。
func TestServingBandIsEnabled(t *testing.T) {
	enabled := bandMask("0000000100000300")
	const serving = 41 // +ECBDINFO: 4,1,41,,,,
	for _, b := range enabled {
		if b == serving {
			return
		}
	}
	t.Fatalf("接続中のバンド %d が有効バンド %v に入っていない — 語順の読み違い", serving, enabled)
}

// **書き戻して同じ 16 進に戻ることが、書き込み側の唯一の安全確認。**
// 幅が変わると語の切れ目がずれて、別のバンドを書き込むことになる。
func TestBandMaskHexRoundTrip(t *testing.T) {
	for _, h := range []string{
		"0000000100000300", "0000008100000300",
		"080000040000000000001000", "080000040000000000005000",
		"00000002", "00000080",
	} {
		got, err := bandMaskHex(bandMask(h), len(h))
		if err != nil {
			t.Errorf("%s: %v", h, err)
			continue
		}
		if !strings.EqualFold(got, h) {
			t.Errorf("往復で変わった: %s -> %v -> %s", h, bandMask(h), got)
		}
	}
}

func TestBandMaskHexWidth(t *testing.T) {
	// LTE は 16 桁、NR は 24 桁。**幅は呼び出し側が決める。**
	if got, _ := bandMaskHex([]int{1, 41, 42}, 16); got != "0000000100000300" {
		t.Errorf("LTE = %q", got)
	}
	if got, _ := bandMaskHex([]int{3, 28, 77}, 24); got != "080000040000000000001000" {
		t.Errorf("NR = %q", got)
	}
	// 空でも幅ぶんのゼロを返す。**短い文字列を返すと語がずれる。**
	if got, _ := bandMaskHex(nil, 24); got != "000000000000000000000000" {
		t.Errorf("空 = %q", got)
	}
	// 幅に入らないバンドは黙って落とさずエラーにする。
	if _, err := bandMaskHex([]int{100}, 16); err == nil {
		t.Error("バンド 100 が 64 ビットのマスクに入ってしまった")
	}
	if _, err := bandMaskHex([]int{0}, 16); err == nil {
		t.Error("バンド 0 が通ってしまった")
	}
}

func TestParseBandList(t *testing.T) {
	cases := []struct {
		in   string
		want []int
	}{
		{"1,41,42", []int{1, 41, 42}},
		{" 42 , 1 ,41 ", []int{1, 41, 42}}, // 並べ替える
		{"1,1,41", []int{1, 41}},           // 重複を落とす
		{"", nil},
		{",,", nil},
	}
	for _, c := range cases {
		got, err := parseBandList(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q = %v, want %v", c.in, got, c.want)
		}
	}
	if _, err := parseBandList("1,abc"); err == nil {
		t.Error("数字でない入力が通ってしまった")
	}
}

func TestNotIn(t *testing.T) {
	if got := notIn([]int{1, 8, 41}, []int{1, 8, 41, 42}); got != nil {
		t.Errorf("全部対応しているのに %v", got)
	}
	if got := notIn([]int{1, 3, 41}, []int{1, 8, 41, 42}); !reflect.DeepEqual(got, []int{3}) {
		t.Errorf("= %v, want [3]", got)
	}
}

// 実機の +EDMFAPP=6,3 をそのまま使う。**3 行とも同じ瞬間に測った実測値**で、
// LTE を B41 だけに絞ると 3 番目の欄が 1 → 41、EARFCN が 100 → 41040 に動いた。
// これが「3 番目がバンド番号」の根拠なので、テストもそこを見る。
func TestParseDMFCA(t *testing.T) {
	cases := []struct {
		name   string
		line   string
		want   []Carrier
		wantNR bool
	}{
		{
			// 全バンド有効。バンド 1、CA 無し、上りも 1 本。
			name: "band 1 単独",
			line: "6,3,1,0,2,1,5,15,15,168,100,1,0,2,1,5,168,18100,0,0",
			want: []Carrier{{Role: "PCC", Band: 1, EARFCN: 100, PCI: 168}},
		},
		{
			// 上りが立っていない瞬間。末尾が短い形。
			name: "上り 0 本",
			line: "6,3,1,0,2,1,5,15,15,168,100,0,0,0",
			want: []Carrier{{Role: "PCC", Band: 1, EARFCN: 100, PCI: 168}},
		},
		{
			// B41 だけに絞ったとき。**2 波の CA。**
			name: "band 41 で CA",
			line: "6,3,2,0,2,41,5,9,9,66,41040,1,1,41,5,0,0,66,40842,1,0,2,41,5,66,41040,0,0",
			want: []Carrier{
				{Role: "PCC", Band: 41, EARFCN: 41040, PCI: 66},
				{Role: "SCC 1", Band: 41, EARFCN: 40842, PCI: 66},
			},
		},
		// 壊れた入力で落ちないこと。**黙って空を返すのが正しい** —
		// 途中まで読めた数字をバンドとして出すほうが危ない。
		{name: "短すぎる", line: "6,3", want: nil},
		{name: "数でない", line: "6,3,x,0,0", want: nil},
		{name: "件数が過大", line: "6,3,99,0", want: nil},
		{name: "件数ぶんの中身が無い", line: "6,3,2,0,2,41,5,9,9,66,41040", want: nil},
	}
	for _, c := range cases {
		got, nr := parseDMFCA(c.line)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: = %+v, want %+v", c.name, got, c.want)
		}
		if nr != c.wantNR {
			t.Errorf("%s: nrActive = %v, want %v", c.name, nr, c.wantNR)
		}
	}
}

func TestQNWCFGInts(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		key   string
		want  []int
	}{
		{"LTE RSRP", []string{`+QNWCFG: "lte_rsrp",-84,-81`}, "lte_rsrp", []int{-84, -81}},
		{"LTE SINR", []string{`+QNWCFG: "lte_sinr",23,24`}, "lte_sinr", []int{23, 24}},
		// **5G に載っていないときは全部 0。** 欠測であって値ではない。
		{"NR 圏外", []string{`+QNWCFG: "nr5g_ssb_rsrp",0,0,0,0`}, "nr5g_ssb_rsrp", nil},
		// 1 つでも非ゼロなら測定値。
		{"NR 接続中", []string{`+QNWCFG: "nr5g_ssb_rsrp",-90,0,0,0`}, "nr5g_ssb_rsrp",
			[]int{-90, 0, 0, 0}},
		// 末尾のコンマで空要素が出る形。実機の lte_ambr がこれ。
		{"末尾コンマ", []string{`+QNWCFG: "lte_ambr",65280000,65280000,`}, "lte_ambr",
			[]int{65280000, 65280000}},
		{"キー違い", []string{`+QNWCFG: "lte_sinr",23,24`}, "lte_rsrp", nil},
		{"応答なし", []string{"OK"}, "lte_rsrp", nil},
		// URC が同じプレフィクスで割り込んでも、照会の応答は後に来る。
		{"URC が先", []string{`+QNWCFG: "lte_rsrp",-70,-70`, `+QNWCFG: "lte_rsrp",-84,-81`},
			"lte_rsrp", []int{-84, -81}},
	}
	for _, c := range cases {
		if got := qnwcfgInts(c.lines, c.key); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: qnwcfgInts(%v, %q) = %v, want %v", c.name, c.lines, c.key, got, c.want)
		}
	}
}
