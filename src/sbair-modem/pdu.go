// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912

package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf16"
)

// SMS の PDU (TS 23.040 / 23.038) を解く。受信 (SMS-DELIVER) のみ。
//
// **テキストモード (AT+CMGF=1) は使わない。** 楽に見えるが、
// 連結メッセージの UDH が落ちて分割が繋げられなくなり、タイムゾーンも
// 落ちる。日本語は UCS2 なのでどのみち自前で解く必要がある。

// concatInfo は連結 SMS の UDH (IEI 0x00 / 0x08) から取れる情報。
type concatInfo struct {
	Ref   int
	Total int
	Seq   int
}

// smsPDU は 1 通ぶんの PDU。連結メッセージではこれが「破片」になる。
type smsPDU struct {
	From   string
	Time   time.Time
	Text   string
	DCS    byte
	Concat *concatInfo
}

const (
	alphaGSM7 = iota
	alpha8Bit
	alphaUCS2
)

// dcsAlphabet maps TP-DCS to the alphabet (TS 23.038 §4).
//
// **0xF0-0xFF (data coding / message class) を忘れないこと。** 事業者の
// 通知メッセージがこの符号で来る。ここを既定の 0000 グループと同じに
// 扱うと、bit3-2 の読み替えを間違えて本文が化ける。
func dcsAlphabet(dcs byte) int {
	switch {
	case dcs&0xC0 == 0x00: // 00xxxxxx: general / auto-delete
		switch (dcs >> 2) & 0x03 {
		case 1:
			return alpha8Bit
		case 2:
			return alphaUCS2
		}
		return alphaGSM7
	case dcs&0xF0 == 0xF0: // 1111xxxx: data coding / message class
		if dcs&0x04 != 0 {
			return alpha8Bit
		}
		return alphaGSM7
	case dcs&0xF0 == 0xE0: // 1110xxxx: message waiting, UCS2
		return alphaUCS2
	}
	return alphaGSM7
}

// gsm7Basic is the GSM 03.38 default alphabet. Index = septet value.
var gsm7Basic = []rune(
	"@£$¥èéùìòÇ\nØø\rÅå" +
		"Δ_ΦΓΛΩΠΨΣΘΞ\x1bÆæßÉ" +
		" !\"#¤%&'()*+,-./" +
		"0123456789:;<=>?" +
		"¡ABCDEFGHIJKLMNO" +
		"PQRSTUVWXYZÄÖÑÜ§" +
		"¿abcdefghijklmno" +
		"pqrstuvwxyzäöñüà")

// gsm7Ext is the extension table, reached through the 0x1B escape.
var gsm7Ext = map[byte]rune{
	0x0A: '\f', 0x14: '^', 0x28: '{', 0x29: '}', 0x2F: '\\',
	0x3C: '[', 0x3D: '~', 0x3E: ']', 0x40: '|', 0x65: '€',
}

// unpackGSM7 turns packed septets into one byte per septet.
//
// **`septets` は詰め物も含めた総数を渡すこと。** UDH があるとデータは
// septet 境界まで送られるので、呼び出し側はここで全部ほどいてから
// 先頭の UDH ぶんを捨てる。境界計算をここでやると offset の扱いが
// 2 箇所に分かれて必ずずれる。
func unpackGSM7(data []byte, septets int) []byte {
	// **オクテットが運べる数を超える septet は詰め物。** UDL がずれている
	// PDU は実在し、素直に読むと末尾の埋めビットが 0 = '@' として 1 文字
	// 増える。実機の 1 通目でこれに気付くのは遅い。
	if max := len(data) * 8 / 7; septets > max {
		septets = max
	}
	out := make([]byte, 0, septets)
	for i := 0; i < septets; i++ {
		bit := i * 7
		idx := bit / 8
		shift := bit % 8
		if idx >= len(data) {
			break
		}
		v := int(data[idx]) >> shift
		if shift > 1 && idx+1 < len(data) {
			v |= int(data[idx+1]) << (8 - shift)
		}
		out = append(out, byte(v&0x7F))
	}
	return out
}

func gsm7ToString(septets []byte) string {
	var b strings.Builder
	for i := 0; i < len(septets); i++ {
		c := septets[i]
		if c == 0x1B {
			i++
			if i >= len(septets) {
				break
			}
			if r, ok := gsm7Ext[septets[i]]; ok {
				b.WriteRune(r)
				continue
			}
			// TS 23.038: 未定義の拡張は空白として表示する。
			b.WriteRune(' ')
			continue
		}
		if int(c) < len(gsm7Basic) {
			b.WriteRune(gsm7Basic[c])
		}
	}
	return b.String()
}

// decodeAddress reads a TP-Address field.
//
// digits は**ニブル数**(TP-OA の長さフィールドの単位)。文字表記の
// アドレス (TON=5) は GSM 7bit が詰まっているので分けて扱う — 事業者の
// 通知が "au" のような名前で届く。
func decodeAddress(toa byte, body []byte, digits int) string {
	if (toa>>4)&0x07 == 5 { // alphanumeric
		return gsm7ToString(unpackGSM7(body, digits*4/7))
	}
	var b strings.Builder
	if toa&0x70 == 0x10 { // TON = 1: international
		b.WriteByte('+')
	}
	for i := 0; i < digits; i++ {
		o := body[i/2]
		n := o & 0x0F
		if i%2 == 1 {
			n = o >> 4
		}
		if n > 9 {
			break // 0xF padding
		}
		b.WriteByte('0' + n)
	}
	return b.String()
}

