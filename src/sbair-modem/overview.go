// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912

package main

import (
	"strconv"
	"strings"
	"time"
)

// Overview is everything the status screen shows, gathered in one AT session.
//
// It is deliberately one coarse structure rather than a field per ubus method:
// rpcd forks the backend per call, and every call is one process, one flock
// and one connection to atcid. Splitting "rsrp" and "rsrq" into two methods
// would double all of that for no gain.
//
// **Only standard 3GPP commands are used, because the cell-level vendor ones
// do not exist here.** The modem answers ATI with "Quectel / RG620T-SBK", but
// the firmware underneath is MediaTek (AT+CGMR reports MOLY.NR16.R2.MD800),
// and every cell-info extension either family defines was measured on hardware
// to return +CME ERROR: 4 (not supported):
//
//	AT+QENG="servingcell"  AT+QCSQ  AT+QNWINFO  AT+QCAINFO   (Quectel)
//	AT+EEMGINFO?  AT+ECELLINFO  AT+SGCELLINFOEX?  AT+BMTCELLINFO
//	AT+GTCCINFO?  AT+XLEC?  AT+CPSI?  AT^HCSQ?  AT+MTSM=
//
// So there is no band, no PCI and no cell-level report to be had. What works
// is +CSQ, +CESQ, +CREG/+CEREG/+CGREG/+C5GREG, +COPS and +CSCON.
//
// It is not a clean sweep though - AT+QTEMP (27 sensors) and AT+QUIMSLOT? do
// answer. **Measure a vendor command one at a time; never write off a family.**
//
// Every field is best-effort: a modem that does not answer one probe must
// still produce a usable screen, so a failed probe leaves its fields empty
// instead of failing the call.
type Overview struct {
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model,omitempty"`
	Revision     string `json:"revision,omitempty"`
	IMEI         string `json:"imei,omitempty"`

	SIMStatus  string `json:"sim_status,omitempty"`  // +CPIN?
	SIMMapping int    `json:"sim_mapping,omitempty"` // 1 = uSIM (tray), 2 = built-in eSIM
	ICCID      string `json:"iccid,omitempty"`
	IMSI       string `json:"imsi,omitempty"`

	Registration string `json:"registration,omitempty"` // human-readable stat
	Registered   bool   `json:"registered"`
	RegDomain    string `json:"reg_domain,omitempty"` // which of CEREG/C5GREG/CREG answered
	Operator     string `json:"operator,omitempty"`
	AccessTech   string `json:"access_tech,omitempty"`
	TAC          string `json:"tac,omitempty"`
	CellID       string `json:"cell_id,omitempty"`

	RSSIdBm string `json:"rssi_dbm,omitempty"`
	RSRPdBm string `json:"rsrp_dbm,omitempty"`
	RSRQdB  string `json:"rsrq_db,omitempty"`

	// SignalNote explains an empty signal panel when the modem answered but
	// the standard fields were "unknown".
	SignalNote string `json:"signal_note,omitempty"`

	// CESQ is the raw +CESQ line. This modem answers with more fields than
	// TS 27.007 defines, and only the standard ones are decoded above, so the
	// raw line is kept rather than guessed at.
	CESQ string `json:"cesq,omitempty"`

	// Temperatures is what AT+QTEMP reports. It is one of the two vendor
	// commands this modem does implement.
	Temperatures []Temperature `json:"temperatures,omitempty"`

	// Errors records probes that failed, so a half-empty screen can be told
	// apart from a modem that is genuinely idle.
	Errors []string `json:"errors,omitempty"`
}

// Temperature is one sensor from AT+QTEMP.
type Temperature struct {
	Sensor  string `json:"sensor"`
	Celsius string `json:"celsius"`
}

// regStat maps the stat field shared by +CREG/+CEREG/+CGREG/+C5GREG.
// 3GPP TS 27.007 §7.2.
var regStat = map[string]string{
	"0": "未登録(検索していない)",
	"1": "登録済み",
	"2": "未登録(検索中)",
	"3": "登録拒否",
	"4": "不明",
	"5": "登録済み(ローミング)",
}

// accessTech maps the access technology field shared by +COPS and the
// registration commands.
var accessTech = map[string]string{
	"0": "GSM", "2": "UTRAN", "3": "GSM/EGPRS", "4": "UTRAN/HSDPA",
	"5": "UTRAN/HSUPA", "6": "UTRAN/HSDPA+HSUPA", "7": "LTE",
	"10": "LTE Cat-M1", "11": "NB-IoT", "12": "LTE (5G NSA)", "13": "NR5G",
}

