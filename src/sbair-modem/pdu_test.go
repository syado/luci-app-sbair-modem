// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912

package main

import (
	"encoding/hex"
	"strings"
	"testing"
	"unicode/utf16"
)

// packGSM7 is the inverse of unpackGSM7, for building test PDUs.
//
// **テスト専用。** 本番は受信しかしないので符号化は要らないが、
// UDH のある連結メッセージを手で組むのは骨が折れるうえ、手が滑ると
// 「デコーダの誤りに合わせた期待値」を書いてしまう。往復で確かめる。
func packGSM7(septets []byte, skip int) []byte {
	all := make([]byte, skip, skip+len(septets))
	all = append(all, septets...)
	out := make([]byte, (len(all)*7+7)/8)
	for i, s := range all {
		bit := i * 7
		idx, shift := bit/8, bit%8
		out[idx] |= s << shift
		if shift > 1 {
			out[idx+1] |= s >> (8 - shift)
		}
	}
	return out
}

func toSeptets(s string) []byte {
	out := []byte{}
	for _, r := range s {
		for i, g := range gsm7Basic {
			if g == r {
				out = append(out, byte(i))
				break
			}
		}
	}
	return out
}

// TestGSM7Canonical は広く知られた PDU。デコーダ全体の土台の確認。
func TestGSM7Canonical(t *testing.T) {
	const pdu = "0791448720003023240DD0E474D81C0EBB010000111011315214000BE8329BFD4697D9EC37"
	m, err := decodeDeliver(pdu)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m.Text != "hellohello" {
		t.Errorf("本文 = %q, 期待 %q", m.Text, "hellohello")
	}
	// TON=5 (0xD0) なので送信者は文字表記。
	if m.From != "diafaan" {
		t.Errorf("送信者 = %q, 期待 %q", m.From, "diafaan")
	}
}

func TestGSM7Unpack(t *testing.T) {
	// "How are you?" — 12 septets。
	data, _ := hex.DecodeString("C8F71D14969741F977FD07")
	got := gsm7ToString(unpackGSM7(data, 12))
	if got != "How are you?" {
		t.Errorf("= %q, 期待 %q", got, "How are you?")
	}
}

// TestGSM7SeptetOffset は UDH がある GSM 7bit。**ここが一番壊れやすい。**
// UDH の後ろは septet 境界まで詰め物が入るので、offset を落とすと
// 連結メッセージだけ静かに化ける。
func TestGSM7SeptetOffset(t *testing.T) {
	for _, tc := range []struct {
		name string
		udh  []byte // UDHL を含まない IE 列
		text string
	}{
		{"8bit ref (UDHL=5, 6 octets, fill=1)", []byte{0x00, 0x03, 0x2A, 0x02, 0x01}, "part one of two"},
		{"16bit ref (UDHL=6, 7 octets, fill=0)", []byte{0x08, 0x04, 0x12, 0x34, 0x02, 0x02}, "and here is two"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			udh := append([]byte{byte(len(tc.udh))}, tc.udh...)
			skip := (len(udh)*8 + 6) / 7
			body := packGSM7(toSeptets(tc.text), skip)
			ud := append(udh, body[len(udh):]...)
			udl := skip + len(toSeptets(tc.text))

			pdu := "00" + // SCA なし
				"44" + // MTI=DELIVER, UDHI=1
				"0B" + "91" + "1346610089F6" + // TP-OA
				"00" + "00" + // PID, DCS=GSM7
				"62807091221363" + // SCTS: 2026-08-07 19:22:31 +09:00
				hexByte(udl) + strings.ToUpper(hex.EncodeToString(ud))

			m, err := decodeDeliver(pdu)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if m.Text != tc.text {
				t.Errorf("本文 = %q, 期待 %q", m.Text, tc.text)
			}
			if m.Concat == nil {
				t.Fatal("連結情報が取れていない")
			}
			if m.Concat.Total != 2 {
				t.Errorf("total = %d, 期待 2", m.Concat.Total)
			}
		})
	}
}

