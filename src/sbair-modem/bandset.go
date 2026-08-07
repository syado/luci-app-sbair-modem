// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912

package main

import (
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// バンドの選択 — `AT+EPBSEH=` で有効バンドを書き換える。
//
//   - **値を変えると数秒だけネットワークから切れる。** 再スキャンが走るため。
//     放っておいても戻るが、だからこの操作は非同期にしてある
//   - 効果は即時。`AT+CFUN` の往復は要らない
//   - **同じ値を書き戻すのは無害** — 構文の確認に使える
//
// ⚠ **管理経路はこのモデムではない。** 画面と SSH は LAN 側なので、
// バンドを間違えて WAN が落ちても操作は続けられる。**それでも自動で
// 巻き戻す** — 「適用したら戻せなくなった」を作らないため。
//
// 書式と実測は sbair6-rs の docs/AT.md「バンドを変える」。
const (
	bandPoll     = 3 * time.Second
	bandPollMax  = 45 * time.Second
	bandSettle   = 2 * time.Second
	bandPrevPath = "sbair.band.previous"
)

// bandMaskHex builds a mask of the same hex width as the one it replaces.
//
// **幅を保つのが要点。** LTE は 16 桁 (64 ビット)、NR は 24 桁 (96 ビット) で
// 返ってくる。詰めた桁数が変わると語の切れ目がずれ、**まったく別のバンドを
// 書き込む**ことになる。並びは `bandMask` の逆で、32 ビット語ごとの
// リトルエンディアン。
func bandMaskHex(bands []int, width int) (string, error) {
	words := width / 8
	if words == 0 {
		return "", fmt.Errorf("マスクの幅が不正です (%d)", width)
	}
	w := make([]uint32, words)
	for _, b := range bands {
		if b < 1 {
			return "", fmt.Errorf("バンド番号が不正です: %d", b)
		}
		i := (b - 1) / 32
		if i >= words {
			return "", fmt.Errorf("バンド %d はこのマスクに入りません", b)
		}
		w[i] |= 1 << uint((b-1)%32)
	}
	var sb strings.Builder
	for _, v := range w {
		fmt.Fprintf(&sb, "%08X", v)
	}
	return sb.String(), nil
}

// epbsehFields reads +EPBSEH and returns its four masks verbatim.
// **生のまま返す。** 書き戻すときに幅と桁を保つのに要る。
func epbsehFields(ch *ATChannel, cmd string) ([]string, error) {
	lines, err := ch.Command(cmd)
	if err != nil {
		return nil, err
	}
	v, ok := Last(lines, "+EPBSEH:")
	if !ok {
		return nil, fmt.Errorf("%s に +EPBSEH: がありません", cmd)
	}
	f := splitAT(v)
	if len(f) < 4 {
		return nil, fmt.Errorf("%s の応答が短すぎます: %s", cmd, v)
	}
	return f[:4], nil
}

// writeEPBSEH sends the four masks.
func writeEPBSEH(ch *ATChannel, f []string) error {
	q := make([]string, len(f))
	for i, s := range f {
		q[i] = `"` + s + `"`
	}
	_, err := ch.Command("AT+EPBSEH=" + strings.Join(q, ","))
	return err
}

// parseBandList turns "1,41,42" into a sorted, de-duplicated slice.
func parseBandList(s string) ([]int, error) {
	var out []int
	seen := map[int]bool{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("バンド番号として読めません: %q", p)
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out, nil
}

func startBandSet(lte, nr string) map[string]any {
	out := startJob("band", "set", lte, nr)
	if _, bad := out["error"]; !bad {
		out["note"] = "適用すると数秒だけ電波が切れます。戻らなければ自動で元の設定に巻き戻します。"
	}
	return out
}

// runBandWorker applies a new band selection, then makes sure the modem came
// back. **戻らなければ書き戻す。**
func runBandWorker(lteArg, nrArg string) int {
	j := newJob("band")
	j.write()

	wantLTE, err := parseBandList(lteArg)
	if err != nil {
		return j.fail("入力", err.Error())
	}
	wantNR, err := parseBandList(nrArg)
	if err != nil {
		return j.fail("入力", err.Error())
	}
	// **LTE を空にさせない。** この機体は 5G NSA なので LTE のアンカーが
	// 要る。空にすると 5G ごと落ちて、巻き戻すまで圏外になる。
	if len(wantLTE) == 0 {
		return j.fail("入力", "LTE を 1 つ以上選んでください。5G は LTE に繋いでから足す方式なので、LTE を空にすると 5G も繋がりません。")
	}

	ch := NewATChannel(*device)
	ch.SetTimeout(30 * time.Second)
	if err := ch.Connect(); err != nil {
		return j.fail("接続", err.Error())
	}
	defer ch.Disconnect()

	j.step("現在の設定を読む")
	cur, err := epbsehFields(ch, "AT+EPBSEH?")
	if err != nil {
		return j.fail("AT+EPBSEH?", err.Error())
	}
	cap, err := epbsehFields(ch, "AT+EPBSEH=?")
	if err != nil {
		return j.fail("AT+EPBSEH=?", err.Error())
	}

	// **対応していないバンドは弾く。** モデムが黙って無視する保証が無い。
	supLTE, supNR := bandMask(cap[2]), bandMask(cap[3])
	if bad := notIn(wantLTE, supLTE); len(bad) > 0 {
		return j.fail("入力", fmt.Sprintf("対応していない LTE バンドです: %v (対応 %v)", bad, supLTE))
	}
	if bad := notIn(wantNR, supNR); len(bad) > 0 {
		return j.fail("入力", fmt.Sprintf("対応していない 5G バンドです: %v (対応 %v)", bad, supNR))
	}

	next := []string{cur[0], cur[1], "", ""}
	if next[2], err = bandMaskHex(wantLTE, len(cur[2])); err != nil {
		return j.fail("入力", err.Error())
	}
	if next[3], err = bandMaskHex(wantNR, len(cur[3])); err != nil {
		return j.fail("入力", err.Error())
	}

	// 巻き戻し先を uci に残す。**/tmp のジョブファイルは再起動で消える。**
	// **セクションを先に作る** — 無いと `uci set` が丸ごと失敗する。
	if ensureConfig() == nil {
		_, _ = uci("set", "sbair.band=band")
		_, _ = uci("set", bandPrevPath+"="+strings.Join(cur, " "))
		_, _ = uci("commit", "sbair")
	}

	j.step("バンドを書き込む")
	if err := writeEPBSEH(ch, next); err != nil {
		return j.fail("AT+EPBSEH=", err.Error())
	}
	time.Sleep(bandSettle)

	j.step("ネットワークへの登録を待つ")
	if waitRegistered(ch, j) {
		// **設定はモデムの電源が切れると消える**(実測: 再起動で出荷既定に
		// 戻る)。起動時に入れ直せるよう、望んだ組み合わせを残す。
		saveDesiredBands(wantLTE, wantNR)
		return finishBandSet(ch, j, next, "バンドを変更しました。")
	}

	// 戻らなかった。**元に戻して、戻ったことまで確かめる。**
	j.step("戻らないので元の設定へ巻き戻す")
	if err := writeEPBSEH(ch, cur); err != nil {
		return j.fail("巻き戻し", "元の設定に戻せませんでした: "+err.Error()+
			" 手動で戻してください: AT+EPBSEH=\""+strings.Join(cur, "\",\"")+"\"")
	}
	time.Sleep(bandSettle)
	if !waitRegistered(ch, j) {
		_, _ = exec.Command("ifup", "wan").CombinedOutput()
		return j.fail("巻き戻し",
			"指定したバンドではつながらず、元の設定に戻しましたが、まだネットワークにつながっていません。"+
				"しばらく待つか、モデムをリセットしてください。")
	}
	finishBandSet(ch, j, cur, "")
	return j.fail("登録", "指定したバンドではネットワークにつながらなかったので、元の設定に巻き戻しました。")
}

// finishBandSet brings the WAN back and records what is now in effect.
func finishBandSet(ch *ATChannel, j *job, applied []string, msg string) int {
	j.step("WAN を張り直す")
	// **AT を握ったまま ifup しない。** その先で ql_datacall が AT を使う。
	ch.Disconnect()
	if out, err := exec.Command("ifup", "wan").CombinedOutput(); err != nil {
		return j.fail("ifup wan", fmt.Sprintf("%v: %s", err, strings.TrimSpace(string(out))))
	}
	if msg == "" {
		return 0
	}
	lte, nr := bandMask(applied[2]), bandMask(applied[3])
	return j.done("完了", fmt.Sprintf("%s LTE %v / 5G %v。接続まで 10〜30 秒かかります。", msg, lte, nr))
}

// waitRegistered polls +CEREG until the modem is attached again.
func waitRegistered(ch *ATChannel, j *job) bool {
	for waited := time.Duration(0); waited < bandPollMax; waited += bandPoll {
		time.Sleep(bandPoll)
		lines, err := ch.Command("AT+CEREG?")
		if err != nil {
			continue
		}
		// **`Last` で取る。** URC は `<n>` を持たないのでフィールドがずれる。
		v, ok := Last(lines, "+CEREG:")
		if !ok {
			continue
		}
		f := splitAT(v)
		if len(f) >= 2 && (f[1] == "1" || f[1] == "5") {
			return true
		}
	}
	return false
}

// notIn returns the members of want that are absent from have.
func notIn(want, have []int) []int {
	in := map[int]bool{}
	for _, h := range have {
		in[h] = true
	}
	var bad []int
	for _, w := range want {
		if !in[w] {
			bad = append(bad, w)
		}
	}
	return bad
}

// bandPrevious reports the mask stored before the last change, so the screen
// can offer to go back to it.
func bandPrevious() []string {
	v, err := uci("get", bandPrevPath)
	if err != nil {
		return nil
	}
	f := strings.Fields(strings.TrimSpace(v))
	if len(f) != 4 {
		return nil
	}
	return f
}

// intsToList renders a band list the way uci stores it.
func intsToList(a []int) string {
	s := make([]string, len(a))
	for i, n := range a {
		s[i] = strconv.Itoa(n)
	}
	return strings.Join(s, ",")
}

func saveDesiredBands(lte, nr []int) {
	if ensureConfig() != nil {
		return
	}
	_, _ = uci("set", "sbair.band=band")
	_, _ = uci("set", "sbair.band.lte="+intsToList(lte))
	_, _ = uci("set", "sbair.band.nr="+intsToList(nr))
	_, _ = uci("commit", "sbair")
}

// desiredBands reads the selection to restore at boot.
func desiredBands() (lte, nr []int, ok bool) {
	v, err := uci("get", "sbair.band.lte")
	if err != nil || strings.TrimSpace(v) == "" {
		return nil, nil, false
	}
	if lte, err = parseBandList(v); err != nil || len(lte) == 0 {
		return nil, nil, false
	}
	if v, err = uci("get", "sbair.band.nr"); err == nil {
		nr, _ = parseBandList(v)
	}
	return lte, nr, true
}

// ensureBands restores the stored band selection.
//
// ⚠ **`AT+EPBSEH=` は再起動をまたがない。** 書いた値は電源が切れると
// 出荷既定に戻る。SIM ロックや IMS と同じように、**起動のたびに入れ直す**。
//
// `slow` が false のときは書かない — 画面からの APN 適用のたびに電波を
// 落とすわけにはいかないので、食い違いを注記で返すだけにする。
func ensureBands(ch *ATChannel, slow bool) []string {
	wantLTE, wantNR, ok := desiredBands()
	if !ok {
		return nil
	}
	cur, err := epbsehFields(ch, "AT+EPBSEH?")
	if err != nil {
		return []string{"バンドの現在値を読めません: " + err.Error()}
	}
	if sameInts(bandMask(cur[2]), wantLTE) && sameInts(bandMask(cur[3]), wantNR) {
		return nil // もう望みどおり。触らない。
	}
	if !slow {
		return []string{fmt.Sprintf("バンドの設定が保存した組み合わせ (LTE %v / 5G %v) と違います。"+
			"電波の画面から適用し直してください。", wantLTE, wantNR)}
	}

	next := []string{cur[0], cur[1], "", ""}
	if next[2], err = bandMaskHex(wantLTE, len(cur[2])); err != nil {
		return []string{"バンドを組み立てられません: " + err.Error()}
	}
	if next[3], err = bandMaskHex(wantNR, len(cur[3])); err != nil {
		return []string{"バンドを組み立てられません: " + err.Error()}
	}
	if err := writeEPBSEH(ch, next); err != nil {
		return []string{"バンドを書き込めません: " + err.Error()}
	}
	return []string{fmt.Sprintf("バンドを入れ直しました (LTE %v / 5G %v)。", wantLTE, wantNR)}
}

const (
	bandSettlePoll   = 5 * time.Second
	bandSettleStable = 45 * time.Second  // これだけ保てば落ち着いたとみなす
	bandSettleMax    = 120 * time.Second // 見張る上限
)

// settleBands keeps the stored band selection in place while the modem
// finishes coming up.
//
// ⚠ **モデムは OS より遅れて初期化をやり直し、そのときバンドが出荷既定へ
// 戻る。** つまり **`sbair-modem boot` の中で 1 回書くだけでは足りない** —
// 起動直後に書いた値は十数秒後に消される。消されたら書き直し、
// しばらく保ったら抜ける。実測した時刻は sbair6-rs の docs/AT.md。
//
// **これは boot の最後でだけ回す。** 頭に置くと APN と WAN がこの時間ぶん
// 待たされる。init スクリプトが setsid で切り離しているので、
// ここで待っても起動そのものは止まらない。
//
// ⚠ **この間 AT の flock を握り続ける**(1 分前後、最悪 2 分)。起動直後に
// 画面を開くと `another sbair-modem is using the modem` が出る。
// **バンドを 1 度も選んでいなければ即座に戻る**ので、使っていない機体は
// この代償を払わない。
func settleBands(ch *ATChannel) {
	wantLTE, wantNR, ok := desiredBands()
	if !ok {
		return
	}
	rewrites := 0
	stable := time.Duration(0)
	for waited := time.Duration(0); waited < bandSettleMax; waited += bandSettlePoll {
		time.Sleep(bandSettlePoll)
		cur, err := epbsehFields(ch, "AT+EPBSEH?")
		if err != nil {
			stable = 0
			continue
		}
		if sameInts(bandMask(cur[2]), wantLTE) && sameInts(bandMask(cur[3]), wantNR) {
			if stable += bandSettlePoll; stable >= bandSettleStable {
				break
			}
			continue
		}
		stable = 0
		next := []string{cur[0], cur[1], "", ""}
		if next[2], err = bandMaskHex(wantLTE, len(cur[2])); err != nil {
			return
		}
		if next[3], err = bandMaskHex(wantNR, len(cur[3])); err != nil {
			return
		}
		if writeEPBSEH(ch, next) == nil {
			rewrites++
		}
	}
	if rewrites > 0 {
		// **消されて書き直したときだけ言う。** 毎回出すと boot のログが
		// 「何かした」で埋まる。
		emit(map[string]any{"result": "band_settled", "rewrites": rewrites,
			"lte": wantLTE, "nr": wantNR})
	}
}
