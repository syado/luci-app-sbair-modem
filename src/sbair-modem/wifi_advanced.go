// SPDX-License-Identifier: MIT
// Copyright (c) 2026 syado
//
// Wi-Fi の追加機能(Phase 3、2026-08-10)。`knsh`本体の逆アセンブル(§docs/KNSH_COMMAND_AUDIT.md
// §6)と純正UI PHPソース(controllers/Wlan/*.php)の読み込みで裏取り済みのコマンドのみを使う。
// 値を渡す書き込み系は、実行するまで実機での挙動が未検証だったため、
// 全て「usage文字列 + PHPの実際の呼び出し箇所」の両方が一致するものだけを実装している。

package main

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
)

func knshOutput(args ...string) (string, error) {
	out, err := exec.Command("knsh", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// ensureKnosSection は`knos.<section>`が無ければ空のまま作る。
//
// 🔴 **実機で踏んだ罠(2026-08-10)**: この機体は`/etc/config/knos`が
// 0バイトで、`network`セクションが存在しない状態だった。`knsh`本体の
// `wlan bandsteering 1`のような書き込みは「成功しました」というメッセージを
// 返す(exit 0)のに、**セクションが無いと`uci_safe_set`が黙って何も書かない**
// (`uci changes knos`が空のまま、`uci -q get`も見えない)。セクションを
// 手で作ってから同じコマンドを打つと初めて反映される、と実機比較で確認済み。
// → 書き込み系を呼ぶ前に必ずこれで下地を作る。中身は空のセクションを
// 作るだけで、`knos_config`が本来入れる既定値(SSIDテンプレート等)には
// 一切触れない。
func ensureKnosSection(section string) {
	if _, err := uci("get", "knos."+section); err != nil {
		_, _ = uci("set", "knos."+section+"="+section)
		_, _ = uci("commit", "knos")
	}
}

// commitKnos / commitWireless は`knsh`の書き込みコマンドが`uci set`止まりで
// `commit`を呼ばないことを実機で確認したため、呼び出し側で明示的に行う。
// (`uci changes knos`に残ったままだと次の再起動で消える。)
func commitKnos()     { _, _ = uci("commit", "knos") }
func commitWireless() { _, _ = uci("commit", "wireless") }

// boolArg は "1"/"true" を書き込み値 "1" に、それ以外を "0" に正規化する。
func boolArg(v string) string {
	if v == "1" || v == "true" {
		return "1"
	}
	return "0"
}

// ---------------------------------------------------------------------------
// クライアント切断: `wlan disconnect client <mac>`
// PHPからの呼び出し箇所は無いが、usage文字列がそのまま1本のコマンドで
// 完結しており(§6-3)、副作用が「そのクライアントの再接続を要求する」
// だけなので恒久設定を壊すリスクが無い。
// ---------------------------------------------------------------------------

func clientDisconnect(mac string) map[string]any {
	if mac == "" {
		return map[string]any{"error": "mac is required"}
	}
	if out, err := knshOutput("wlan", "disconnect", "client", mac); err != nil {
		return map[string]any{"error": fmt.Sprintf("knsh wlan disconnect client: %v: %s", err, out)}
	}
	return map[string]any{"result": "ok"}
}

// ---------------------------------------------------------------------------
// Wi-Fi全体ON/OFF: `knsh wlan function set <0|1>` → knos.network.wlan_enabled
//
// 逆アセンブルで確認(§docs/KNSH_COMMAND_AUDIT.md §6、"wlan function"文字列への
// 参照から辿った)。`/lib/wifi/mtwifi.lua`はこのキーが**空(未設定)のときは
// 有効とみなす**(`wlan_enable = '1'`がデフォルト)。この機体は現在
// `/etc/config/knos`が空(2026-08-10 発見、原因不明)なので、空文字列は
// 「無効」ではなく「デフォルト値(有効)」として扱う。
// ---------------------------------------------------------------------------

func wifiEnabledStatus() map[string]any {
	v, _ := uci("get", "knos.network.wlan_enabled")
	return map[string]any{"enabled": v != "0"}
}

func wifiEnabledSet(on string) map[string]any {
	ensureKnosSection("network")
	val := boolArg(on)
	if out, err := knshOutput("wlan", "function", "set", val); err != nil {
		return map[string]any{"error": fmt.Sprintf("knsh wlan function set: %v: %s", err, out)}
	}
	commitKnos()
	return map[string]any{"result": "ok", "enabled": val == "1"}
}

// ---------------------------------------------------------------------------
// バンドステアリング: `knsh wlan bandsteering [<0|1>]` → knos.network.bandsteering
// (Wlan/Denpa.php:178 `system("knsh wlan bandsteering ".$value)` で実証済み)
// ---------------------------------------------------------------------------

var modeDigitRe = regexp.MustCompile(`:\s*(\d)`)

func bandsteeringStatus() map[string]any {
	out, err := knshOutput("wlan", "bandsteering")
	if err != nil {
		return map[string]any{"error": out}
	}
	m := modeDigitRe.FindStringSubmatch(out)
	return map[string]any{"enabled": len(m) > 1 && m[1] == "1"}
}

func bandsteeringSet(on string) map[string]any {
	ensureKnosSection("network")
	val := boolArg(on)
	if out, err := knshOutput("wlan", "bandsteering", val); err != nil {
		return map[string]any{"error": fmt.Sprintf("knsh wlan bandsteering: %v: %s", err, out)}
	}
	commitKnos()
	return map[string]any{"result": "ok", "enabled": val == "1"}
}

// ---------------------------------------------------------------------------
// 分離設定3種: `knsh wlan get/set {wlan2lan,intercommunication,ssid1to2} [<0|1>]`
// → knos.network.wlan_{wlan2lan,intercommunication,ssid1to2}
//
// `/usr/share/knos/isolate.include`(実機から直接読んで確認済み)による
// 実際の意味:
//   - wlan2lan: 0のとき「SSID1(メイン)」を含む全SSIDが有線LAN(eth0.1/.2)へ
//     出られなくなる。🔴 **このトグルはSSID1専用で、SSID2には効かない**
//     (`set_rule_wlan2lan()`がSSID2向けの0x102/0x202/0x302 DROPをこの値に関係なく
//     無条件で足すため)。SSID2が有線LANへ出られる状態自体は`sbair-netfix`が
//     常時ebtablesを監視して確保しており、こことは独立に解決済み・維持されている。
//   - intercommunication: 0のとき、SSID1同士の帯域をまたいだ通信
//     (2.4GHz-SSID1 ⇔ 5GHz-SSID1 等)を遮断する。
//   - ssid1to2: 0のとき、SSID1(メイン)とSSID2(ゲスト)の間の通信を遮断する
//     (いわゆるゲストネットワーク分離)。
//
// 値を書き込むと`/etc/config/knos`が更新されるが、実際にebtablesへ反映するには
// `/usr/share/knos/isolate.include`の再実行が要る。`knsh wlan set ...`自体は
// この再実行を行わない(実機で確認: 設定だけ変えても`ebtables -t nat -L
// postrouting_btnssid`のルール数が変わらなかった)。
//
// 🔴 **`/etc/init.d/firewall reload`では効かない**(実機で確認)。
// `uci show firewall`には`firewall.isolate=include`(path=isolate.include)が
// 登録されているのに、`firewall reload`のログには
// `Running script '.../knos/firewall.include'`は出るが**isolate.includeは
// 一度も呼ばれない**(fw3がこのinclude登録を拾っていない。原因未特定)。
// → 直接`sh /usr/share/knos/isolate.include`を実行する。中身は
// `ebtables -t nat -F`で一度flushしてから全ルールを再構築する自己完結スクリプトで、
// ip/ip6tables(ルーティング・フィルタ)には触れないため、Wi-Fi・WAN・LANの
// 通常の通信には影響しない。数瞬 ebtables のnatテーブルが空になる瞬間は
// あるので、`applyWifiRestart`と同じくバックグラウンドで起動して即座に
// 応答を返す(2026-08-10、XHRタイムアウト対策と同じ理由)。
// ---------------------------------------------------------------------------

var isolationKnshKey = map[string]string{
	"wlan2lan":           "wlan2lan",
	"intercommunication": "intercommunication",
	"ssid1to2":           "ssid1to2",
}

// isolationStatus は`knsh wlan get <key>`ではなく`uci get knos.network.wlan_<key>`を
// 直接読む。🔴 **実機で確認した食い違い**: この機体は`/etc/config/knos`が
// 空(§冒頭)で、その状態で`knsh wlan get wlan2lan`は独自のデフォルト値として
// "0"を返す。しかし実際にebtablesへ反映する`/usr/share/knos/isolate.include`は
// 生の`uci -q get`の結果が空文字なら`[ "$val" = "0" ]`が偽になり、
// **制限を追加しない(=許可されたまま)**。つまり`knsh get`の表示を信じると
// 「遮断中」と誤表示するが、実際の通信は許可されている。isolate.include
// 自身の判定条件と完全に同じロジックで読む。
var isolationUCIKey = map[string]string{
	"wlan2lan":           "knos.network.wlan_wlan2lan",
	"intercommunication": "knos.network.wlan_intercommunication",
	"ssid1to2":           "knos.network.wlan_ssid1to2",
}

func isolationStatus() map[string]any {
	out := map[string]any{}
	for name, key := range isolationUCIKey {
		v, _ := uci("get", key)
		out[name] = v != "0"
	}
	return out
}

func isolationSet(kind, on string) map[string]any {
	key, ok := isolationKnshKey[kind]
	if !ok {
		return map[string]any{"error": fmt.Sprintf("unknown isolation kind %q", kind)}
	}
	ensureKnosSection("network")
	val := boolArg(on)
	if out, err := knshOutput("wlan", "set", key, val); err != nil {
		return map[string]any{"error": fmt.Sprintf("knsh wlan set %s: %v: %s", key, err, out)}
	}
	commitKnos()

	// isolate.includeは`ebtables -t nat -F`から全ルールを再構築するため、
	// `sbair-netfix`が普段取り消し続けているSSID2の有線LAN遮断ルール(§wifi.go
	// reconcileSSID2LanBlock)も一緒に復活してしまう。常駐デーモンの次周期
	// (最大15秒)を待たず、ここで直後に同じ後始末を行う。
	cmd := exec.Command("sh", "-c", "sh /usr/share/knos/isolate.include; "+
		"mode=\"$(uci -q get dhcp.lan.ignore)\"; "+
		"if [ \"$mode\" = \"1\" ]; then "+
		"ebtables -t nat -D postrouting_wlan2lan --mark 0x102 -j DROP 2>/dev/null; "+
		"ebtables -t nat -D postrouting_wlan2lan --mark 0x202 -j DROP 2>/dev/null; "+
		"ebtables -t nat -D postrouting_wlan2lan --mark 0x302 -j DROP 2>/dev/null; "+
		"fi")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	_ = cmd.Start()

	return map[string]any{"result": "ok", "enabled": val == "1"}
}

// ---------------------------------------------------------------------------
// 11r (高速ローミング): `knsh wlan set 11r <0|1>` → wireless.<iface>.ieee80211r
// (usage文字列で確認済み。全SSIDに一括で効く単一トグル)
// ---------------------------------------------------------------------------

func dot11rStatus() map[string]any {
	v, _ := knshOutput("wlan", "get", "11r")
	return map[string]any{"enabled": strings.TrimSpace(v) != "0"}
}

func dot11rSet(on string) map[string]any {
	val := boolArg(on)
	if out, err := knshOutput("wlan", "set", "11r", val); err != nil {
		return map[string]any{"error": fmt.Sprintf("knsh wlan set 11r: %v: %s", err, out)}
	}
	commitWireless()
	return map[string]any{"result": "ok", "enabled": val == "1"}
}

// ---------------------------------------------------------------------------
// MACフィルタ: `knsh wlan filter mode [<0|1>]` / `filter list add <mac> <0|1>` /
// `filter list delete <mac>`
//
// 純正UI(Macfilter.php + macfilter.phtml、直接読んで確認済み)による意味:
//   - filter mode = 1: **許可リスト方式**を有効化する(「接続を許可する端末の
//     MACアドレス」ラベル)。0で無効(誰でも接続可)。拒否リストではない。
//   - filter list add <mac> <enable>: そのMACをリストに追加/更新する。
//     enableは「このエントリ自体を今有効にするか」(1=有効/0=無効、
//     リストに残したまま一時的に無効化できる)。filter modeが0なら
//     リストの中身に関わらず誰でも接続できる。
//
// リストの実体は`wireless.rax0.maclist`(空白区切り、各要素は
// "<index>,<enable>,<mac>"のCSV3項目。PHPが読む先に合わせてこの1本だけ見る)。
// 🔴 **実機で確認: フィールド順はPHPソースから読めなかった**(`Macfilter.php`は
// `$macSplit[2]`=MACしか使っていない)。`filter list add <mac> 1`を実行して
// 実際に書かれた値が`"0,1,<mac>"`だったことから逆算して確定した
// (先頭が自動採番されたindex=0、2番目が指定したenable=1)。
// ---------------------------------------------------------------------------

type macFilterEntry struct {
	MAC     string `json:"mac"`
	Enabled bool   `json:"enabled"`
	Index   string `json:"index"`
}

func macFilterStatus() map[string]any {
	modeOut, _ := knshOutput("wlan", "filter", "mode")
	m := modeDigitRe.FindStringSubmatch(modeOut)
	enabled := len(m) > 1 && m[1] == "1"

	raw, _ := uci("get", "wireless.rax0.maclist")
	var list []macFilterEntry
	if raw != "" {
		for _, tok := range strings.Fields(raw) {
			parts := strings.Split(tok, ",")
			if len(parts) < 3 {
				continue
			}
			list = append(list, macFilterEntry{
				Index:   parts[0],
				Enabled: parts[1] == "1",
				MAC:     parts[2],
			})
		}
	}
	return map[string]any{"enabled": enabled, "list": list}
}

func macFilterModeSet(on string) map[string]any {
	ensureKnosSection("network")
	val := boolArg(on)
	if out, err := knshOutput("wlan", "filter", "mode", val); err != nil {
		return map[string]any{"error": fmt.Sprintf("knsh wlan filter mode: %v: %s", err, out)}
	}
	commitKnos()
	commitWireless()
	return map[string]any{"result": "ok", "enabled": val == "1"}
}

func macFilterAdd(mac, enabled string) map[string]any {
	if mac == "" {
		return map[string]any{"error": "mac is required"}
	}
	ensureKnosSection("network")
	val := boolArg(enabled)
	if out, err := knshOutput("wlan", "filter", "list", "add", mac, val); err != nil {
		return map[string]any{"error": fmt.Sprintf("knsh wlan filter list add: %v: %s", err, out)}
	}
	commitKnos()
	commitWireless()
	return map[string]any{"result": "ok"}
}

func macFilterDelete(mac string) map[string]any {
	if mac == "" {
		return map[string]any{"error": "mac is required"}
	}
	if out, err := knshOutput("wlan", "filter", "list", "delete", mac); err != nil {
		return map[string]any{"error": fmt.Sprintf("knsh wlan filter list delete: %v: %s", err, out)}
	}
	commitKnos()
	commitWireless()
	return map[string]any{"result": "ok"}
}

// ---------------------------------------------------------------------------
// WPS: `knsh wlan wps status` / `run <2.4ghz|5ghz> <pbc|pin> [pin]` /
// `pin random` / `reset <2.4ghz|5ghz>`
//
// 帯域文字列は`strcasecmp`判定(§docs/KNSH_COMMAND_AUDIT.md §6-2)なので
// 大文字小文字は問わない。6GHzはWPS未対応(usage文字列に無い)。
// PBC(プッシュボタン)は実行すると**一定時間誰でも参加できる状態になる**ため、
// 画面側で必ず確認ダイアログを挟むこと。
// ---------------------------------------------------------------------------

var wpsBandArg = map[string]string{"2.4G": "2.4ghz", "5G": "5ghz"}

func wpsStatus() map[string]any {
	out, err := knshOutput("wlan", "wps", "status")
	if err != nil {
		return map[string]any{"error": out}
	}
	pin, _ := knshOutput("wlan", "wps", "pin")
	return map[string]any{"status": out, "pin": pin}
}

func wpsRun(band, mode, pin string) map[string]any {
	arg, ok := wpsBandArg[band]
	if !ok {
		return map[string]any{"error": fmt.Sprintf("WPS未対応の帯域です: %q(2.4G/5Gのみ)", band)}
	}
	args := []string{"wlan", "wps", "run", arg, mode}
	if mode == "pin" {
		if pin == "" {
			return map[string]any{"error": "PINが必要です"}
		}
		args = append(args, pin)
	}
	out, err := knshOutput(args...)
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("knsh wlan wps run: %v: %s", err, out)}
	}
	return map[string]any{"result": "ok", "output": out}
}

// wpsPinRandom は乱数PINを生成させたあと、`wlan wps pin`(引数無し)で読み直して
// 実際の値を返す。**`wps pin random`自体の標準出力にはPIN値が出ない**
// (実機で確認: 空文字を返すだけ)ため、生成後に別途読み戻す必要がある。
func wpsPinRandom() map[string]any {
	if out, err := knshOutput("wlan", "wps", "pin", "random"); err != nil {
		return map[string]any{"error": fmt.Sprintf("knsh wlan wps pin random: %v: %s", err, out)}
	}
	pin, err := knshOutput("wlan", "wps", "pin")
	if err != nil {
		return map[string]any{"result": "ok", "pin": ""}
	}
	return map[string]any{"result": "ok", "pin": pin}
}

func wpsReset(band string) map[string]any {
	arg, ok := wpsBandArg[band]
	if !ok {
		return map[string]any{"error": fmt.Sprintf("WPS未対応の帯域です: %q(2.4G/5Gのみ)", band)}
	}
	out, err := knshOutput("wlan", "wps", "reset", arg)
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("knsh wlan wps reset: %v: %s", err, out)}
	}
	return map[string]any{"result": "ok", "output": out}
}
