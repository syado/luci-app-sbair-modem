// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912

package main

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// IMS の状態と切替。**出荷状態では無効で、そのままだと SMS が 1 通も届かない。**
//
// ⚠ **判定に `mipc_wan_cli --ims_get_config` を使わないこと。** 書く先と
// 読む先が食い違っているらしく、**登録しているのに `Off` を返す**。
// 実態は `AT+CIREG?` で見る。config の値は参考として出すだけにする。
//
// ⚠ **無効にしても IMS が完全に落ちるとは限らない。** 表示は ext_info の
// ビットをそのまま出し、「オン/オフ」と断定しないこと。
//
// 切替の実測と ext_info の意味は sbair6-rs の docs/AT.md「IMS」。

// imsService maps the +CIREG <ext_info> bitmap (TS 27.007 §8.68).
var imsService = []struct {
	bit  int
	name string
}{
	{1, "音声 (MMTEL)"},
	{2, "テキスト"},
	{4, "SMS over IMS"},
	{8, "ビデオ"},
}

func imsStatus(ch *ATChannel) map[string]any {
	out := map[string]any{}

	// **真偽はこちら。** n,<reg_info>[,<ext_info>]
	if lines, err := ch.Command("AT+CIREG?"); err == nil {
		if v, ok := Last(lines, "+CIREG:"); ok {
			out["cireg"] = v
			f := splitAT(v)
			if len(f) >= 2 {
				reg, _ := strconv.Atoi(strings.TrimSpace(f[1]))
				out["registered"] = reg == 1
			}
			if len(f) >= 3 {
				ext, _ := strconv.Atoi(strings.TrimSpace(f[2]))
				out["ext_info"] = ext
				var svc []string
				for _, s := range imsService {
					if ext&s.bit != 0 {
						svc = append(svc, s.name)
					}
				}
				out["services"] = svc
			}
		}
	} else {
		out["error"] = fmt.Sprintf("モデムに聞けません: %v", err)
	}

	// 参考値。**実態と食い違うので、これで判定しない。**
	if v, err := mipc("--ims_get_config"); err == nil {
		out["config"] = v
		out["config_on"] = strings.Contains(v, "On")
	}
	return out
}

// imsSet turns the modem's IMS stack on or off.
//
// **再起動はまたぐ。** 効かなくなるのは設定を初期化したときで、
// そのとき SIM ロックが出荷既定へ戻り、圏外になるので IMS も登録できない。
// (SIM ロックを解除すれば戻る)
func imsSet(ch *ATChannel, on bool) map[string]any {
	arg := "0"
	if on {
		arg = "1"
	}
	v, err := mipc("--ims_set_config", arg)
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("切り替えられません: %v", err)}
	}
	if !strings.Contains(v, "success") {
		return map[string]any{"error": "モデムが受け付けませんでした: " + strings.TrimSpace(v)}
	}
	out := imsStatus(ch)
	out["result"] = "ok"
	if on {
		// 登録まで 10 秒ほどかかる。押した直後は未登録に見えるのが普通。
		out["note"] = "有効にしました。登録まで 10〜30 秒かかります。"
	} else {
		out["note"] = "無効にしました。"
	}
	return out
}

// mipc runs the vendor CLI with a bound. **時間を切らないと画面が固まる** —
// モデムが応答しないときに戻ってこないことがある。
func mipc(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, "mipc_wan_cli", args...).CombinedOutput()
	if err != nil && len(b) == 0 {
		return "", err
	}
	return string(b), nil
}
