// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912

package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// SIM ロック(ネットワークロック)の状態と切替。

// **鍵と PLMN はファームウェアの /bin/sim_lock.sh から読んだもの。**
// スクリプトを呼ばず自分で AT を打つのは、スクリプトが無い版でも動かすため。
// 冒頭のコメントに SB_ID / WCP_ID / SIM_ID として置かれている値をそのまま使う。
const (
	simlockKey = "50603202"
	simlockSB  = "44020" // SoftBank
	simlockWCP = "44100" // ロック時にカテゴリ 0 へ入れる PLMN
	simlockGID = "67"    // カテゴリ 2 の GID1
)

// simlockState reads the lock without changing anything.
//
// AT+ESMLCK? はカテゴリごとに 7 要素のタプルを返す:
//
//	(0,1,5,0,1,15,0),(1,2,5,0,0,10,0),(2,1,5,0,1,10,0),…,"<IMSI>",1,16,1,0,2
//	 │ │ │ │ │ │  └ autolock_count (推定)
//	 │ │ │ │ │ └─── そのカテゴリに保管できる件数。**定数**
//	 │ │ │ │ └───── いま登録されているロックデータの件数
//	 │ │ │ └─────── retry_count (推定)
//	 │ │ └───────── max_retry_count。**これが試行予算**
//	 │ └─────────── state: 1 = ロック中 / 2 = 解除済み
//	 └───────────── カテゴリ 0-6
//
// **6 番目を「残り試行回数」と読んではいけない。** 15/10/10/10/2/2/2 は
// MDDB の構造体サイズ(code_cat_n 45B ÷ PLMN 3B = 15、code_cat_sp 230B ÷ 23B
// = 10、…)と一致する容量の定数で、ロック→解除→削除を経ても変わらない。
// **実際の予算は 3 番目 = 5。** ここを取り違えると「15 回試せる」と誤解する。
//
// 4 番目と 7 番目のどちらが retry_count でどちらが autolock_count かは
// 構造体からは決められない(どちらも 0)。ただし**用途は決まる**:
// 4 番目が「残り回数」なら 0 = 使い切りのはずで、正しい鍵で解除できた
// 事実と矛盾する。よって **4 番目は「使った回数」**で、残りは
// max_retry - retry_count。
func simlockState(ch *ATChannel) map[string]any {
	out := map[string]any{}

	if lines, err := ch.Command(`AT+CLCK="PN",2`); err == nil {
		if v, ok := First(lines, "+CLCK:"); ok {
			out["locked"] = strings.TrimSpace(v) == "1"
		}
	}
	lines, err := ch.Command("AT+ESMLCK?")
	if err != nil {
		out["error"] = err.Error()
		return out
	}
	v, ok := First(lines, "+ESMLCK:")
	if !ok {
		return out
	}
	out["raw"] = v

	type cat struct {
		Category  int    `json:"category"`
		Label     string `json:"label"`
		Locked    bool   `json:"locked"`
		Entries   int    `json:"entries"`   // 登録されているロックデータ件数
		Capacity  int    `json:"capacity"`  // 保管できる件数 (定数)
		MaxRetry  int    `json:"max_retry"` // 試行予算
		Remaining int    `json:"remaining"` // max_retry - retry_count
	}
	// カテゴリ 0-4 は 3GPP TS 22.022 の personalisation category。
	// **5 と 6 は MTK の拡張**で、規格には無い(モデム内の +CPIN 文字列表に
	// PH-NSSP / PH-SIMC があることで確認)。
	labels := map[int]string{
		0: "ネットワーク", 1: "ネットワークサブセット", 2: "サービスプロバイダ",
		3: "コーポレート", 4: "SIM/USIM",
		5: "ネットワークサブセット+事業者 (MTK 拡張)",
		6: "SIM+コーポレート (MTK 拡張)",
	}
	num := func(s string) int {
		var n int
		fmt.Sscanf(strings.TrimSpace(s), "%d", &n)
		return n
	}
	var cats []cat
	for _, part := range strings.Split(v, ")") {
		part = strings.TrimLeft(strings.TrimSpace(part), ",")
		part = strings.TrimPrefix(part, "(")
		f := strings.Split(part, ",")
		if len(f) < 6 {
			continue
		}
		var c cat
		if _, err := fmt.Sscanf(f[0], "%d", &c.Category); err != nil {
			continue
		}
		c.Label = labels[c.Category]
		c.Locked = strings.TrimSpace(f[1]) == "1"
		c.MaxRetry = num(f[2])
		c.Entries = num(f[4])
		c.Capacity = num(f[5])
		c.Remaining = c.MaxRetry - num(f[3])
		cats = append(cats, c)
	}
	if len(cats) > 0 {
		out["categories"] = cats
	}
	return out
}

