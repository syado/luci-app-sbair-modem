// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// rpcd exec backend for the ubus object "sbair".

// methods is the table handed to rpcd.
//
// **rpcd reads each argument's type from the type of the sample value**, not
// from a type name: `{"mapping": 0}` declares an integer, whereas
// `{"mapping": "int"}` would declare a string and make rpcd reject
// `{"mapping": 1}` before this program ever runs. So the values here are
// zero values of the intended type, not names.
var methods = map[string]map[string]any{
	"overview":      {},
	"esim_status":   {},
	"esim_list":     {},
	"esim_enable":   {"iccid": ""},
	"esim_disable":  {"iccid": ""},
	"esim_delete":   {"iccid": ""},
	"esim_nickname": {"iccid": "", "nickname": ""},
	"simmap_get":    {},
	"simmap_set":    {"mapping": 0},
	"simmap_status": {},
	// eSIM のインストール (ES9+)。20〜30 秒かかるので非同期。
	"esim_download":        {"activation_code": "", "confirmation_code": ""},
	"esim_download_status": {},
	// SIM ロック(ネットワークロック)。切替は CFUN の往復が要るので非同期。
	"simlock_get":    {},
	"simlock_set":    {"on": false},
	"simlock_status": {},
	// APN。ICCID をキーに /etc/config/sbair へ保存する。
	"apn_status": {},
	"apn_set": {"iccid": "", "apn": "", "auth": "", "username": "",
		"password": "", "iptype": "", "label": ""},
	"apn_delete": {"iccid": ""},
	"apn_apply":  {},
	"apn_probe":  {},
	// モデムのリセット (AT+CFUN=0 → 1 → ifup wan)。30〜60 秒かかるので非同期。
	"modem_reset":        {},
	"modem_reset_status": {},
	// SMS。受信のみ。sms_status は本文を読まないので未読を既読にしない。
	"sms_list":     {},
	"sms_status":   {},
	"sms_import":   {},
	"sms_sims":     {},
	"sms_messages": {"iccid": "", "limit": 0},
}

func cmdRPCD(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: sbair-modem rpcd list|call <method>")
		return 2
	}
	switch args[0] {
	case "list":
		emit(methods)
		return 0
	case "call":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: sbair-modem rpcd call <method>")
			return 2
		}
		return rpcdCall(args[1])
	}
	fmt.Fprintf(os.Stderr, "sbair-modem rpcd: unknown verb %q\n", args[0])
	return 2
}

type rpcdArgs struct {
	ICCID            string `json:"iccid"`
	Mapping          int    `json:"mapping"`
	ActivationCode   string `json:"activation_code"`
	ConfirmationCode string `json:"confirmation_code"`
	On               bool   `json:"on"`
	APN              string `json:"apn"`
	Auth             string `json:"auth"`
	Username         string `json:"username"`
	Password         string `json:"password"`
	IPType           string `json:"iptype"`
	Label            string `json:"label"`
	Nickname         string `json:"nickname"`
	Limit            int    `json:"limit"`
}

// rpcdError keeps failures on stdout as JSON. rpcd treats a non-zero exit as
// a broken backend and gives LuCI nothing to display, so a method that fails
// for an ordinary reason - no eUICC, modem busy - still exits 0 and reports
// the reason in the payload.
func rpcdError(format string, a ...any) int {
	emit(map[string]any{"error": fmt.Sprintf(format, a...)})
	return 0
}

