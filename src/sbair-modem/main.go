// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912

// sbair-modem - modem and eSIM control for the SoftBank Air 6 / RG620T-SBK.
//
// The AT entry point, the rpcd backend behind the LuCI screens, the local ES10
// eUICC operations and the ES9+ download, over the shared AT layer in
// atchannel.go.
//
// Build (static, no libc dependency, for OpenWrt aarch64/musl):
//
//	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o sbair-modem .
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/damonto/euicc-go/lpa"
)

var (
	device  = flag.String("d", "/dev/adb_atci_socket", "AT device (unix socket or tty)")
	yes     = flag.Bool("y", false, "confirm a destructive operation")
	imei    = flag.String("imei", "", "IMEI to present to the SM-DP+")
	confirm = flag.String("cc", "", "confirmation code, when the profile requires one")
	// The standard GSMA ISD-R AID is the default; cards such as eSIM.me or
	// 5ber use their own, hence this escape hatch.
	aidHex  = flag.String("aid", "", "ISD-R AID in hex (default: standard GSMA ISD-R)")
	verbose = flag.Bool("v", false, "log the APDU exchange to stderr")
	// Each STORE DATA block becomes one AT+CGLA line of roughly
	// 2*(5+mss)+20 characters. atcid drops the connection when a line gets
	// too long, which shows up as an empty reply and EOF partway through a
	// profile download, so keep blocks well under its buffer.
	mss = flag.Int("mss", 120, "bytes per APDU block")
)

func emit(v any) {
	e := json.NewEncoder(os.Stdout)
	e.SetEscapeHTML(false)
	_ = e.Encode(v)
}

// release is set while the eUICC is open. os.Exit skips deferred calls, so
// every error path has to let go of the logical channel itself - otherwise it
// stays open on the card (channels outlive the AT session here) and AT+CCHO
// starts failing after a handful of leaks.
var release func()

func fail(format string, a ...any) {
	if release != nil {
		release()
	}
	emit(map[string]any{"error": fmt.Sprintf(format, a...)})
	os.Exit(1)
}

func main() {
	flag.Usage = usage
	flag.Parse()
	// Keep the library quiet: stdout must stay valid JSON for the caller.
	// -v sends its APDU trace to stderr, which leaves stdout parseable.
	logTo := io.Discard
	level := slog.LevelInfo
	if *verbose {
		logTo, level = os.Stderr, slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(logTo, &slog.HandlerOptions{Level: level})))

	// flag stops at the first non-flag argument, so "download CODE -cc 1234"
	// would leave the options unparsed. Re-scan the remainder so options may
	// follow the sub-command, which is how these are naturally typed.
	args := parseTrailingFlags(flag.Args())
	if len(args) == 0 {
		usage()
		os.Exit(1)
	}

	// These two never open the eUICC. `at` is the tool for poking at a modem
	// whose card side is broken, and `rpcd list` is called by rpcd on every
	// ACL enumeration - putting a logical channel on the card each time
	// somebody opens LuCI would be a slow way to exhaust them.
	switch args[0] {
	case "at":
		os.Exit(cmdAT(args[1:]))
	case "rpcd":
		os.Exit(cmdRPCD(args[1:]))
	case "simmap-worker":
		// Started detached by simmap_set. Not for people to run by hand.
		if len(args) < 2 {
			os.Exit(2)
		}
		var n int
		fmt.Sscanf(args[1], "%d", &n)
		os.Exit(runSimmapWorker(n))
	case "simlock-worker":
		if len(args) < 2 {
			os.Exit(2)
		}
		os.Exit(runSimlockWorker(args[1]))
	case "download-worker":
		if len(args) < 2 {
			os.Exit(2)
		}
		cc := ""
		if len(args) > 2 {
			cc = args[2]
		}
		os.Exit(runDownloadWorker(args[1], cc))
	}

	ch := NewATChannel(*device)
	ch.SetTimeout(60 * time.Second)
	if err := ch.Connect(); err != nil {
		fail("モデムに繋がりません: %v", err)
	}
	defer ch.Disconnect()

	switch args[0] {
	case "gc":
		// Reclaim logical channels leaked by a killed run. Cheap, and the
		// only way out of "the card looks like it is not an eUICC" short of
		// resetting it.
		ch.CloseAllChannels()
		emit(map[string]any{"result": "ok", "operation": "gc"})
		return
	case "status":
		emit(esimStatus(ch))
		return
	case "simmap":
		if len(args) < 2 {
			emit(simMapping(ch))
			return
		}
		var n int
		fmt.Sscanf(args[1], "%d", &n)
		ch.Disconnect() // ワーカーが lock を取れるように手放す
		emit(startSimmap(n))
		return
	case "overview":
		emit(collectOverview(ch))
		return
	case "apn":
		// 起動時の自動適用に使う (init.d/sbair-apn)。
		if len(args) > 1 && args[1] == "apply" {
			emit(apnApply(ch))
			return
		}
		if len(args) > 1 && args[1] == "probe" {
			emit(apnProbe())
			return
		}
		emit(apnStatus(ch))
		return
	case "simlock":
		if len(args) > 1 && (args[1] == "on" || args[1] == "off") {
			ch.Disconnect() // ワーカーが lock を取れるように手放す
			emit(startSimlock(args[1] == "on"))
			return
		}
		emit(simlockState(ch))
		return
	case "list":
		emit(esimOp(ch, "esim_list", ""))
		return
	case "enable", "disable", "delete":
		if len(args) < 2 {
			fail("usage: sbair-modem %s <ICCID> -y", args[0])
		}
		if !*yes {
			fail("%s refused: pass -y to confirm", args[0])
		}
		emit(esimOp(ch, "esim_"+args[0], args[1]))
		return
	}

	switch args[0] {
	case "download":
		// CLI では待って構わないので、ワーカーの中身をそのまま同期で回す。
		if len(args) < 2 {
			fail("usage: sbair-modem download <ACTIVATION-CODE|->")
		}
		code := args[1]
		if code == "-" {
			b, err := io.ReadAll(os.Stdin)
			if err != nil {
				fail("アクティベーションコードを読めません: %v", err)
			}
			code = strings.TrimSpace(string(b))
		}
		ch.Disconnect() // ワーカーと同じ経路を使うので lock を手放す
		runDownloadWorker(code, *confirm)
		emit(readJob("download"))
		return
	case "discovery":
		_, _, _, aid := inspectCard(ch)
		client, err := openEUICC(ch, aid)
		if err != nil {
			fail("eUICC を開けません: %v", err)
		}
		release = func() { client.Close(); release = nil }
		defer func() {
			if release != nil {
				release()
			}
		}()
		cmdDiscovery(client)
		return
	}
	usage()
	os.Exit(1)
}