func TestUCS2Japanese(t *testing.T) {
	const want = "テスト、こんにちは。"
	var b strings.Builder
	for _, u := range utf16.Encode([]rune(want)) {
		b.WriteString(hexByte(int(u >> 8)))
		b.WriteString(hexByte(int(u & 0xFF)))
	}
	body := b.String()
	pdu := "00" + "04" + "0B" + "91" + "1346610089F6" + "00" + "08" +
		"62807091221363" + hexByte(len(body)/2) + body

	m, err := decodeDeliver(pdu)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m.Text != want {
		t.Errorf("本文 = %q, 期待 %q", m.Text, want)
	}
	if m.From != "+31641600986" {
		t.Errorf("送信者 = %q", m.From)
	}
}

func TestSCTSTimezone(t *testing.T) {
	for _, tc := range []struct {
		hex    string
		want   string
		offset int // 秒
	}{
		{"62807091221363", "2026-08-07T19:22:31", 9 * 3600},      // +09:00 (0x63)
		{"6280709122136B", "2026-08-07T19:22:31", -9 * 3600},     // -09:00 (符号 bit)
		{"62807091221300", "2026-08-07T19:22:31", 0},             // UTC
		{"62807091221322", "2026-08-07T19:22:31", 5*3600 + 1800}, // +05:30 (22 quarters)
	} {
		b, _ := hex.DecodeString(tc.hex)
		got := decodeSCTS(b)
		if got.Format("2006-01-02T15:04:05") != tc.want {
			t.Errorf("%s → %s, 期待 %s", tc.hex, got.Format("2006-01-02T15:04:05"), tc.want)
		}
		if _, off := got.Zone(); off != tc.offset {
			t.Errorf("%s のオフセット = %d, 期待 %d", tc.hex, off, tc.offset)
		}
	}
}

func TestDCSAlphabet(t *testing.T) {
	for _, tc := range []struct {
		dcs  byte
		want int
	}{
		{0x00, alphaGSM7}, {0x04, alpha8Bit}, {0x08, alphaUCS2},
		{0x10, alphaGSM7}, {0x18, alphaUCS2}, // message class 付き
		{0xF0, alphaGSM7}, {0xF4, alpha8Bit}, // data coding グループ
		{0xE0, alphaUCS2}, // message waiting, UCS2
	} {
		if got := dcsAlphabet(tc.dcs); got != tc.want {
			t.Errorf("DCS %#02x → %d, 期待 %d", tc.dcs, got, tc.want)
		}
	}
}

func TestReassemble(t *testing.T) {
	parts := []smsPart{
		{Index: 2, PDU: &smsPDU{From: "+8190", Text: "world", Concat: &concatInfo{Ref: 7, Total: 2, Seq: 2}}},
		{Index: 1, Unread: true, PDU: &smsPDU{From: "+8190", Text: "hello ", Concat: &concatInfo{Ref: 7, Total: 2, Seq: 1}}},
		{Index: 5, PDU: &smsPDU{From: "+8180", Text: "単発"}},
		{Index: 9, PDU: &smsPDU{From: "+8170", Text: "half", Concat: &concatInfo{Ref: 3, Total: 3, Seq: 2}}},
	}
	got := reassemble(parts)
	if len(got) != 3 {
		t.Fatalf("%d 通, 期待 3", len(got))
	}
	byText := map[string]smsMessage{}
	for _, m := range got {
		byText[m.Text] = m
	}
	if m, ok := byText["hello world"]; !ok {
		t.Error("連結が繋がっていない")
	} else if !m.Unread {
		t.Error("どれか 1 つでも未読なら未読として扱うこと")
	}
	if m, ok := byText["half"]; !ok {
		t.Error("欠けた連結が消えている")
	} else if len(m.Missing) != 2 {
		t.Errorf("欠番 = %v, 期待 2 件", m.Missing)
	}
}

func hexByte(v int) string {
	return strings.ToUpper(hex.EncodeToString([]byte{byte(v)}))
}