func startSimlock(on bool) map[string]any {
	arg := "off"
	if on {
		arg = "on"
	}
	out := startJob("simlock", arg)
	if _, bad := out["error"]; !bad {
		out["note"] = "40〜60 秒かかります。SIM を読み直すため電波を止めます。"
	}
	return out
}

func runSimlockWorker(arg string) int {
	j := newJob("simlock")
	j.write()

	ch := NewATChannel(*device)
	ch.SetTimeout(30 * time.Second)
	if err := ch.Connect(); err != nil {
		return j.fail("接続", err.Error())
	}
	defer ch.Disconnect()

	step, msg, err := simlockApply(ch, arg == "on", j.step)
	if err != nil {
		return j.fail(step, err.Error())
	}
	return j.done(step, msg)
}

// simlockApply does the switch on an **already open** channel.
//
// ⚠ **自前でチャネルを開かないこと。** flock はプロセスの寿命ぶん保持される
// (`Disconnect()` では手放されない) ので、AT を掴んだまま別のチャネルを
// 開こうとすると**自分自身に弾かれる**。実機でこれを踏んだ:
// `another sbair-modem is using the modem`。
func simlockApply(ch *ATChannel, on bool, step func(string)) (string, string, error) {
	arg := "off"
	if on {
		arg = "on"
	}
	// **個々のコマンドの失敗で打ち切らない。** これは宣言的な操作で、
	// 既に解除済みのカテゴリを解除しようとすると `+CME ERROR: 100` が
	// 返るが、望む状態にはなっている。**判定は最後の検証で行う。**
	// (ベンダのスクリプトは戻り値を一切見ないので、この違いが出ない)
	var notes []string
	run := func(cmd string) {
		if _, err := ch.Command(cmd); err != nil {
			notes = append(notes, err.Error())
		}
	}

	// 掛け直しの前にも解除が要る。既存のルールを消さずに追加すると積み上がる。
	step("ロックを解除 (AT+ESMLCK)")
	for _, c := range []string{"0", "2"} {
		for _, op := range []string{"0", "3"} { // 0 = 解除 / 3 = ルール削除
			run(fmt.Sprintf("AT+ESMLCK=%s,%s,%q", c, op, simlockKey))
		}
	}

	if arg == "on" {
		step("ロックを設定 (AT+ESMLCK)")
		run(fmt.Sprintf("AT+ESMLCK=0,2,%q,%q", simlockKey, simlockWCP))
		run(fmt.Sprintf("AT+ESMLCK=2,2,%q,%q,%q", simlockKey, simlockSB, simlockGID))
	}

	// ベンダのスクリプトが併せて触るもの。画面と LED の表示がずれないよう
	// 同じことをする。
	flag := "0"
	if arg == "on" {
		flag = "1"
	}
	_, _ = uci("set", "modeswitch.common.sim_lock="+flag)
	_, _ = uci("commit", "modeswitch")
	if f, err := os.OpenFile("/var/run/knos/kn_ledmond", os.O_WRONLY, 0); err == nil {
		_, _ = f.WriteString("ipc_update_modem,sim;")
		f.Close()
	}

	// **CFUN=4 では足りない。** SIM ごと落とさないと +CPIN の判定が
	// 初期化時のまま残り、解除しても PH-NET PIN が消えない。
	step("SIM を読み直す (AT+CFUN=0)")
	if _, err := ch.Command("AT+CFUN=0"); err != nil {
		return "AT+CFUN=0", "", err
	}
	time.Sleep(15 * time.Second)

	step("電波を戻す (AT+CFUN=1)")
	if _, err := ch.Command("AT+CFUN=1"); err != nil {
		time.Sleep(10 * time.Second)
		_, _ = ch.Command("AT+CFUN=1")
	}

	step("SIM の状態を確認")
	want := (arg == "on")
	for i := 0; i < 12; i++ {
		time.Sleep(5 * time.Second)
		lines, err := ch.Command("AT+CPIN?")
		if err != nil {
			continue
		}
		pin, ok := First(lines, "+CPIN:")
		if !ok {
			continue
		}
		st := simlockState(ch)
		locked, known := st["locked"].(bool)
		if !known {
			continue
		}
		msg := fmt.Sprintf("SIM: %s / ロック: %v", pin, locked)
		if locked == want {
			return "完了", msg, nil
		}
		// 望む状態になっていない。**そのときだけ**、途中で出た誤りを見せる。
		if i == 11 && len(notes) > 0 {
			return "確認", "", errors.New(msg + " / " + strings.Join(notes, " / "))
		}
	}
	return "確認", "", errors.New("切替後に SIM の状態を確認できませんでした。")
}
