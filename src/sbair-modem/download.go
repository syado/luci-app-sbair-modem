// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912

package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/damonto/euicc-go/lpa"
	sgp22 "github.com/damonto/euicc-go/v2"
)

// ES9+ — profile のダウンロード(eSIM のインストール)。
//
// **シェルでは書けない唯一の部分。** SM-DP+ への TLS と ECDSA の署名検証、
// それに完全な ASN.1 が要る。実測で 20〜30 秒かかるので、SIM マッピングの
// 切替と同じく非同期にしてある(足回りは job.go)。

func startDownload(code, cc string) map[string]any {
	code = strings.TrimSpace(code)
	if code == "" {
		return map[string]any{"error": "アクティベーションコードを入れてください。"}
	}
	if _, err := parseActivationCode(code); err != nil {
		return map[string]any{"error": err.Error()}
	}
	out := startJob("download", code, cc)
	if _, bad := out["error"]; !bad {
		out["note"] = "20〜30 秒かかります。SM-DP+ との通信が終わるまで待ってください。"
	}
	return out
}

// runDownloadWorker installs one profile. Detached, so progress goes to the
// state file rather than stdout.
func runDownloadWorker(code, cc string) int {
	j := newJob("download")
	j.write()

	ac, err := parseActivationCode(code)
	if err != nil {
		return j.fail("アクティベーションコード", err.Error())
	}
	if cc != "" {
		ac.ConfirmationCode = cc
	}

	ch := NewATChannel(*device)
	ch.SetTimeout(120 * time.Second)
	if err := ch.Connect(); err != nil {
		return j.fail("接続", err.Error())
	}
	defer ch.Disconnect()

	j.step("eUICC を開く")
	kind, _, _, aid := inspectCard(ch)
	if kind != cardEUICC {
		return j.fail("eUICC を開く",
			"eUICC がありません。物理スロットの eUICC カードへ切り替えてください。")
	}
	client, err := openEUICC(ch, aid)
	if err != nil {
		return j.fail("eUICC を開く", err.Error())
	}
	// os.Exit は defer を飛ばすので、返る前に必ず閉じる。
	closed := false
	release := func() {
		if !closed {
			client.Close()
			closed = true
		}
	}
	defer release()

	// **SM-DP+ は IMEI を要求する。** モデムが知っているものを使う。
	j.step("IMEI を取得")
	if ac.IMEI = *imei; ac.IMEI == "" {
		v, err := ch.IMEI()
		if err != nil {
			release()
			return j.fail("IMEI", fmt.Sprintf("モデムから読めません: %v", err))
		}
		ac.IMEI = v
	}

	j.step("SM-DP+ と通信")
	opts := &lpa.DownloadOptions{
		OnProgress: func(s lpa.DownloadStage) {
			// **string(s) と書かないこと。** DownloadStage は uint8 で、
			// string() はルーン変換になり "\x01" が出る。String() を通す。
			j.state.Stages = append(j.state.Stages, s.String())
			j.step(stageLabel(s.String()))
		},
		// ここまで来たら利用者はダウンロードを求めている。問い直さない。
		OnConfirm:               func(*sgp22.ProfileInfo) bool { return true },
		OnEnterConfirmationCode: func() string { return cc },
	}
	if _, err := client.DownloadProfile(context.Background(), ac, opts); err != nil {
		release()
		return j.fail("ダウンロード", err.Error())
	}
	release()
	return j.done("完了", "profile を追加しました。有効化は一覧から行ってください。")
}

// stageLabel turns euicc-go's stage names into something a screen can show.
func stageLabel(s string) string {
	switch s {
	case "Authenticating Client":
		return "eUICC の認証"
	case "Authenticating Server":
		return "SM-DP+ の認証"
	case "Installing":
		return "profile の書き込み"
	}
	return s
}

// parseActivationCode accepts "LPA:1$smdp$matchingid[$oid][$cc]" and also a
// bare "smdp$matchingid", which is what people usually paste.
func parseActivationCode(s string) (*lpa.ActivationCode, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "LPA:"), "1$")
	parts := strings.Split(s, "$")
	if len(parts) < 1 || parts[0] == "" {
		return nil, fmt.Errorf("アクティベーションコード %q に SM-DP+ のアドレスがありません", s)
	}
	ac := &lpa.ActivationCode{SMDP: &url.URL{Scheme: "https", Host: parts[0]}}
	if len(parts) > 1 {
		ac.MatchingID = parts[1]
	}
	if len(parts) > 2 {
		ac.OID = parts[2]
	}
	if len(parts) > 3 && parts[3] != "" {
		ac.ConfirmationCode = parts[3]
	}
	return ac, nil
}
