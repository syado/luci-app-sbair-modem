// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912

package main

import "testing"

// 実機に SMS が 1 通も届かないので、AT+CMGL の応答は組み立てて確かめる。
// **未検証なのは「この機体の +CMGL がこの形で返るか」だけ**にしておく。
const canonPDU = "0791448720003023240DD0E474D81C0EBB010000111011315214000BE8329BFD4697D9EC37"

func TestParseCMGL(t *testing.T) {
	lines := []string{
		`+CMGL: 1,0,,25`,
		canonPDU,
		`+CNVRM: 0`, // 割り込む URC。PDU と取り違えないこと
		`+CMGL: 2,1,,25`,
		canonPDU,
	}
	parts, bad := parseCMGL(lines)
	if len(bad) != 0 {
		t.Fatalf("誤り = %v, 期待 なし", bad)
	}
	if len(parts) != 2 {
		t.Fatalf("%d 件, 期待 2", len(parts))
	}
	if parts[0].Index != 1 || !parts[0].Unread {
		t.Errorf("1 件目 = index %d unread %v, 期待 1 / true", parts[0].Index, parts[0].Unread)
	}
	if parts[1].Index != 2 || parts[1].Unread {
		t.Errorf("2 件目 = index %d unread %v, 期待 2 / false", parts[1].Index, parts[1].Unread)
	}
	if parts[0].PDU.Text != "hellohello" {
		t.Errorf("本文 = %q", parts[0].PDU.Text)
	}
}

// 本体の無いヘッダを黙って捨てない。捨てると「届いているのに出ない」に化ける。
func TestParseCMGLMissingBody(t *testing.T) {
	parts, bad := parseCMGL([]string{
		`+CMGL: 3,0,,25`,
		`+CMGL: 4,0,,25`,
		canonPDU,
	})
	if len(parts) != 1 || parts[0].Index != 4 {
		t.Fatalf("parts = %+v", parts)
	}
	if len(bad) != 1 {
		t.Fatalf("誤り = %v, 期待 1 件", bad)
	}
}

// 壊れた PDU が 1 つあっても、残りは出す。
func TestParseCMGLBadPDU(t *testing.T) {
	parts, bad := parseCMGL([]string{
		`+CMGL: 1,0,,25`,
		"00040B911346610089F60008", // SCTS の手前で切れている
		`+CMGL: 2,0,,25`,
		canonPDU,
	})
	if len(parts) != 1 || parts[0].Index != 2 {
		t.Fatalf("parts = %+v", parts)
	}
	if len(bad) != 1 {
		t.Errorf("誤り = %v, 期待 1 件", bad)
	}
}

func TestIsHexLine(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{canonPDU, true},
		{"0791448720003023", true},
		{"0791448720003023a", false}, // 奇数
		{"+CMGL: 1,0,,25", false},
		{"OK", false},
		{"07914487", true},
		{"079144", false}, // 短すぎる
	} {
		if got := isHexLine(tc.in); got != tc.want {
			t.Errorf("isHexLine(%q) = %v, 期待 %v", tc.in, got, tc.want)
		}
	}
}
