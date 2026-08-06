// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// APN の保存と適用。

const (
	apnConfig = "sbair"
	wanIface  = "network.wan"
)

// sectionName turns an ICCID into a UCI section name. UCI names allow only
// [A-Za-z0-9_], and an ICCID can carry a trailing 'f' padding nibble.
var notUCIName = regexp.MustCompile(`[^A-Za-z0-9_]`)

func sectionName(iccid string) string {
	return "s" + notUCIName.ReplaceAllString(strings.TrimSpace(iccid), "")
}

// uci runs the uci command and returns **stdout only**.
//
// **CombinedOutput would be wrong here.** `uci get` on a missing option prints
// "uci: Entry not found" on stderr and exits non-zero; folding that into the
// value makes an unset option read back as that message - and then writing it
// somewhere else stores the error text as configuration. That is exactly what
// put `iptype='uci: Entry not found'` into network.wan.
func uci(args ...string) (string, error) {
	out, err := exec.Command("uci", args...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ensureConfig creates /etc/config/sbair when it is missing.
//
// **`uci set` fails outright if the config file does not exist**, which would
// make the very first APN anyone saves fail with an opaque error.
func ensureConfig() error {
	path := "/etc/config/" + apnConfig
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return os.WriteFile(path, []byte("# APN, keyed by ICCID. Managed by sbair-modem.\n"), 0644)
}

type apnEntry struct {
	ICCID    string `json:"iccid"`
	APN      string `json:"apn"`
	Auth     string `json:"auth"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	IPType   string `json:"iptype,omitempty"`
	Label    string `json:"label,omitempty"`
}

func apnGet(iccid string) (apnEntry, bool) {
	sec := apnConfig + "." + sectionName(iccid)
	if v, err := uci("get", sec); err != nil || v == "" {
		return apnEntry{}, false
	}
	get := func(k string) string { v, _ := uci("get", sec+"."+k); return v }
	return apnEntry{
		ICCID: iccid, APN: get("apn"), Auth: get("auth"),
		Username: get("username"), Password: get("password"),
		IPType: get("iptype"), Label: get("label"),
	}, true
}

func apnList() []apnEntry {
	out, err := uci("show", apnConfig)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var list []apnEntry
	for _, l := range strings.Split(out, "\n") {
		// sbair.s8981....iccid='8981...'
		if !strings.Contains(l, ".iccid=") {
			continue
		}
		v := strings.Trim(l[strings.Index(l, "=")+1:], "'\"")
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		if e, ok := apnGet(v); ok {
			list = append(list, e)
		}
	}
	return list
}

func apnSet(e apnEntry) map[string]any {
	if strings.TrimSpace(e.ICCID) == "" {
		return map[string]any{"error": "ICCID が指定されていません。"}
	}
	if strings.TrimSpace(e.APN) == "" {
		return map[string]any{"error": "APN が指定されていません。"}
	}
	if err := ensureConfig(); err != nil {
		return map[string]any{"error": fmt.Sprintf("/etc/config/%s を作れません: %v", apnConfig, err)}
	}
	sec := apnConfig + "." + sectionName(e.ICCID)
	if _, err := uci("set", sec+"=apn"); err != nil {
		return map[string]any{"error": fmt.Sprintf("uci に書けません: %v", err)}
	}
	for k, v := range map[string]string{
		"iccid": e.ICCID, "apn": e.APN, "auth": e.Auth,
		"username": e.Username, "password": e.Password,
		"iptype": e.IPType, "label": e.Label,
	} {
		if v == "" {
			_, _ = uci("delete", sec+"."+k)
			continue
		}
		if _, err := uci("set", sec+"."+k+"="+v); err != nil {
			return map[string]any{"error": fmt.Sprintf("uci set %s: %v", k, err)}
		}
	}
	if _, err := uci("commit", apnConfig); err != nil {
		return map[string]any{"error": fmt.Sprintf("uci commit: %v", err)}
	}
	return map[string]any{"result": "ok", "iccid": e.ICCID}
}

func apnDelete(iccid string) map[string]any {
	if strings.TrimSpace(iccid) == "" {
		return map[string]any{"error": "ICCID が指定されていません。"}
	}
	if _, err := uci("delete", apnConfig+"."+sectionName(iccid)); err != nil {
		return map[string]any{"error": fmt.Sprintf("uci delete: %v", err)}
	}
	if _, err := uci("commit", apnConfig); err != nil {
		return map[string]any{"error": fmt.Sprintf("uci commit: %v", err)}
	}
	return map[string]any{"result": "ok", "iccid": iccid}
}

// apnProbe asks the SIM what APN it wants.
//
// **ベンダの `ql_datacall --apn_provision_by_sim` がそのまま答える。**
// 事業者の DB を持つのはモデム側で、こちらが MCC/MNC の表を抱える必要はない。
// 返る JSON のキーは netifd の proto が使うものと同じ対応:
//
//	apn → apn / auth_type → auth / user → username
//	password → password / protocol → iptype
//
// **提案するだけで、保存も適用もしない。** 事業者から降ってきた値が
// その契約で正しいとは限らないので、確認してもらってから入れる。
func apnProbe() map[string]any {
	out, err := exec.Command("ql_datacall", "--apn_provision_by_sim").Output()
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("SIM から読み出せません: %v", err)}
	}
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		return map[string]any{"error": fmt.Sprintf("応答を解釈できません: %v", err)}
	}

	str := func(k string) string {
		switch v := raw[k].(type) {
		case string:
			return v
		case float64:
			return strconv.Itoa(int(v))
		}
		return ""
	}
	e := apnEntry{
		APN: str("apn"), Auth: str("auth_type"),
		Username: str("user"), Password: str("password"),
		IPType: str("protocol"),
	}
	if e.APN == "" {
		return map[string]any{"error": "SIM が APN を返しませんでした。"}
	}
	return map[string]any{"suggestion": e, "plmn": str("plmn")}
}

// currentICCID reads the ICCID of whatever SIM is live right now.
func currentICCID(ch *ATChannel) string {
	lines, err := ch.Command("AT+CCID")
	if err != nil {
		return ""
	}
	if v, ok := First(lines, "+CCID:"); ok {
		return strings.Trim(strings.TrimSpace(v), "\"")
	}
	return ""
}

// apnStatus reports what is stored, what is live, and whether they agree.
func apnStatus(ch *ATChannel) map[string]any {
	iccid := currentICCID(ch)
	out := map[string]any{"iccid": iccid, "entries": apnList()}

	live := map[string]string{}
	for _, k := range []string{"apn", "auth", "username", "iptype"} {
		if v, err := uci("get", wanIface+"."+k); err == nil {
			live[k] = v
		}
	}
	out["wan"] = live

	if e, ok := apnGet(iccid); ok {
		out["entry"] = e
		out["applied"] = live["apn"] == e.APN
	} else {
		out["applied"] = false
	}
	return out
}

// applyToLTE mirrors the entry into /etc/config/lte.
//
// **これをやらないと再起動のたびに APN が出荷時の値へ戻る。** ベンダの
// `/usr/bin/knsh` が起動時に
//
//	lte.<mode>.apn           -> network.wan.apn
//	lte.<mode>.apn_auth_type -> network.wan.auth
//	lte.<mode>.apn_userid    -> network.wan.username
//	lte.<mode>.apn_passwd    -> network.wan.password
//
// を流し込む。**network.wan だけ直しても上書きされる**(実機で
// 起動 48 秒後に `sbair-apn` が正しい値を書き、その 4 秒後に knsh が
// SoftBank の `artemis.air` へ戻すのを観測した)。ベンダの仕組みと
// 戦わず、参照元の方に正しい値を置く。
//
// mode は 5g / lte / backup / test。**どれが選ばれるかは knsh 側で決まる**
// ので全部そろえる。存在しない mode は uci が黙って弾くので害は無い。
//
// 失敗しても致命的ではない(次の起動で戻るだけ)ので、呼び出し側は
// 続行してよい。
func applyToLTE(e apnEntry) {
	if _, err := uci("get", "lte"); err != nil {
		return // この機体には無い設定。何もしない。
	}
	auth := e.Auth
	if auth == "" {
		auth = "0"
	}
	for _, mode := range []string{"5g", "lte", "backup", "test"} {
		sec := "lte." + mode
		if _, err := uci("get", sec); err != nil {
			continue
		}
		_, _ = uci("set", sec+".apn="+e.APN)
		_, _ = uci("set", sec+".apn_auth_type="+auth)
		_, _ = uci("set", sec+".apn_userid="+e.Username)
		_, _ = uci("set", sec+".apn_passwd="+e.Password)
	}
	_, _ = uci("commit", "lte")
}

// apnApply writes the entry for the live SIM into network.wan and restarts it.
//
// **何も設定が無いときは触らない。** 空の APN を書き込むと、ベンダの
// `check_auto_apn_prov`(SIM から APN を引く仕組み)まで潰しかねない。
func apnApply(ch *ATChannel) map[string]any {
	iccid := currentICCID(ch)
	if iccid == "" {
		return map[string]any{"error": "SIM の ICCID を読めません。"}
	}
	e, ok := apnGet(iccid)
	if !ok {
		return map[string]any{"result": "skipped", "iccid": iccid,
			"note": "この SIM の APN は登録されていません。WAN は変更していません。"}
	}

	set := func(k, v string) {
		if v == "" {
			_, _ = uci("delete", wanIface+"."+k)
			return
		}
		_, _ = uci("set", wanIface+"."+k+"="+v)
	}
	set("apn", e.APN)
	set("auth", e.Auth)
	set("username", e.Username)
	set("password", e.Password)
	set("iptype", e.IPType)

	// **auto_conf は必ず 0 にする。** 1 のままだと netifd の proto が
	// `check_auto_apn_prov` を呼ぶが、その関数のループは**データが欠けて
	// いるときにしか retry を減らさない**:
	//
	//	retry=2
	//	while [ "$retry" -gt 0 ]; do
	//	    apn_data=`ql_datacall --apn_provision_by_sim`
	//	    ...
	//	    if [ -z "$db_user" ] || [ -z "$db_password" ] || [ -z "$db_auth_type" ]; then
	//	        let retry--
	//	    fi
	//	    sleep 1
	//	done          # ← 成功時に break が無い
	//
	// SIM が完全な APN を返すと**永久に抜けない**。実機で au の SIM が
	// user/password/auth_type を全部返し、WAN の setup がここで止まって
	// `starting connection` に到達しない。
	// こちらで APN を管理する以上、その仕組みは使わない。
	_, _ = uci("set", wanIface+".auto_conf=0")

	if _, err := uci("commit", "network"); err != nil {
		return map[string]any{"error": fmt.Sprintf("uci commit network: %v", err)}
	}

	// **/etc/config/lte にも同じ値を入れる。** ここを直さないと再起動で戻る。
	applyToLTE(e)

	// ifup は netifd に proto を回し直させる。reload だけだと
	// ql_datacall がセッションを張り直さないことがある。
	if out, err := exec.Command("ifup", "wan").CombinedOutput(); err != nil {
		return map[string]any{"error": fmt.Sprintf("ifup wan: %v: %s",
			err, strings.TrimSpace(string(out)))}
	}
	return map[string]any{"result": "ok", "iccid": iccid, "apn": e.APN,
		"note": "WAN を張り直しました。接続まで 10〜30 秒かかります。"}
}