func collectOverview(ch *ATChannel) *Overview {
	o := &Overview{}

	// **One budget for the whole call, not per probe.** The screen polls every
	// 15s and each poll is a process that waits on the flock, so a wedged
	// modem answering a dozen probes at the per-command timeout would take
	// longer than the poll interval and the processes would pile up behind
	// each other. Once the budget is gone the rest are skipped and recorded.
	deadline := time.Now().Add(overviewBudget)

	ask := func(cmd string) []string {
		if time.Now().After(deadline) {
			o.Errors = append(o.Errors, cmd+": 時間切れのため実行していません")
			return nil
		}
		lines, err := ch.Command(cmd)
		if err != nil {
			o.Errors = append(o.Errors, cmd+": "+err.Error())
			return nil
		}
		return lines
	}

	// value returns the payload of a reply, whether or not the modem echoes a
	// prefix. AT+CGMM answers with a bare "RG620T-SBK" while AT+CGMR answers
	// "+CGMR: MOLY..." on this firmware - both forms have to work, and a
	// stray URC must be skipped either way.
	value := func(lines []string, prefix string) string {
		if v, ok := First(lines, prefix); ok {
			return v
		}
		for _, l := range lines {
			if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "+") {
				return l
			}
		}
		return ""
	}

	o.Manufacturer = value(ask("AT+CGMI"), "+CGMI:")
	o.Model = value(ask("AT+CGMM"), "+CGMM:")
	o.Revision = strings.TrimPrefix(value(ask("AT+CGMR"), "+CGMR:"), "Revision:")
	o.IMEI = value(ask("AT+CGSN"), "+CGSN:")

	o.SIMStatus = value(ask("AT+CPIN?"), "+CPIN:")
	if v, ok := First(ask("AT+ESIMMAP?"), "+ESIMMAP:"); ok {
		o.SIMMapping, _ = strconv.Atoi(strings.TrimSpace(v))
	}
	o.ICCID = strings.Trim(value(ask("AT+CCID"), "+CCID:"), "\"")
	// A locked SIM answers +CME ERROR: 14 here; that is a state, not a fault,
	// so it lands in Errors and the field simply stays empty.
	o.IMSI = value(ask("AT+CIMI"), "+CIMI:")

	collectRegistration(o, ask)
	collectSignal(o, ask)
	collectTemperature(o, ask)
	return o
}

// collectTemperature reads AT+QTEMP.
//
// The cell-info vendor commands are all absent on this modem, but this one is
// not - it answers with 27 sensors, one per line:
//
//	+QTEMP:"soc_max","66.9","0"
func collectTemperature(o *Overview, ask func(string) []string) {
	for _, l := range ask("AT+QTEMP") {
		v, ok := strings.CutPrefix(l, "+QTEMP:")
		if !ok {
			continue
		}
		f := splitAT(v)
		if len(f) < 2 || f[0] == "" || f[1] == "" {
			continue
		}
		o.Temperatures = append(o.Temperatures, Temperature{Sensor: f[0], Celsius: f[1]})
	}
}

// collectRegistration asks every registration domain and picks the most
// informative answer.
//
// **Picking the first domain that answers is wrong.** All four answer whether
// or not they are attached, +C5GREG is asked first, and on this modem it comes
// back as a bare `+C5GREG: 2,0` while +CEREG carries the location:
//
//	+C5GREG: 2,0
//	+CEREG:  2,0,"****","********",7
//	+CGREG:  2,0,"0000","00000000",0,"00"     <- placeholder zeros
//	+CREG:   2,0,"0000","00*******",7
//
// So the answers are scored: attached beats carrying a location, which beats
// merely answering. Ties keep the earlier (more specific) domain.
func collectRegistration(o *Overview, ask func(string) []string) {
	type domain struct{ cmd, set, prefix, label string }
	domains := []domain{
		// **ドメイン名を RAT で名付けないこと。** +CEREG は AcT=13 (NR) も
		// 報告するので「LTE」と呼ぶと、5G NSA のときに接続方式 NR5G と
		// 食い違う。ここはレジスタの名前を出す。
		{"AT+C5GREG?", "AT+C5GREG=2", "+C5GREG:", "5GS (SA)"},
		{"AT+CEREG?", "AT+CEREG=2", "+CEREG:", "EPS (LTE / 5G NSA)"},
		{"AT+CGREG?", "AT+CGREG=2", "+CGREG:", "パケット"},
		{"AT+CREG?", "AT+CREG=2", "+CREG:", "回線交換"},
	}

	type answer struct {
		label string
		f     []string
		score int
	}
	var best *answer

	for _, d := range domains {
		// URC が同じプレフィクスで割り込むので、最後の行を採る。
		v, ok := Last(ask(d.cmd), d.prefix)
		if !ok {
			continue
		}
		// <n>,<stat>[,"<tac>","<ci>"[,<AcT>]]
		f := splitAT(v)
		if len(f) < 2 {
			continue
		}

		// **<n> が 2 でないと TAC も Cell ID も返ってこない。** 既定は 0 で、
		// そのときの応答は `+CEREG: 0,0` の 2 フィールドだけ。2 にすると
		// 照会形にも位置情報が付く。一度打てば済むが、**モデムが再初期化
		// されると戻る**(CFUN の往復や SIM マッピングの切替で消える)ので、
		// 毎回 <n> を見て必要なときだけ設定し直す。
		if f[0] != "2" {
			ask(d.set)
			if v2, ok2 := Last(ask(d.cmd), d.prefix); ok2 {
				if f2 := splitAT(v2); len(f2) >= 2 {
					f = f2
				}
			}
		}

		score := 0
		if f[1] == "1" || f[1] == "5" {
			score = 2
		} else if hasLocation(f) {
			score = 1
		}
		if best == nil || score > best.score {
			a := answer{label: d.label, f: f, score: score}
			best = &a
		}
		if score == 2 {
			break
		}
	}

	if best == nil {
		return
	}
	f := best.f
	o.Registration = regStat[f[1]]
	if o.Registration == "" {
		o.Registration = "stat=" + f[1]
	}
	o.Registered = best.score == 2
	// **Only name the domain when it actually registered.** Every domain
	// answers whether or not it is attached, so naming the one that merely
	// answered first would put "5G" on the screen of a modem attached to
	// nothing.
	if o.Registered {
		o.RegDomain = best.label
	}
	if hasLocation(f) {
		o.TAC, o.CellID = f[2], f[3]
	}
	if len(f) > 4 {
		o.AccessTech = accessTech[f[4]]
	}

	// +COPS: <mode>,<format>,"<operator>",<AcT>
	//
	// **Do not trust its AcT while unregistered.** With no service this modem
	// answers `+COPS: 0,255,"",0`, and reading that 0 as an access technology
	// puts "GSM" on the screen of a 5G router that is not attached to
	// anything.
	if v, ok := First(ask("AT+COPS?"), "+COPS:"); ok {
		f := splitAT(v)
		if len(f) > 2 && f[2] != "" {
			o.Operator = f[2]
			if len(f) > 3 {
				if s, hit := accessTech[f[3]]; hit && o.AccessTech == "" {
					o.AccessTech = s
				}
			}
		}
	}
}

