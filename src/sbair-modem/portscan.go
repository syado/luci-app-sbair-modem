// SPDX-License-Identifier: MIT
// Copyright (c) 2026 syado

package main

import (
	"bufio"
	"fmt"
	"net"
	"sort"
	"strconv"
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

// scanConcurrency は同時に張る接続数の上限。無制限にgoroutineを立てると、
// 範囲指定で数千ポートを渡されたときにこの機体(組み込みARM、メモリ限られる)
// のソケット/メモリを一気に食う。ポートあたり400msのタイムアウトなので、
// 上限200でも65535ポート全域を試すと最悪 65535/200*0.4s ≈ 130秒かかる —
// 遅いのは承知の上で、範囲は呼び出し側([]clients.js)で上限を示して選ばせる。
const scanConcurrency = 200

// maxScanPorts は1回のリクエストで許可するポート数の上限。これを超える指定は
// 先頭からこの数だけに切り詰める(暴走防止。UI側にも同じ上限を出す)。
const maxScanPorts = 4096

type openPort struct {
	Port    int    `json:"port"`
	Service string `json:"service"`
	Banner  string `json:"banner,omitempty"`
}

// parsePortSpec は "1-1024,8080,9100" のようなカンマ区切り(範囲は"a-b")を
// ポート番号の重複無し集合にする。空文字列や不正なトークンは無視する
// (1つでも不正だからと全体をエラーにすると、他の正しい指定まで無駄になる)。
func parsePortSpec(spec string) []int {
	seen := map[int]bool{}
	var out []int
	add := func(p int) {
		if p < 1 || p > 65535 || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, tok := range strings.Split(spec, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if i := strings.IndexByte(tok, '-'); i > 0 {
			lo, errLo := strconv.Atoi(strings.TrimSpace(tok[:i]))
			hi, errHi := strconv.Atoi(strings.TrimSpace(tok[i+1:]))
			if errLo != nil || errHi != nil || lo > hi {
				continue
			}
			for p := lo; p <= hi && len(out) < maxScanPorts; p++ {
				add(p)
			}
			continue
		}
		if p, err := strconv.Atoi(tok); err == nil {
			add(p)
		}
		if len(out) >= maxScanPorts {
			break
		}
	}
	return out
}

// scanPorts は ip の指定ポートを走査する。ports が空文字なら、これまで通り
// commonPorts(よく使う約25ポート)だけを見る。
func scanPorts(ip, ports string) map[string]any {
	if net.ParseIP(ip) == nil {
		return map[string]any{"error": fmt.Sprintf("bad ip %q", ip)}
	}

	targets := map[int]string{}
	if strings.TrimSpace(ports) == "" {
		targets = commonPorts
	} else {
		for _, p := range parsePortSpec(ports) {
			if service, ok := commonPorts[p]; ok {
				targets[p] = service
			} else {
				targets[p] = ""
			}
		}
		if len(targets) == 0 {
			return map[string]any{"error": "ポート指定を解釈できません(例: 1-1024,8080)"}
		}
	}

	var mu sync.Mutex
	var open []openPort
	var wg sync.WaitGroup
	sem := make(chan struct{}, scanConcurrency)
	for port, service := range targets {
		wg.Add(1)
		sem <- struct{}{}
		go func(port int, service string) {
			defer wg.Done()
			defer func() { <-sem }()
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
	return map[string]any{"ip": ip, "open": open, "scanned": len(targets)}
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
