// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912

package main

import (
	"bufio"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// 特定の1台に対する簡易ポートスキャン + バナー取得。接続機器一覧を出すたびに
// 全台へ行うと重いので、画面から個別に呼ぶオンデマンド機能にしている。
// ローカルLAN内への接続のみで、Air6自身の外向き通信問題とは無関係。

var commonPorts = map[int]string{
	21: "FTP", 22: "SSH", 23: "Telnet", 25: "SMTP", 53: "DNS",
	80: "HTTP", 110: "POP3", 139: "NetBIOS", 143: "IMAP", 443: "HTTPS",
	445: "SMB", 554: "RTSP", 631: "IPP(printer)", 993: "IMAPS", 995: "POP3S",
	3389: "RDP", 5000: "UPnP/other", 5357: "WSD", 8008: "Chromecast",
	8009: "Chromecast", 8080: "HTTP-alt", 8443: "HTTPS-alt", 9100: "JetDirect(printer)",
	32400: "Plex",
}

type openPort struct {
	Port    int    `json:"port"`
	Service string `json:"service"`
	Banner  string `json:"banner,omitempty"`
}

func scanPorts(ip string) map[string]any {
	if net.ParseIP(ip) == nil {
		return map[string]any{"error": fmt.Sprintf("bad ip %q", ip)}
	}

	var mu sync.Mutex
	var open []openPort
	var wg sync.WaitGroup
	for port, service := range commonPorts {
		wg.Add(1)
		go func(port int, service string) {
			defer wg.Done()
			addr := fmt.Sprintf("%s:%d", ip, port)
			conn, err := net.DialTimeout("tcp4", addr, 400*time.Millisecond)
			if err != nil {
				return
			}
			banner := grabBanner(conn, port)
			conn.Close()
			mu.Lock()
			open = append(open, openPort{Port: port, Service: service, Banner: banner})
			mu.Unlock()
		}(port, service)
	}
	wg.Wait()

	sort.Slice(open, func(i, j int) bool { return open[i].Port < open[j].Port })
	return map[string]any{"ip": ip, "open": open}
}

// grabBanner はポート種別に応じて短いバナー取得を試みる。失敗しても空文字を
// 返すだけで、ポートが開いていること自体は既に分かっているので実害は無い。
func grabBanner(conn net.Conn, port int) string {
	_ = conn.SetDeadline(time.Now().Add(700 * time.Millisecond))

	switch port {
	case 80, 8080:
		fmt.Fprintf(conn, "HEAD / HTTP/1.0\r\nHost: x\r\n\r\n")
		return readHeader(conn, "server")
	case 21, 22, 25, 110, 143:
		// FTP/SSH/SMTP/POP3/IMAPはこちらから何も送らなくても最初にバナーを送ってくる。
		line, _ := bufio.NewReader(conn).ReadString('\n')
		return strings.TrimSpace(line)
	default:
		return ""
	}
}

// readHeader はHTTPレスポンスのヘッダ行から指定した名前(小文字比較)の値を拾う。
func readHeader(conn net.Conn, want string) string {
	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line == "\r" {
			break
		}
		i := strings.IndexByte(line, ':')
		if i < 0 {
			continue
		}
		if strings.ToLower(strings.TrimSpace(line[:i])) == want {
			return strings.TrimSpace(line[i+1:])
		}
	}
	return ""
}