// hasLocation reports whether a registration reply carries a usable TAC and
// cell id. **All-zero fields are placeholders, not values** - +CGREG answers
// `"0000","00000000"` when it has nothing, and showing that as a cell is worse
// than showing nothing.
func hasLocation(f []string) bool {
	if len(f) < 4 {
		return false
	}
	real := func(s string) bool {
		s = strings.TrimSpace(s)
		return s != "" && strings.Trim(s, "0") != ""
	}
	return real(f[2]) && real(f[3])
}

// collectSignal reads the two signal commands this modem does implement.
func collectSignal(o *Overview, ask func(string) []string) {
	// +CSQ: <rssi>,<ber>; rssi 0..31 maps to -113..-51 dBm, 99 = unknown.
	if v, ok := First(ask("AT+CSQ"), "+CSQ:"); ok {
		f := splitAT(v)
		if len(f) > 0 {
			if n, err := strconv.Atoi(f[0]); err == nil && n >= 0 && n <= 31 {
				o.RSSIdBm = strconv.Itoa(-113 + 2*n)
			}
		}
	}

	// +CESQ: <rxlev>,<ber>,<rscp>,<ecno>,<rsrq>,<rsrp>  (TS 27.007 §8.69)
	//
	// This modem appends three more fields than the spec defines. Their
	// meaning has not been confirmed on hardware, so only the standard six
	// are decoded and the whole line is passed through as `cesq`.
	v, ok := First(ask("AT+CESQ"), "+CESQ:")
	if !ok {
		return
	}
	o.CESQ = v
	f := splitAT(v)
	if len(f) > 4 {
		// 0 = below range, 1..33 = -19.5 dB in 0.5 steps, 34 = -3 or better,
		// 255 = unknown.
		if n, err := strconv.Atoi(f[4]); err == nil && n >= 1 && n <= 34 {
			o.RSRQdB = strconv.FormatFloat(-20+float64(n)*0.5, 'f', -1, 64)
		}
	}
	if len(f) > 5 {
		// 0 = below range, 1..96 = -140 dBm in 1 dB steps, 97 = -44 or
		// better, 255 = unknown.
		if n, err := strconv.Atoi(f[5]); err == nil && n >= 1 && n <= 97 {
			o.RSRPdBm = strconv.Itoa(-141 + n)
		}
	}

	// **The LTE fields go 255 (unknown) at times while the three extra fields
	// carry values.** Their scale has not been measured, so decoding them
	// would be publishing guessed dBm - worse than an empty field. Say so
	// instead, and let the raw line above carry the numbers.
	if o.RSRPdBm == "" && o.RSRQdB == "" && len(f) > 8 {
		o.SignalNote = "標準フィールドが「不明」を返しています。" +
			"規格外の追加フィールドには値がありますが、単位が未確認のため表示しません(生の行を参照)。"
	}
}

// splitAT splits an AT parameter list on commas and strips the quotes that
// some fields carry. Values here never contain a comma, so a plain split is
// enough and avoids dragging in a CSV reader.
func splitAT(s string) []string {
	parts := strings.Split(s, ",")
	for i, p := range parts {
		parts[i] = strings.Trim(strings.TrimSpace(p), "\"")
	}
	return parts
}

const (
	// overviewTimeout bounds a single probe.
	overviewTimeout = 4 * time.Second
	// overviewBudget bounds the whole call, and has to stay below the screen's
	// poll interval so a slow modem cannot make polls overlap.
	overviewBudget = 10 * time.Second
)
