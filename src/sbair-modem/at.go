// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912

package main

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// cmdAT sends one AT command and prints the reply. It is the replacement for
// the standalone sbair-at C tool, and keeps its interface and exit codes so
// existing notes and scripts still read correctly:
//
//	sbair-modem at 'AT+CGMM'          body only (the final result code is dropped)
//	sbair-modem at -r 'AT+CPIN?'      raw, including the final result code
//	sbair-modem at -t 60 'AT+COPS=?'  timeout in seconds (default 10)
//
// Exit: 0 = OK / 1 = ERROR family / 2 = usage / 3 = connection or I/O failure.
//
// This subcommand never opens the eUICC. Poking at AT is the one thing that
// has to keep working when the card side is broken, which is exactly when it
// is needed.
func cmdAT(args []string) int {
	raw := false
	timeout := 10
	var cmd string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-r", "--raw":
			raw = true
		case "-t", "--timeout":
			if i+1 >= len(args) {
				return atUsage()
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n <= 0 {
				fmt.Fprintf(os.Stderr, "sbair-modem at: bad timeout %q\n", args[i+1])
				return 2
			}
			timeout = n
			i++
		default:
			if cmd != "" {
				return atUsage()
			}
			cmd = args[i]
		}
	}
	if cmd == "" {
		return atUsage()
	}

	ch := NewATChannel(*device)
	ch.SetTimeout(time.Duration(timeout) * time.Second)
	if err := ch.Connect(); err != nil {
		fmt.Fprintf(os.Stderr, "sbair-modem at: %v\n          (is atcid running?)\n", err)
		return 3
	}
	defer ch.Disconnect()

	lines, err := ch.Command(cmd)
	for _, l := range lines {
		fmt.Println(l)
	}
	if err != nil {
		// An ERROR family reply is a result, not a malfunction: the body (if
		// any) has been printed, and -r adds the final line. Anything else -
		// a timeout, a truncated reply, a dead socket - is a failure.
		if ch.lastFinal != "" && ch.lastFinal != "OK" {
			if raw {
				fmt.Println(ch.lastFinal)
			}
			return 1
		}
		fmt.Fprintf(os.Stderr, "sbair-modem at: %v\n", err)
		return 3
	}
	if raw {
		fmt.Println(ch.lastFinal)
	}
	return 0
}

func atUsage() int {
	fmt.Fprint(os.Stderr, `usage: sbair-modem at [-r] [-t <seconds>] '<AT command>'

  -r   raw: print the final result code as well
  -t   how long to wait for the final result code (default 10)

Exit: 0 = OK / 1 = ERROR family / 2 = usage / 3 = connection or I/O failure.
`)
	return 2
}
