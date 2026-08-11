// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912

package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os/exec"
	"strings"
)

// 広告ブロックの自己登録ページ。認証は無い(自分の端末しか操作できない設計のため
// 不要と判断した)。LuCIとは独立した専用ポートで公開する。
//
// 仕組み: アクセスしてきたHTTP接続の送信元IP(r.RemoteAddr)を、clients.goと同じ
// `ip neigh` でMACに変換する。表示・操作の対象は「今アクセスしてきた本人の端末」
// だけに限定されるので、他人の端末を勝手に操作されることはない。

const portalPort = ":8090"

// macFromIP は clients.go の clientList と同じ ip neigh を使って、
// 指定IPに対応するMACアドレスを引く。見つからなければ空文字。
func macFromIP(ip string) string {
	out, err := exec.Command("ip", "neigh", "show", "dev", "br-lan").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 1 || f[0] != ip {
			continue
		}
		for i, tok := range f {
			if tok == "lladdr" && i+1 < len(f) {
				return strings.ToLower(f[i+1])
			}
		}
	}
	return ""
}

func portalRequesterMAC(r *http.Request) (mac, ip string) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return macFromIP(host), host
}

func portalPage(mac, ip string, enabled bool, message string) string {
	status := "無効"
	statusColor := "#888"
	toggleLabel := "広告ブロックを有効にする"
	nextVal := "1"
	if enabled {
		status = "有効"
		statusColor = "#5cb85c"
		toggleLabel = "広告ブロックを無効にする"
		nextVal = "0"
	}

	body := fmt.Sprintf(`<h2>この端末の広告ブロック設定</h2>`)
	if mac == "" {
		body += fmt.Sprintf(`<p style="color:#c00">この端末(IP: %s)のMACアドレスを特定できませんでした。
			少し待ってからページを再読み込みしてください。</p>`, ip)
	} else {
		body += fmt.Sprintf(`<p>あなたの端末: <code>%s</code></p>`, mac)
		body += fmt.Sprintf(`<p>現在の状態: <b style="color:%s">%s</b></p>`, statusColor, status)
		body += fmt.Sprintf(`<form method="post" action="/toggle">
			<input type="hidden" name="enabled" value="%s">
			<button type="submit" style="font-size:1.1em;padding:.6em 1.2em;">%s</button>
		</form>`, nextVal, toggleLabel)
		body += `<p style="opacity:.7;margin-top:1em">DNS over HTTPS/TLSを使うアプリ・ブラウザには効きません。</p>`
	}
	if message != "" {
		body += fmt.Sprintf(`<p style="color:#5cb85c">%s</p>`, message)
	}

	return fmt.Sprintf(`<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>広告ブロック設定</title>
<style>body{font-family:sans-serif;max-width:32em;margin:2em auto;padding:0 1em}
code{background:#eee;padding:.1em .4em;border-radius:.2em}</style>
</head><body>%s</body></html>`, body)
}

func portalHandler(w http.ResponseWriter, r *http.Request) {
	mac, ip := portalRequesterMAC(r)
	message := ""

	if r.Method == http.MethodPost && r.URL.Path == "/toggle" {
		if mac == "" {
			http.Error(w, "MACアドレスを特定できません", http.StatusServiceUnavailable)
			return
		}
		_ = r.ParseForm()
		res := adblockSet(mac, r.FormValue("enabled"))
		if e, ok := res["error"]; ok {
			message = fmt.Sprintf("エラー: %v", e)
		} else {
			message = "設定を反映しました。"
		}
	}

	enabled := mac != "" && adblockMacs()[mac]
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, portalPage(mac, ip, enabled, message))
}

func runPortal() int {
	http.HandleFunc("/", portalHandler)
	http.HandleFunc("/toggle", portalHandler)
	log.Printf("sbair-portal listening on %s", portalPort)
	if err := http.ListenAndServe(portalPort, nil); err != nil {
		log.Printf("sbair-portal: %v", err)
		return 1
	}
	return 0
}
