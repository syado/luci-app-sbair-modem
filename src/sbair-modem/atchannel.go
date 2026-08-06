// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912

package main

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ATChannel is the single way into this modem. It talks to atcid's unix socket
// (/dev/adb_atci_socket) directly - no serial port, no socat pty. A character
// device works too.
//
// It doubles as a driver.SmartCardChannel for euicc-go, using the standard
// 3GPP TS 27.007 logical-channel commands (AT+CCHO / AT+CGLA / AT+CCHC).
//
// Device quirks this exists to absorb, all confirmed on hardware:
//
//   - Raw AT text, no length framing. Responses are \r\n separated.
//   - **A reply ends only at a final result code.** A gap in delivery means
//     nothing: AT+COPS=? goes quiet for tens of seconds mid-answer. Never treat
//     a pause, or EOF, as the end of a reply - let the timeout decide.
//   - **URCs interleave with command responses.** AT+ESIMMAP=2 answers
//     "+CNVRM: 0" and then "OK". Always match on the expected prefix; never
//     take a line by position or "the first non-empty line".
//   - atcid drops long-lived connections, especially while the modem is busy.
//     Logical channels outlive the AT session here, so reconnecting and
//     re-issuing is safe and invisible to the caller.
//   - **Two concurrent connections kill the first one.** rpcd spawns a process
//     per ubus call, so serialisation has to be cross-process: see lock().
//   - SELECT-by-AID through AT+CSIM is rejected even for an AID that EF_DIR
//     proves exists, so AID selection MUST go through AT+CCHO.
//   - AT+CCHO replies with a bare channel number ("1"), not "+CCHO: 1".
//   - The length argument of AT+CGLA counts hex characters, not bytes.
//   - Profile state changes answer SW=91xx ("normal ending, proactive command
//     pending") rather than 9000. That is success, but euicc-go's transmitter
//     only accepts 9000/61xx, so 91xx is normalised to 9000 here.
type ATChannel struct {
	path    string
	conn    io.ReadWriteCloser
	reader  *bufio.Reader
	channel byte
	timeout time.Duration
	lockFD  *os.File
	// lastFinal keeps the final result code of the most recent command, which
	// Command() otherwise swallows. Only the `at` subcommand's -r wants it.
	lastFinal string
}

// lockPath lives in /tmp because that is a tmpfs that always exists on
// OpenWrt. /var/lock does not on every image, and a lock file that cannot be
// created would leave this running unserialised - losing the one protection it
// exists for, with no visible symptom until two callers collide.
const lockPath = "/tmp/sbair-at.lock"

func NewATChannel(path string) *ATChannel {
	return &ATChannel{path: path, timeout: 30 * time.Second}
}

// SetTimeout bounds a single command. AT+COPS=? needs far more than the
// default; a status poll should not wait that long.
func (c *ATChannel) SetTimeout(d time.Duration) {
	if d > 0 {
		c.timeout = d
	}
}

