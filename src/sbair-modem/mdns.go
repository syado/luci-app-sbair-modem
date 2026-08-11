// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// mDNS(マルチキャストDNS, RFC 6762)での逆引き。
//
// **上位の光回線ルータは自分のDHCPクライアントのPTRレコードを持たない**
// (実機でNXDOMAINを確認済み。docs参照)ため、通常のユニキャストDNS逆引き
// (reverseLookup, clients.go)では端末名が引けない。一方、mDNS対応の端末
// (iOS/macOS/多くのAndroid・Windows)は"<逆順アドレス>.in-addr.arpa"の
// PTR問い合わせにマルチキャストで応答することが多いので、こちらを試す。
//
// 外部ライブラリは使わず、素のUDPソケットとDNSメッセージの手組みで済ませる
// (質問1本・応答のPTR解析のみで足りるため)。

const (
	mdnsAddr = "224.0.0.251:5353"
	mdnsPort = 5353
)

// mdnsReverseLookup は ip のリストを受け取り、引けた分だけ ip → ホスト名 を返す。
// br-lan が無ければ何もしない。ベストエフォート。
func mdnsReverseLookup(ips []string) map[string]string {
	out := map[string]string{}
	if len(ips) == 0 {
		return out
	}

	iface, err := net.InterfaceByName("br-lan")
	if err != nil {
		return out
	}
	group, err := net.ResolveUDPAddr("udp4", mdnsAddr)
	if err != nil {
		return out
	}
	conn, err := net.ListenMulticastUDP("udp4", iface, group)
	if err != nil {
		return out
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(1500 * time.Millisecond))

	// 逆引き名(arpaName) → 元のIP、応答が来たときに引き当てる用。
	wantName := map[string]string{}
	var questions [][]byte
	for _, ip := range ips {
		arpa, ok := arpaName(ip)
		if !ok {
			continue
		}
		wantName[arpa] = ip
		q, err := encodeName(arpa)
		if err != nil {
			continue
		}
		questions = append(questions, q)
	}
	if len(questions) == 0 {
		return out
	}

	if pkt, err := encodeQuery(questions); err == nil {
		_, _ = conn.WriteToUDP(pkt, group)
	}

	buf := make([]byte, 9000)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			break // タイムアウト、もしくはこれ以上読めない。
		}
		for arpa, host := range parsePTRAnswers(buf[:n]) {
			if ip, ok := wantName[arpa]; ok {
				if _, already := out[ip]; !already {
					out[ip] = host
				}
			}
		}
	}
	return out
}

// arpaName は "192.168.0.5" を "5.0.168.192.in-addr.arpa" に変換する。
func arpaName(ipStr string) (string, bool) {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return "", false
	}
	v4 := ip.To4()
	if v4 == nil {
		return "", false
	}
	return fmt.Sprintf("%d.%d.%d.%d.in-addr.arpa", v4[3], v4[2], v4[1], v4[0]), true
}

// encodeName はDNSのラベル形式(長さ1バイト+ラベル…+終端0x00)にする。
func encodeName(name string) ([]byte, error) {
	var b []byte
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 {
			return nil, errors.New("bad label")
		}
		b = append(b, byte(len(label)))
		b = append(b, label...)
	}
	b = append(b, 0)
	return b, nil
}

// encodeQuery は questions(それぞれ encodeName 済みの名前 + QTYPE/QCLASS)を
// 1本のDNSメッセージにまとめる。PTR(12) / IN(1) 固定。
func encodeQuery(names [][]byte) ([]byte, error) {
	hdr := make([]byte, 12)
	binary.BigEndian.PutUint16(hdr[4:], uint16(len(names))) // QDCOUNT
	msg := hdr
	for _, name := range names {
		msg = append(msg, name...)
		tail := make([]byte, 4)
		binary.BigEndian.PutUint16(tail[0:], 12) // QTYPE = PTR
		binary.BigEndian.PutUint16(tail[2:], 1)  // QCLASS = IN
		msg = append(msg, tail...)
	}
	return msg, nil
}

// readName はDNSメッセージ中の名前(ポインタ圧縮対応)を読む。
func readName(msg []byte, off int) (string, int, error) {
	var labels []string
	start := off
	jumped := false
	guard := 0
	for {
		guard++
		if guard > 128 {
			return "", 0, errors.New("name too deep")
		}
		if off >= len(msg) {
			return "", 0, errors.New("truncated name")
		}
		l := int(msg[off])
		if l == 0 {
			off++
			break
		}
		if l&0xC0 == 0xC0 { // 圧縮ポインタ
			if off+1 >= len(msg) {
				return "", 0, errors.New("truncated pointer")
			}
			ptr := int(l&0x3F)<<8 | int(msg[off+1])
			if !jumped {
				start = off + 2
			}
			jumped = true
			off = ptr
			continue
		}
		off++
		if off+l > len(msg) {
			return "", 0, errors.New("truncated label")
		}
		labels = append(labels, string(msg[off:off+l]))
		off += l
	}
	if jumped {
		return strings.Join(labels, "."), start, nil
	}
	return strings.Join(labels, "."), off, nil
}

// parsePTRAnswers は受信した1パケットから、PTRレコードの
// (問い合わせ名 → ターゲットのホスト名) をすべて拾う。
// Answer/Authority/Additional のどこに入っていても見る
// (実装によって置き場所が違うため)。
func parsePTRAnswers(msg []byte) map[string]string {
	out := map[string]string{}
	if len(msg) < 12 {
		return out
	}
	qd := int(binary.BigEndian.Uint16(msg[4:6]))
	an := int(binary.BigEndian.Uint16(msg[6:8]))
	ns := int(binary.BigEndian.Uint16(msg[8:10]))
	ar := int(binary.BigEndian.Uint16(msg[10:12]))

	off := 12
	for i := 0; i < qd; i++ {
		_, next, err := readName(msg, off)
		if err != nil {
			return out
		}
		off = next + 4 // QTYPE + QCLASS
	}

	total := an + ns + ar
	for i := 0; i < total; i++ {
		name, next, err := readName(msg, off)
		if err != nil {
			return out
		}
		off = next
		if off+10 > len(msg) {
			return out
		}
		rtype := binary.BigEndian.Uint16(msg[off : off+2])
		rdlen := int(binary.BigEndian.Uint16(msg[off+8 : off+10]))
		off += 10
		if off+rdlen > len(msg) {
			return out
		}
		if rtype == 12 { // PTR
			target, _, err := readName(msg, off)
			if err == nil && target != "" {
				out[strings.ToLower(name)] = target
			}
		}
		off += rdlen
	}
	return out
}
