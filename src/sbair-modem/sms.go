// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// messageHash identifies a message for de-duplication on re-import.
//
// **生の PDU を使う。** 送信者・時刻・本文の組では足りない — 一括送信は
// 同じ秒に同じ本文を複数投げてくるので、それらが 1 通に潰れる。実際に
// 届いた 3 通は SCTS の秒だけが違っていて、たまたま助かっていた。
func messageHash(raw []string) string {
	h := sha256.Sum256([]byte(strings.Join(raw, "|")))
	return hex.EncodeToString(h[:16])
}

// 受信 SMS の取得。読むだけで、消しも送りもしない。

// smsPart は AT+CMGL の 1 エントリ。連結メッセージではこれが破片。
type smsPart struct {
	Index  int
	Unread bool
	Raw    string // 生の PDU。**同一性の判定に使う** (→ smsMessage.Hash)
	PDU    *smsPDU
}

// smsMessage は画面に出す 1 通。連結は繋いだあとの姿。
type smsMessage struct {
	Indexes []int  `json:"indexes"`
	From    string `json:"from"`
	Time    string `json:"time,omitempty"`
	Text    string `json:"text"`
	Unread  bool   `json:"unread"`
	Parts   int    `json:"parts,omitempty"`   // 連結の総数。単発なら 0
	Missing []int  `json:"missing,omitempty"` // 届いていない破片の番号
	Hash    string `json:"hash,omitempty"`    // 取り込みの重複判定に使う

	when time.Time
	raw  []string // 破片ごとの生 PDU
}

// smsList reads every stored message.
//
// ⚠ **AT+CMGL は未読を既読に変える** (TS 27.007)。`AT+CMGL=?` はモード引数を
// 申告しないので避けようがない。応答の <stat> は変更**前**の値なので 1 回目は
// 正しく未読と分かるが、開き直すと全部既読になる。純正 WebUI の未読表示も
// 一緒に消える。
func smsList(ch *ATChannel) map[string]any {
	msgs, bad, err := collectSMS(ch)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	out := map[string]any{"messages": msgs, "count": len(msgs)}
	if len(bad) > 0 {
		out["errors"] = bad
	}
	return out
}

// collectSMS reads the modem and returns the messages.
//
// **map ではなくスライスで返すこと。** 取り込み側が `map[string]any` から
// 型アサーションで取り出す形にしていたが、返り値の型を変えると黙って nil に
// なり、**「0 通読んで 0 通追加しました」と成功のふりをする**。
func collectSMS(ch *ATChannel) ([]smsMessage, []string, error) {
	// **毎回 PDU モードを立てる。** 既定は 0 だが、モデムはベンダの
	// スタックと共用なので誰かがテキストモードにしている前提で書く。
	if _, err := ch.Command("AT+CMGF=0"); err != nil {
		return nil, nil, fmt.Errorf("PDU モードにできません: %w", err)
	}
	lines, err := ch.Command("AT+CMGL=4") // 4 = ALL
	if err != nil {
		return nil, nil, fmt.Errorf("SMS を読めません: %w", err)
	}
	parts, bad := parseCMGL(lines)
	msgs := reassemble(parts)
	if msgs == nil {
		msgs = []smsMessage{} // JSON に null ではなく [] を出す
	}
	return msgs, bad, nil
}

// smsStatus is the cheap poll: how many are stored, without reading them.
//
// **本文を読まない**ので未読が既読に変わらない。画面の定期更新はこちらを使う。
func smsStatus(ch *ATChannel) map[string]any {
	lines, err := ch.Command("AT+CPMS?")
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("保存領域を読めません: %v", err)}
	}
	v, ok := First(lines, "+CPMS:")
	if !ok {
		return map[string]any{"error": "保存領域の応答がありません。"}
	}
	f := splitAT(v)
	out := map[string]any{}
	if len(f) >= 3 {
		out["storage"] = strings.Trim(f[0], "\"")
		out["used"], _ = strconv.Atoi(strings.TrimSpace(f[1]))
		out["total"], _ = strconv.Atoi(strings.TrimSpace(f[2]))
	}
	return out
}