// lock serialises access across processes.
//
// Opening a second connection to atcid kills the first, and rpcd runs the
// backend as a fresh process per ubus call - so two ubus calls arriving
// together are two processes and an in-process mutex would not help. The lock
// is held for the lifetime of the process; the kernel releases it on exit,
// including on a crash, so a killed run cannot wedge it.
func (c *ATChannel) lock() error {
	if c.lockFD != nil {
		return nil
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		// A read-only /var/lock is not a reason to refuse to talk to the
		// modem at all; carry on unserialised rather than fail closed.
		slog.Debug("[AT] cannot open lock file, continuing unserialised", "err", err)
		return nil
	}
	for i := 0; ; i++ {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			c.lockFD = f
			return nil
		}
		if i >= 100 { // 100 * 200ms = 20s
			f.Close()
			return fmt.Errorf("another sbair-modem is using the modem (%s)", lockPath)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// Connect is idempotent, and has to be: euicc-go's transmitter calls it when
// the LPA client is created, by which point a caller that wanted to run a few
// plain AT commands first is already connected. Dialling a second time would
// be the one thing this modem cannot survive - a second connection to atcid
// kills the first.
func (c *ATChannel) Connect() error {
	if c.conn != nil {
		return nil
	}
	if err := c.lock(); err != nil {
		return err
	}
	fi, err := os.Stat(c.path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", c.path, err)
	}
	if fi.Mode()&os.ModeSocket != 0 {
		conn, err := net.Dial("unix", c.path)
		if err != nil {
			return fmt.Errorf("dial %s: %w", c.path, err)
		}
		c.conn = conn
	} else {
		f, err := os.OpenFile(c.path, os.O_RDWR, 0)
		if err != nil {
			return fmt.Errorf("open %s: %w", c.path, err)
		}
		c.conn = f
	}
	c.reader = bufio.NewReader(c.conn)
	return nil
}

// Disconnect drops the socket but keeps the process-wide lock: a reconnect in
// the middle of an operation must not let another process slip in.
func (c *ATChannel) Disconnect() error {
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	c.reader = nil
	return err
}

// errTruncated marks a reply that ended without a final result code. It is
// deliberately distinct from a plain transport error, because retrying is only
// safe when nothing of the reply had been seen yet.
var errTruncated = errors.New("reply ended without a final result code")

// Command sends one AT command and returns the body lines, excluding the final
// result code.
func (c *ATChannel) Command(cmd string) ([]string, error) {
	lines, err := c.commandOnce(cmd)
	if err == nil || !isDisconnect(err) {
		return lines, err
	}
	slog.Debug("[AT] reconnecting after transport error", "err", err)
	_ = c.Disconnect()
	if err := c.Connect(); err != nil {
		return nil, fmt.Errorf("reconnect: %w", err)
	}
	return c.commandOnce(cmd)
}

func isDisconnect(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, net.ErrClosed)
}

// isFinal reports whether a line is a final result code.
//
// NB: AT+CCHO's success reply is a bare channel number with no "+CCHO:", so
// recognising the final line is the only reliable way to know a reply is over
// - it can never be done from a prefix on the body.
func isFinal(line string) (final, failed bool) {
	switch line {
	case "OK":
		return true, false
	case "ERROR", "BUSY", "NO CARRIER", "NO ANSWER", "NO DIALTONE":
		return true, true
	}
	if strings.HasPrefix(line, "+CME ERROR:") || strings.HasPrefix(line, "+CMS ERROR:") {
		return true, true
	}
	return false, false
}

func (c *ATChannel) commandOnce(cmd string) ([]string, error) {
	if c.conn == nil {
		return nil, errors.New("channel is not connected")
	}
	// One deadline for the whole exchange. A gap in delivery is not the end of
	// a reply on this modem, so the timeout is the only thing that may end it.
	if d, ok := c.conn.(net.Conn); ok {
		_ = d.SetDeadline(time.Now().Add(c.timeout))
	}
	if _, err := io.WriteString(c.conn, cmd+"\r"); err != nil {
		return nil, fmt.Errorf("write %q: %w", cmd, err)
	}
	slog.Debug("[AT] >", "cmd", cmd)

	var lines []string
	for {
		line, err := c.reader.ReadString('\n')
		if err != nil {
			// **Never hand back what we have as success.** Without a final
			// result code the reply may be cut in half, and half an answer is
			// worse than no answer - it looks like a modem that lost a field.
			slog.Debug("[AT] read error", "err", err, "collected", lines)
			if len(lines) == 0 && isDisconnect(err) {
				return nil, err // retryable: nothing of the reply was seen
			}
			return nil, fmt.Errorf("%q: %w (%v)", cmd, errTruncated, err)
		}
		line = strings.TrimRight(line, "\r\n")
		slog.Debug("[AT] <", "line", line)
		if line == "" {
			continue
		}
		if final, failed := isFinal(line); final {
			c.lastFinal = line
			if failed {
				return lines, fmt.Errorf("%s -> %s", cmd, line)
			}
			return lines, nil
		}
		lines = append(lines, line)
	}
}

// First returns the remainder of the first line carrying prefix.
//
// Always reach for this rather than indexing into the reply: URCs such as
// "+CNVRM: 0" arrive interleaved, so line position carries no meaning.
func First(lines []string, prefix string) (string, bool) {
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(l, prefix)), true
		}
	}
	return "", false
}

// Last returns the remainder of the *last* line carrying prefix.
//
// **Use this where a URC shares the prefix with the reply.** With +CEREG=2 the
// modem also sends unsolicited `+CEREG: <stat>,...` lines, which have no <n>
// field - one field fewer than the reply to `AT+CEREG?`. Reading a queued URC
// as the reply shifts every field by one and turns a TAC into a status. The
// reply to a query always arrives after whatever was already queued, so the
// last match is the one that answers the question.
func Last(lines []string, prefix string) (string, bool) {
	v, found := "", false
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			v, found = strings.TrimSpace(strings.TrimPrefix(l, prefix)), true
		}
	}
	return v, found
}

