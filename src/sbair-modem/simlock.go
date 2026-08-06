// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912

package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// SIM ロック(ネットワークロック)の状態と切替。
//
// **手順はファームウェアの中にあった。** ベンダのスクリプト
// `/bin/sim_lock.sh` が `AT+ESMLCK` を打ち、uci の
// `modeswitch.common.sim_lock` を更新し、LED に通知する。
// ここでは同じことを自分で行う — スクリプトが無い版でも動くように。
//
// ⚠ **解除しただけでは `+CPIN` は変わらない。** SIM の判定は初期化時のもので、
// `AT+CFUN=4` の往復では足りず、`AT+CFUN=0` → `AT+CFUN=1`(SIM ごと落とす方)
// が要る。40 秒ほどかかるので job.go の非同期に載せる。

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
// AT+ESMLCK? answers a tuple per category:
//
//	(0,1,5,0,1,15,0),(1,2,...),(2,1,5,0,1,10,0),...
//	 ^ ^           ^  ^
//	 | 状態 1=ロック  | 残り試行
//	 カテゴリ         ロック中フラグ
//
// ロックされているのはカテゴリ 0 (ネットワーク) と 2 (サービスプロバイダ)。
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

	// カテゴリごとの残り試行回数。**鍵を間違えるとここが減り、
	// 使い切ると戻せなくなる**ので、画面に出して判断材料にする。
	type cat struct {
		Category  int    `json:"category"`
		Label     string `json:"label"`
		Locked    bool   `json:"locked"`
		Remaining int    `json:"remaining"`
	}
	labels := map[int]string{
		0: "ネットワーク", 1: "ネットワークサブセット", 2: "サービスプロバイダ",
		3: "コーポレート", 4: "SIM/USIM",
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
		fmt.Sscanf(f[5], "%d", &c.Remaining)
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
	j.step("ロックを解除 (AT+ESMLCK)")
	for _, c := range []string{"0", "2"} {
		for _, op := range []string{"0", "3"} { // 0 = 解除 / 3 = ルール削除
			run(fmt.Sprintf("AT+ESMLCK=%s,%s,%q", c, op, simlockKey))
		}
	}

	if arg == "on" {
		j.step("ロックを設定 (AT+ESMLCK)")
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
	j.step("SIM を読み直す (AT+CFUN=0)")
	if _, err := ch.Command("AT+CFUN=0"); err != nil {
		return j.fail("AT+CFUN=0", err.Error())
	}
	time.Sleep(15 * time.Second)

	j.step("電波を戻す (AT+CFUN=1)")
	if _, err := ch.Command("AT+CFUN=1"); err != nil {
		time.Sleep(10 * time.Second)
		_, _ = ch.Command("AT+CFUN=1")
	}

	j.step("SIM の状態を確認")
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
			return j.done("完了", msg)
		}
		// 望む状態になっていない。**そのときだけ**、途中で出た誤りを見せる。
		if i == 11 && len(notes) > 0 {
			return j.fail("確認", msg+" / "+strings.Join(notes, " / "))
		}
	}
	return j.fail("確認", "切替後に SIM の状態を確認できませんでした。")
}
