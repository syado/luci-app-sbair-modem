// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912

package main

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/damonto/euicc-go/bertlv"
	"github.com/damonto/euicc-go/lpa"
	sgp22 "github.com/damonto/euicc-go/v2"
)

// ES10 - the local eUICC operations.

// knownISDR lists the ISD-R AIDs worth trying, standard first.
//
// The GSMA one covers most cards; eSTK.me, eSIM.me and 5ber ship their own, and
// a card that answers none of them is simply not an eUICC.
var knownISDR = []string{
	"A0000005591010FFFFFFFF8900000100", // standard GSMA ISD-R
	"A06573746B6D65FFFF4953442D522030", // eSTK.me SE0
	"A06573746B6D65FFFF4953442D522031", // eSTK.me SE1
	"A0000005591010FFFFFFFF8900000177",
	"A0000005591010000000008900000300",
	"A0000005591010000000008900000200",
}

// cardKind is what is actually in the active SIM position.
const (
	cardNone  = "none"  // no card, or the modem cannot read one
	cardSIM   = "sim"   // a card that answers, but has no ISD-R
	cardEUICC = "euicc" // an eUICC
)

// probeISDR finds an ISD-R the card will open, and returns its AID.
//
// **A single failure means nothing.** The card allows five logical channels
// and they outlive the AT session, so a killed run leaves them open; once they
// are gone AT+CCHO fails for every AID and a perfectly good eUICC looks like an
// ordinary SIM. Reclaim them and try once more before concluding.
func probeISDR(ch *ATChannel) (string, bool) {
	try := func() (string, bool) {
		for _, aid := range knownISDR {
			raw, err := hex.DecodeString(aid)
			if err != nil {
				continue
			}
			if n, err := ch.OpenLogicalChannel(raw); err == nil {
				_ = ch.CloseLogicalChannel(n)
				return aid, true
			}
		}
		return "", false
	}
	if aid, ok := try(); ok {
		return aid, true
	}
	ch.CloseAllChannels()
	return try()
}

// inspectCard reports what is in the active SIM position without assuming it is
// an eUICC.
// msisdn reads the subscriber number. Many SIMs leave EF_MSISDN empty, so an
// absent number is normal, not a fault.
//
//	+CNUM: "","07000000000",128,0,4
func msisdn(ch *ATChannel) string {
	lines, err := ch.Command("AT+CNUM")
	if err != nil {
		return ""
	}
	v, ok := First(lines, "+CNUM:")
	if !ok {
		return ""
	}
	f := splitAT(v)
	if len(f) > 1 {
		return strings.TrimSpace(f[1])
	}
	return ""
}

func inspectCard(ch *ATChannel) (kind, pin, iccid, aid string) {
	if lines, err := ch.Command("AT+CPIN?"); err == nil {
		pin, _ = First(lines, "+CPIN:")
	}
	if lines, err := ch.Command("AT+CCID"); err == nil {
		if v, ok := First(lines, "+CCID:"); ok {
			iccid = strings.Trim(strings.TrimSpace(v), "\"")
		}
	}
	if pin == "" && iccid == "" {
		return cardNone, pin, iccid, ""
	}
	if a, ok := probeISDR(ch); ok {
		return cardEUICC, pin, iccid, a
	}
	return cardSIM, pin, iccid, ""
}

// openEUICC opens a logical channel to the ISD-R.
//
// The caller keeps ownership of ch: euicc-go's transmitter calls Connect() on
// it, which is idempotent, and Close() on the returned client releases the
// logical channel. Failing to release one leaks it until the card resets.
func openEUICC(ch *ATChannel, aid string) (*lpa.Client, error) {
	if *aidHex != "" {
		aid = strings.TrimSpace(*aidHex)
	}
	opts := &lpa.Options{Channel: ch, MSS: *mss}
	if aid != "" {
		raw, err := hex.DecodeString(aid)
		if err != nil {
			return nil, fmt.Errorf("bad AID %q: %v", aid, err)
		}
		opts.AID = raw
	}
	return lpa.New(opts)
}

