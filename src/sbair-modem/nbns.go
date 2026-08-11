// SPDX-License-Identifier: MIT
// Copyright (c) 2026 syado

package main

import (
	"encoding/binary"
	"net"
	"strings"
	"sync"
	"time"
)

// NetBIOS Name Service (NBSTAT, RFC 1002)でWindows機器のコンピュータ名を引く。
// `nmblookup -A <ip>` / `nbtstat -A <ip>` と同じことをする。UDP 137へのユニキャスト
// なので、ローカルLAN内の通信のみで完結する(Air6自身の外向き通信問題とは無関係)。

// nbstatQuery は "*" をNetBIOSの第一級エンコード(1バイトずつ上位/下位ニブルを
// 'A'+nibbleにマップし32バイトにする)にしたNBSTAT問い合わせパケットを返す。
func nbstatQuery() []byte {
	// "*" (0x2A) + 0x00 x15 = 16バイトの生の名前を、ニブルごとにエンコードして32バイトに。
	raw := make([]byte, 16)
	raw[0] = '*'
	encoded := make([]byte, 32)
	for i, b := range raw {
		encoded[i*2] = 'A' + (b >> 4)
		encoded[i*2+1] = 'A' + (b & 0x0f)
	}

	pkt := make([]byte, 12)
	binary.BigEndian.PutUint16(pkt[4:], 1) // QDCOUNT=1

	pkt = append(pkt, byte(len(encoded)))
	pkt = append(pkt, encoded...)
	pkt = append(pkt, 0) // ラベル終端(スコープ無し)

	tail := make([]byte, 4)
	binary.BigEndian.PutUint16(tail[0:], 0x0021) // QTYPE = NBSTAT
	binary.BigEndian.PutUint16(tail[2:], 1)      // QCLASS = IN
	return append(pkt, tail...)
}

// parseNBSTAT は応答からワークステーション名(グループ名やサービス名は除く)を取り出す。
//
// 🔴 **実機で文字化けを踏んだ**: 当初「Answer部のNAMEは質問への2バイト圧縮ポインタ
// (0xC0 0x0C)」と決め打ちして固定オフセットで読んでいたが、応答実装によっては
// **NAMEを生の34バイト(1+32+1)でそのまま繰り返す**ものがあり、オフセットが
// 32バイトずれて無関係なバイト列を「名前」として表示してしまった。
// mdns.goのreadName(DNS形式の圧縮ポインタ・生名前どちらにも対応)を使って
// きちんと辿るように直した。
//
// 🔴 **2026-08-12、実機のWindows機を相手に再度踏んだ**: 上の修正後も
// 常に空を返していた。Pythonで生バイトを取って手で追ったところ、
// **QDCOUNT が 0 の応答が実在する**(Windows機の実測: ヘッダは
// `00 00 84 00 00 00 00 01 00 00 00 00` = QDCOUNT=0, ANCOUNT=1)。
// 質問部が無いのに「質問部を読み飛ばす」処理を固定でやっていたため、
// 残り全体のオフセットがまるごとズレて何も拾えていなかった。
// QDCOUNT を見て、0 なら質問部の読み飛ばしそのものを省略する。
func parseNBSTAT(msg []byte) string {
	if len(msg) < 12 {
		return ""
	}
	qdcount := binary.BigEndian.Uint16(msg[4:6])

	off := 12
	if qdcount > 0 {
		var err error
		_, off, err = readName(msg, off) // 質問部のNAMEを読み飛ばす
		if err != nil || off+4 > len(msg) {
			return ""
		}
		off += 4 // QTYPE + QCLASS
	}

	_, off, err := readName(msg, off) // AnswerのNAME
	if err != nil || off+10 > len(msg) {
		return ""
	}
	off += 10 // TYPE(2) + CLASS(2) + TTL(4) + RDLENGTH(2)

	if off >= len(msg) {
		return ""
	}
	numNames := int(msg[off])
	off++
	for i := 0; i < numNames; i++ {
		if off+18 > len(msg) {
			break
		}
		name := strings.TrimRight(string(msg[off:off+15]), " \x00")
		suffix := msg[off+15]
		flags := binary.BigEndian.Uint16(msg[off+16 : off+18])
		off += 18
		const groupBit = 0x8000
		// suffix 0x00 = ワークステーション名(いわゆるコンピュータ名)。
		// グループ名(flags&groupBit)は端末名ではないので除く。
		// printableでないもの・1文字だけのもの(パース位置がずれた場合の保険)も除く。
		if suffix == 0x00 && flags&groupBit == 0 && len(name) >= 2 && isPrintableName(name) {
			return name
		}
	}
	return ""
}

func isPrintableName(s string) bool {
	for _, r := range s {
		if r < 0x20 || r > 0x7e {
			return false
		}
	}
	return true
}

// nbnsLookup は ip のリストを受け取り、引けた分だけ ip → コンピュータ名 を返す。
func nbnsLookup(ips []string) map[string]string {
	out := map[string]string{}
	if len(ips) == 0 {
		return out
	}
	query := nbstatQuery()

	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, ip := range ips {
		wg.Add(1)
		go func(ip string) {
			defer wg.Done()
			conn, err := net.DialTimeout("udp4", ip+":137", 500*time.Millisecond)
			if err != nil {
				return
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(700 * time.Millisecond))
			if _, err := conn.Write(query); err != nil {
				return
			}
			buf := make([]byte, 2048)
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			if name := parseNBSTAT(buf[:n]); name != "" {
				mu.Lock()
				out[ip] = name
				mu.Unlock()
			}
		}(ip)
	}
	wg.Wait()
	return out
}