func parseTrailingFlags(args []string) []string {
	var pos []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-y", "--yes":
			*yes = true
		case "-cc", "--cc":
			if i+1 < len(args) {
				*confirm = args[i+1]
				i++
			}
		case "-imei", "--imei":
			if i+1 < len(args) {
				*imei = args[i+1]
				i++
			}
		case "-d", "--d":
			if i+1 < len(args) {
				*device = args[i+1]
				i++
			}
		default:
			pos = append(pos, args[i])
		}
	}
	return pos
}

func usage() {
	fmt.Fprint(os.Stderr, `sbair-modem - modem and eSIM control for the SoftBank Air 6

  sbair-modem at [-r] [-t SEC] '<AT>'      send one AT command
  sbair-modem overview                     modem status, as JSON
  sbair-modem status                       SIM mapping and eUICC availability
  sbair-modem list                         profile list
  sbair-modem enable  <ICCID> -y           enable a profile
  sbair-modem disable <ICCID> -y           disable a profile
  sbair-modem delete  <ICCID> -y           delete a profile (irreversible)
  sbair-modem download <ACTIVATION-CODE|-> [-cc CODE] [-imei IMEI]
                                           install a profile (ES9+)
  sbair-modem discovery                    ES11 discovery (SM-DS)
  sbair-modem simmap [1|2]                 SIM mapping: show, or switch to
                                           1 = tray / 2 = built-in eSIM
  sbair-modem simlock [on|off]             SIM lock: show, or switch
  sbair-modem apn [apply|probe]            APN: show / apply stored / ask the SIM
  sbair-modem gc                           reclaim leaked logical channels
  sbair-modem rpcd list|call <method>      rpcd backend (called by rpcd)

The activation code is the full "LPA:1$smdp.example$MATCHINGID" string, or "-"
to read it from stdin. Output is JSON.

NB: on this device the built-in eSIM has no ISD-R. Everything eUICC-related
needs AT+ESIMMAP? to report 1, i.e. a card in the physical tray.
`)
}

func cmdDiscovery(client *lpa.Client) {
	entries, err := client.Discovery(nil, []byte(*imei))
	if err != nil {
		fail("discovery failed: %v", err)
	}
	emit(map[string]any{"entries": entries})
}