// esimStatus answers "what is in the SIM position, and is there anything to
// manage" - and when there is, returns the profiles in the same payload.
//
// **The profiles come back here on purpose.** Finding any of this out means
// opening a logical channel, and the card only has five: a screen that called
// esim_status and then esim_list would open and release two per draw, and one
// failed release is a leak that stays until the card resets. One method, one
// channel.
func esimStatus(ch *ATChannel) map[string]any {
	out := simMapping(ch)
	if _, bad := out["error"]; bad {
		return out
	}
	mapping, _ := out["mapping"].(int)

	kind, pin, iccid, aid := inspectCard(ch)
	out["card"] = kind
	if pin != "" {
		out["sim_status"] = pin
	}
	if iccid != "" {
		out["iccid"] = iccid
	}
	if aid != "" {
		out["isdr_aid"] = aid
	}
	if n := msisdn(ch); n != "" {
		out["msisdn"] = n
	}

	if kind != cardEUICC {
		out["available"] = false
		switch {
		case kind == cardNone && mapping == 1:
			out["reason"] = "物理スロットにカードがありません。"
		case kind == cardNone:
			out["reason"] = "SIM を読めません。"
		case mapping == 2:
			// The built-in eSIM genuinely has no ISD-R. Not a failure - there
			// is simply no eUICC to talk to until the mapping is switched.
			out["reason"] = "内蔵 eSIM に ISD-R はありません。" +
				"eUICC を操作するには物理スロットの eUICC カードへ切り替えてください。"
		default:
			out["reason"] = "物理スロットのカードは eUICC ではありません" +
				"(通常の SIM)。既知の ISD-R AID はどれも開けませんでした。"
		}
		return out
	}

	client, err := openEUICC(ch, aid)
	if err != nil {
		out["available"] = false
		out["reason"] = fmt.Sprintf("eUICC を開けません: %v", err)
		return out
	}
	defer client.Close()

	out["available"] = true
	eid, err := client.EID()
	if err != nil {
		out["available"] = false
		out["reason"] = fmt.Sprintf("EID を読めません: %v", err)
		return out
	}
	out["eid"] = strings.ToUpper(hex.EncodeToString(eid))

	// EUICCInfo2 comes back as raw BER-TLV; tag 82 inside it is the SGP.22
	// version the card implements.
	if v, err := client.EUICCInfo2(); err == nil && v != nil {
		if svn := v.First(bertlv.Universal.Primitive(2)); svn != nil && len(svn.Value) == 3 {
			out["svn"] = fmt.Sprintf("%d.%d.%d", svn.Value[0], svn.Value[1], svn.Value[2])
		}
	}

	// A profile listing that fails does not make the card unavailable, so it
	// is reported alongside rather than replacing everything above.
	if profiles, err := client.ListProfile(nil, nil); err != nil {
		out["profiles_error"] = fmt.Sprintf("profile を読めません: %v", err)
	} else {
		list := make([]profileJSON, 0, len(profiles))
		for _, p := range profiles {
			list = append(list, toJSON(p))
		}
		out["profiles"] = list
	}
	return out
}

// esimOp runs one profile operation and reports the result as a plain map, so
// the same code serves both the rpcd backend and the CLI subcommands.
//
// nickname は付随の引数。使うのは esim_nickname だけ。
func esimOp(ch *ATChannel, method, iccid string, extra ...string) map[string]any {
	// Refuse early when there is no eUICC, rather than letting it fail deep
	// inside as an opaque +CME ERROR: 100.
	kind, _, _, aid := inspectCard(ch)
	if kind != cardEUICC {
		m := simMapping(ch)
		n, _ := m["mapping"].(int)
		if n == 2 {
			return map[string]any{"error": "eUICC がありません" +
				"(SIM マッピングが内蔵 eSIM で、内蔵 eSIM に ISD-R はありません)。"}
		}
		if kind == cardNone {
			return map[string]any{"error": "物理スロットにカードがありません。"}
		}
		return map[string]any{"error": "物理スロットのカードは eUICC ではありません(通常の SIM)。"}
	}

	client, err := openEUICC(ch, aid)
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("eUICC を開けません: %v", err)}
	}
	defer client.Close()

	if method == "esim_list" {
		profiles, err := client.ListProfile(nil, nil)
		if err != nil {
			return map[string]any{"error": fmt.Sprintf("profile を読めません: %v", err)}
		}
		list := make([]profileJSON, 0, len(profiles))
		for _, p := range profiles {
			list = append(list, toJSON(p))
		}
		return map[string]any{"profiles": list}
	}

	// **空を弾く。** 空文字は NewICCID を素通りし、カード側で
	// "undefined error" になって原因が分からなくなる。
	if strings.TrimSpace(iccid) == "" {
		return map[string]any{"error": "ICCID が指定されていません。"}
	}
	id, err := sgp22.NewICCID(iccid)
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("ICCID %q が不正です: %v", iccid, err)}
	}
	switch method {
	case "esim_enable":
		err = client.EnableProfile(id, true)
	case "esim_disable":
		err = client.DisableProfile(id, true)
	case "esim_delete":
		err = client.DeleteProfile(id)
	case "esim_nickname":
		// **空文字は「名前を消す」。** SGP.22 はそれを許すので、
		// 未指定と区別せずそのまま渡す。
		nick := ""
		if len(extra) > 0 {
			nick = extra[0]
		}
		err = client.SetNickname(id, nick)
	default:
		return map[string]any{"error": fmt.Sprintf("unknown operation %q", method)}
	}
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("%s に失敗しました: %v", method, err)}
	}
	// Enable and disable make the card REFRESH, and it rejects AT+CCHO while
	// it re-initialises - so the screen should re-read after a short pause,
	// not immediately. Delete と nickname は REFRESH を起こさない。
	refresh := method == "esim_enable" || method == "esim_disable"
	return map[string]any{"result": "ok", "operation": method, "iccid": iccid,
		"refresh_pending": refresh}
}

type profileJSON struct {
	ICCID    string `json:"iccid"`
	ISDPAid  string `json:"isdp_aid"`
	State    string `json:"state"`
	Class    string `json:"class"`
	Nickname string `json:"nickname"`
	Provider string `json:"provider"`
	Name     string `json:"name"`
}

func toJSON(p *sgp22.ProfileInfo) profileJSON {
	state := "disabled"
	if p.ProfileState == sgp22.ProfileEnabled {
		state = "enabled"
	}
	class := map[sgp22.ProfileClass]string{
		sgp22.ProfileClassTest:         "test",
		sgp22.ProfileClassProvisioning: "provisioning",
		sgp22.ProfileClassOperational:  "operational",
	}[p.ProfileClass]
	return profileJSON{
		ICCID:    p.ICCID.String(),
		ISDPAid:  strings.ToUpper(hex.EncodeToString(p.ISDPAID)),
		State:    state,
		Class:    class,
		Nickname: p.ProfileNickname,
		Provider: p.ServiceProviderName,
		Name:     p.ProfileName,
	}
}