// IMEI asks the modem for its IMEI. SM-DP+ authentication requires one, and
// making the user type it in would be silly when the modem knows it.
func (c *ATChannel) IMEI() (string, error) {
	if c.conn == nil {
		if err := c.Connect(); err != nil {
			return "", err
		}
		defer c.Disconnect()
	}
	lines, err := c.Command("AT+CGSN")
	if err != nil {
		return "", err
	}
	for _, l := range lines {
		l = strings.TrimSpace(strings.TrimPrefix(l, "+CGSN:"))
		l = strings.Trim(l, "\"")
		if len(l) == 15 && strings.IndexFunc(l, func(r rune) bool { return r < '0' || r > '9' }) < 0 {
			return l, nil
		}
	}
	return "", fmt.Errorf("no IMEI in AT+CGSN reply: %v", lines)
}

func (c *ATChannel) OpenLogicalChannel(AID []byte) (byte, error) {
	lines, err := c.Command(fmt.Sprintf("AT+CCHO=%q", strings.ToUpper(hex.EncodeToString(AID))))
	if err != nil {
		return 0, err
	}
	// The success reply is a bare number. "+CCHO: n" is accepted too, in case
	// a firmware update ever starts spelling it the standard way.
	for _, l := range lines {
		l = strings.TrimSpace(strings.TrimPrefix(l, "+CCHO:"))
		if n, err := strconv.Atoi(strings.TrimSpace(l)); err == nil && n > 0 && n < 20 {
			c.channel = byte(n)
			return byte(n), nil
		}
	}
	return 0, fmt.Errorf("AT+CCHO gave no channel number: %v", lines)
}

func (c *ATChannel) CloseLogicalChannel(channel byte) error {
	_, err := c.Command(fmt.Sprintf("AT+CCHC=%d", channel))
	return err
}

// CloseAllChannels reclaims every logical channel, whoever opened them.
//
// The card allows five. Anything that leaks one - a killed process, a crashed
// download - eats into that, and once they are gone AT+CCHO simply fails and
// the card looks like it is not an eUICC at all.
//
// NB: AT+CCHC answers for any channel number the card supports, open or not,
// so the number of successes says nothing about how many were really in use.
func (c *ATChannel) CloseAllChannels() {
	for n := 1; n <= 5; n++ {
		_, _ = c.Command(fmt.Sprintf("AT+CCHC=%d", n))
	}
}

// Transmit sends one APDU and returns the complete reply with the status word
// appended.
//
// 61xx chaining is resolved here rather than left to euicc-go: this card only
// answers GET RESPONSE when the CLA carries the bare logical channel (01), and
// euicc-go issues it with its ES10 CLA (81), which returns 9000 and no data.
// A large AuthenticateServer reply would otherwise arrive truncated.
func (c *ATChannel) Transmit(command []byte) ([]byte, error) {
	var data []byte
	for {
		b, err := c.exchange(command)
		if err != nil {
			return nil, err
		}
		sw1, sw2 := b[len(b)-2], b[len(b)-1]
		data = append(data, b[:len(b)-2]...)

		switch {
		case sw1 == 0x61:
			// GET RESPONSE on the bare channel CLA, not the ES10 one.
			command = []byte{c.channel & 0x0f, 0xC0, 0x00, 0x00, sw2}
		case sw1 == 0x6C:
			// Wrong Le: repeat the original command with the length the card asked for.
			command = append(append([]byte{}, command[:len(command)-1]...), sw2)
			data = data[:0]
		case sw1 == 0x91:
			// Success with a proactive command (REFRESH) pending; upstream
			// only accepts 9000, so present it as plain success.
			return append(data, 0x90, 0x00), nil
		default:
			return append(data, sw1, sw2), nil
		}
	}
}

// exchange runs a single AT+CGLA round trip.
func (c *ATChannel) exchange(command []byte) ([]byte, error) {
	h := strings.ToUpper(hex.EncodeToString(command))
	lines, err := c.Command(fmt.Sprintf("AT+CGLA=%d,%d,%q", c.channel, len(h), h))
	if err != nil {
		return nil, err
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "+CGLA:") {
			continue
		}
		i, j := strings.Index(l, "\""), strings.LastIndex(l, "\"")
		if i < 0 || j <= i {
			continue
		}
		b, err := hex.DecodeString(l[i+1 : j])
		if err != nil {
			return nil, fmt.Errorf("bad hex in %q: %w", l, err)
		}
		if len(b) < 2 {
			return nil, fmt.Errorf("reply too short: %q", l)
		}
		return b, nil
	}
	return nil, fmt.Errorf("no +CGLA reply: %v", lines)
}
