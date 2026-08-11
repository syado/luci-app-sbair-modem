// SPDX-License-Identifier: MIT
// Copyright (c) 2026 syado

package main

import (
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// SSDP(UPnPの発見プロトコル)。スマートTV・プリンター・ゲーム機等、mDNS/NBNSに
// 応答しない機器から friendlyName を拾うための3つ目の経路。
//
// M-SEARCHはローカルLANへのマルチキャストで、応答のLOCATION先(同じLAN内の機器)への
// HTTP GETも局所通信のみ。Air6自身の外向き通信問題とは無関係。

const ssdpAddr = "239.255.255.250:1900"

var ssdpRequest = "M-SEARCH * HTTP/1.1\r\n" +
	"HOST: 239.255.255.250:1900\r\n" +
	"MAN: \"ssdp:discover\"\r\n" +
	"MX: 2\r\n" +
	"ST: ssdp:all\r\n\r\n"

// ssdpDiscover は応答してきた機器の ip → friendlyName を返す(引けた分だけ)。
func ssdpDiscover() map[string]string {
	out := map[string]string{}

	group, err := net.ResolveUDPAddr("udp4", ssdpAddr)
	if err != nil {
		return out
	}
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return out
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2500 * time.Millisecond))

	if _, err := conn.WriteToUDP([]byte(ssdpRequest), group); err != nil {
		return out
	}

	locations := map[string]string{} // ip -> LOCATION URL
	buf := make([]byte, 4096)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			break
		}
		loc := parseSSDPLocation(buf[:n])
		if loc != "" {
			locations[from.IP.String()] = loc
		}
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	for ip, loc := range locations {
		wg.Add(1)
		go func(ip, loc string) {
			defer wg.Done()
			if name := fetchFriendlyName(loc); name != "" {
				mu.Lock()
				out[ip] = name
				mu.Unlock()
			}
		}(ip, loc)
	}
	wg.Wait()
	return out
}

func parseSSDPLocation(msg []byte) string {
	for _, line := range strings.Split(string(msg), "\r\n") {
		if i := strings.IndexByte(line, ':'); i > 0 {
			key := strings.ToUpper(strings.TrimSpace(line[:i]))
			if key == "LOCATION" {
				return strings.TrimSpace(line[i+1:])
			}
		}
	}
	return ""
}

// fetchFriendlyName はUPnPデバイス記述XMLを取りに行き、<friendlyName>を抜き出す。
// 厳密なXMLパースはせず、タグの間の文字列を素朴に拾うだけ(この用途には十分)。
func fetchFriendlyName(location string) string {
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get(location)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if err != nil {
		return ""
	}
	const open, close = "<friendlyName>", "</friendlyName>"
	s := string(body)
	i := strings.Index(s, open)
	if i < 0 {
		return ""
	}
	s = s[i+len(open):]
	j := strings.Index(s, close)
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(s[:j])
}