// parseCMGL pairs each "+CMGL: ..." header with the hex PDU that follows it.
//
// **行番号で組にしないこと。** URC が割り込むので、ヘッダの次の
// 「16 進だけの行」を本体とする。
func parseCMGL(lines []string) ([]smsPart, []string) {
	var parts []smsPart
	var bad []string
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "+CMGL:") {
			continue
		}
		f := splitAT(strings.TrimPrefix(lines[i], "+CMGL:"))
		if len(f) < 2 {
			continue
		}
		idx, _ := strconv.Atoi(strings.TrimSpace(f[0]))
		stat, _ := strconv.Atoi(strings.TrimSpace(f[1]))

		body := ""
		for j := i + 1; j < len(lines); j++ {
			if strings.HasPrefix(lines[j], "+") {
				break // 次のヘッダか URC。本体は無かった
			}
			if isHexLine(lines[j]) {
				body = lines[j]
				i = j
				break
			}
		}
		if body == "" {
			bad = append(bad, fmt.Sprintf("#%d: PDU が見つかりません", idx))
			continue
		}
		p, err := decodeDeliver(body)
		if err != nil {
			bad = append(bad, fmt.Sprintf("#%d: %v", idx, err))
			continue
		}
		parts = append(parts, smsPart{Index: idx, Unread: stat == 0, Raw: body, PDU: p})
	}
	return parts, bad
}

func isHexLine(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 8 || len(s)%2 != 0 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789ABCDEFabcdef", c) {
			return false
		}
	}
	return true
}

// reassemble joins concatenated parts and sorts newest first.
//
// **欠けた連結を隠さない。** 破片が 1 つ届いていないのは普通に起きるので、
// 揃うまで伏せるのではなく、届いているぶんを繋いで欠番を添える。
func reassemble(parts []smsPart) []smsMessage {
	type key struct {
		from string
		ref  int
		tot  int
	}
	groups := map[key][]smsPart{}
	var out []smsMessage

	for _, p := range parts {
		if p.PDU.Concat == nil {
			out = append(out, smsMessage{
				Indexes: []int{p.Index}, From: p.PDU.From, Text: p.PDU.Text,
				Unread: p.Unread, when: p.PDU.Time, raw: []string{p.Raw},
				Time: p.PDU.Time.Format(time.RFC3339),
			})
			continue
		}
		c := p.PDU.Concat
		k := key{p.PDU.From, c.Ref, c.Total}
		groups[k] = append(groups[k], p)
	}

	for k, g := range groups {
		sort.Slice(g, func(i, j int) bool { return g[i].PDU.Concat.Seq < g[j].PDU.Concat.Seq })
		var text strings.Builder
		m := smsMessage{From: k.from, Parts: k.tot}
		seen := map[int]bool{}
		for _, p := range g {
			text.WriteString(p.PDU.Text)
			m.Indexes = append(m.Indexes, p.Index)
			m.raw = append(m.raw, p.Raw)
			seen[p.PDU.Concat.Seq] = true
			// **1 つでも未読なら未読。** 途中まで読んだ扱いにしても
			// 画面では意味を持たない。
			if p.Unread {
				m.Unread = true
			}
			// 時刻は先頭の破片のものを採る。
			if m.when.IsZero() || p.PDU.Concat.Seq == 1 {
				m.when = p.PDU.Time
			}
		}
		for s := 1; s <= k.tot; s++ {
			if !seen[s] {
				m.Missing = append(m.Missing, s)
			}
		}
		m.Text = text.String()
		m.Time = m.when.Format(time.RFC3339)
		out = append(out, m)
	}

	for i := range out {
		out[i].Hash = messageHash(out[i].raw)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].when.Equal(out[j].when) {
			return out[i].Indexes[0] > out[j].Indexes[0]
		}
		return out[i].when.After(out[j].when)
	})
	return out
}
