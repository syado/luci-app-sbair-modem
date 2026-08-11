// SPDX-License-Identifier: MIT
// Copyright (c) 2026 syado

package main

import (
	"net"
	"time"
)

// LLMNR(Link-Local Multicast Name Resolution, RFC 4795)での逆引き。
//
// Windows機はデフォルトでLLMNRを話す(NetBIOSと違い、ファイル共有設定を
// 切っていても大抵は生きている)。mDNS(mdns.go)と同じDNSワイヤフォーマットを
// 使うので、そちらのエンコード/デコード関数(encodeName・encodeQuery・
// parsePTRAnswers・readName)をそのまま再利用する。マルチキャスト先が
// 224.0.0.252:5355 な点だけがmDNSと違う。
//
// 🔴 **この機体での実機検証はまだ行っていない**(構文上はRFC通りに組んだのみ)。
// 失敗しても他の解決手段(NBNS/mDNS/SSDP)に影響しない読み取り専用のUDP問い合わせ
// なので安全側に倒して先に追加し、実機で応答が来るかは追って確認する。

const llmnrAddr = "224.0.0.252:5355"

// llmnrReverseLookup は ip のリストを受け取り、引けた分だけ ip → ホスト名 を返す。
func llmnrReverseLookup(ips []string) map[string]string {
	out := map[string]string{}
	if len(ips) == 0 {
		return out
	}

	iface, err := net.InterfaceByName("br-lan")
	if err != nil {
		return out
	}
	group, err := net.ResolveUDPAddr("udp4", llmnrAddr)
	if err != nil {
		return out
	}
	conn, err := net.ListenMulticastUDP("udp4", iface, group)
	if err != nil {
		return out
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(1200 * time.Millisecond))

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
			break
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