func rpcdCall(method string) int {
	if _, ok := methods[method]; !ok {
		return rpcdError("unknown method %q", method)
	}

	var in rpcdArgs
	// rpcd sends "{}" for a method with no arguments, and nothing at all when
	// invoked by hand. Both are fine; only malformed JSON is an error.
	if st, err := os.Stdin.Stat(); err == nil && st.Mode()&os.ModeCharDevice == 0 {
		dec := json.NewDecoder(os.Stdin)
		if err := dec.Decode(&in); err != nil && err.Error() != "EOF" {
			return rpcdError("bad arguments: %v", err)
		}
	}

	// These two must not touch AT. simmap_status is polled while the worker
	// holds the lock for the better part of a minute; making it wait on that
	// lock would mean the screen cannot show progress precisely when there is
	// progress to show.
	switch method {
	case "simmap_status":
		emit(readJob("simmap"))
		return 0
	case "esim_download_status":
		emit(readJob("download"))
		return 0
	case "simmap_set":
		emit(startSimmap(in.Mapping))
		return 0
	case "esim_download":
		emit(startDownload(in.ActivationCode, in.ConfirmationCode))
		return 0
	case "simlock_status":
		emit(readJob("simlock"))
		return 0
	case "simlock_set":
		emit(startSimlock(in.On))
		return 0
	case "modem_reset_status":
		emit(readJob("reset"))
		return 0
	case "modem_reset":
		emit(startModemReset())
		return 0
	// UCI だけを触るものも AT を開かない。
	case "apn_set":
		emit(apnSet(apnEntry{ICCID: in.ICCID, APN: in.APN, Auth: in.Auth,
			Username: in.Username, Password: in.Password,
			IPType: in.IPType, Label: in.Label}))
		return 0
	case "apn_delete":
		emit(apnDelete(in.ICCID))
		return 0
	// 保管庫を読むだけ。**AT を開かない**ので、画面を開いても未読は消えない。
	case "sms_sims":
		emit(smsSIMs())
		return 0
	case "sms_messages":
		emit(smsMessages(in.ICCID, in.Limit))
		return 0
	case "apn_probe":
		// モデムに聞くだけで AT は開かない。
		emit(apnProbe())
		return 0
	}

	ch := NewATChannel(*device)
	ch.SetTimeout(overviewTimeout)
	if err := ch.Connect(); err != nil {
		return rpcdError("モデムに繋がりません: %v", err)
	}
	defer ch.Disconnect()

	switch method {
	case "overview":
		emit(collectOverview(ch))
	case "simmap_get":
		emit(simMapping(ch))
	case "simlock_get":
		emit(simlockState(ch))
	case "sms_status":
		emit(smsStatus(ch))
	case "sms_list":
		// **70 通ぶんの PDU 行が返りうる。** overview の 4 秒では足りない。
		ch.SetTimeout(30 * time.Second)
		emit(smsList(ch))
	case "sms_import":
		ch.SetTimeout(30 * time.Second)
		emit(smsImport(ch))
	case "apn_status":
		emit(apnStatus(ch))
	case "apn_apply":
		emit(apnApply(ch))
	case "esim_status", "esim_list", "esim_enable", "esim_disable", "esim_delete",
		"esim_nickname":
		// **Everything that opens a logical channel gets the long timeout**,
		// esim_status included - it lists profiles too. A healthy card answers
		// in well under a second, but enable and disable make it REFRESH, and
		// it rejects AT+CCHO while re-initialising. That is precisely when the
		// screen re-reads, so the short status bound would turn a normal wait
		// into a failure.
		ch.SetTimeout(60 * time.Second)
		if method == "esim_status" {
			emit(esimStatus(ch))
		} else {
			emit(esimOp(ch, method, in.ICCID, in.Nickname))
		}
	}
	return 0
}

// simMapping reports which SIM the modem is currently wired to.
//
// 1 = the physical tray, 2 = the built-in eSIM. Only one is ever live.
func simMapping(ch *ATChannel) map[string]any {
	out := map[string]any{}
	lines, err := ch.Command("AT+ESIMMAP?")
	if err != nil {
		out["error"] = err.Error()
		return out
	}
	v, ok := First(lines, "+ESIMMAP:")
	if !ok {
		out["error"] = fmt.Sprintf("AT+ESIMMAP? に +ESIMMAP: がありません: %v", lines)
		return out
	}
	var n int
	fmt.Sscanf(v, "%d", &n)
	out["mapping"] = n
	switch n {
	case 1:
		out["label"] = "物理スロット (uSIM)"
	case 2:
		out["label"] = "内蔵 eSIM"
	default:
		out["label"] = fmt.Sprintf("不明 (%d)", n)
	}
	return out
}