// decodeSCTS reads the 7-octet service centre timestamp.
//
// **タイムゾーンの符号は 1 桁目の bit3。** 十の位のニブルに紛れているので、
// 単純な BCD 反転だけで読むと日本 (+9h = 36 quarter hours, 0x63) は通っても
// 負のオフセットで壊れる。
func decodeSCTS(b []byte) time.Time {
	d := func(o byte) int { return int(o&0x0F)*10 + int(o>>4) }
	yy := d(b[0])
	year := 2000 + yy
	if yy >= 70 {
		year = 1900 + yy
	}
	tzTens, tzUnits := int(b[6]&0x07), int(b[6]>>4)
	quarter := tzTens*10 + tzUnits
	if b[6]&0x08 != 0 {
		quarter = -quarter
	}
	return time.Date(year, time.Month(d(b[1])), d(b[2]),
		d(b[3]), d(b[4]), d(b[5]), 0,
		time.FixedZone("", quarter*15*60))
}

// decodeDeliver parses one SMS-DELIVER PDU as returned by AT+CMGL in PDU mode.
func decodeDeliver(pduHex string) (*smsPDU, error) {
	raw, err := hex.DecodeString(strings.TrimSpace(pduHex))
	if err != nil {
		return nil, fmt.Errorf("PDU が 16 進ではありません: %w", err)
	}
	p := 0
	need := func(n int) error {
		if p+n > len(raw) {
			return errors.New("PDU が途中で切れています")
		}
		return nil
	}

	// SCA。長さは「TOA + 番号」のオクテット数。0 なら中身は無い。
	if err := need(1); err != nil {
		return nil, err
	}
	scaLen := int(raw[p])
	p++
	if err := need(scaLen); err != nil {
		return nil, err
	}
	p += scaLen

	if err := need(1); err != nil {
		return nil, err
	}
	pduType := raw[p]
	p++
	if pduType&0x03 != 0x00 {
		return nil, fmt.Errorf("SMS-DELIVER ではありません (MTI=%d)", pduType&0x03)
	}
	hasUDH := pduType&0x40 != 0

	// TP-OA。長さフィールドはニブル数。
	if err := need(1); err != nil {
		return nil, err
	}
	oaDigits := int(raw[p])
	p++
	if err := need(1); err != nil {
		return nil, err
	}
	oaTOA := raw[p]
	p++
	oaOctets := (oaDigits + 1) / 2
	if err := need(oaOctets); err != nil {
		return nil, err
	}
	from := decodeAddress(oaTOA, raw[p:p+oaOctets], oaDigits)
	p += oaOctets

	if err := need(2 + 7 + 1); err != nil {
		return nil, err
	}
	p++ // TP-PID
	dcs := raw[p]
	p++
	when := decodeSCTS(raw[p : p+7])
	p += 7
	udl := int(raw[p])
	p++
	ud := raw[p:]

	var concat *concatInfo
	udhOctets := 0
	if hasUDH {
		if len(ud) < 1 {
			return nil, errors.New("UDH が入りません")
		}
		udhl := int(ud[0])
		udhOctets = 1 + udhl
		if udhOctets > len(ud) {
			return nil, errors.New("UDH が途中で切れています")
		}
		concat = parseUDH(ud[1:udhOctets])
	}

	var text string
	switch dcsAlphabet(dcs) {
	case alphaUCS2:
		body := ud[udhOctets:]
		// UDL はオクテット数。UDH ぶんを引いた残りが本文。
		if n := udl - udhOctets; n >= 0 && n <= len(body) {
			body = body[:n]
		}
		text = ucs2ToString(body)
	case alpha8Bit:
		body := ud[udhOctets:]
		if n := udl - udhOctets; n >= 0 && n <= len(body) {
			body = body[:n]
		}
		text = string(body)
	default:
		// **GSM 7bit だけ UDL が septet 数。** UDH は septet 境界まで
		// 詰められるので、全部ほどいてから頭を捨てる。
		skip := (udhOctets*8 + 6) / 7
		all := unpackGSM7(ud, udl)
		if skip > len(all) {
			skip = len(all)
		}
		text = gsm7ToString(all[skip:])
	}

	return &smsPDU{From: from, Time: when, Text: text, DCS: dcs, Concat: concat}, nil
}

// parseUDH walks the information elements and returns the concatenation one.
func parseUDH(ies []byte) *concatInfo {
	for i := 0; i+1 < len(ies); {
		iei, iedl := ies[i], int(ies[i+1])
		if i+2+iedl > len(ies) {
			return nil
		}
		v := ies[i+2 : i+2+iedl]
		switch {
		case iei == 0x00 && iedl == 3:
			return &concatInfo{Ref: int(v[0]), Total: int(v[1]), Seq: int(v[2])}
		case iei == 0x08 && iedl == 4:
			return &concatInfo{Ref: int(v[0])<<8 | int(v[1]), Total: int(v[2]), Seq: int(v[3])}
		}
		i += 2 + iedl
	}
	return nil
}

// ucs2ToString decodes UTF-16BE, which is what Japanese SMS arrives as.
func ucs2ToString(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
	}
	return string(utf16.Decode(u))
}
