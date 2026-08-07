// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912

package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// 受信 SMS の保管。SIM (ICCID) ごとに分けて持つ。
//
// **モデムに置いたままにできない理由が 2 つある。**
//
//  1. 保存領域は 70 通で頭打ち。溢れれば消える
//  2. `AT+CMGL` が未読を既読に変えてしまう (→ docs/API.md)。**最初に取り込んだ
//     ときの未読状態は、そのとき記録しないと二度と分からない**
//
// なので取り込みは `INSERT OR IGNORE`。同じメッセージを読み直しても
// **未読の記録を後から書き換えない**。

// **/data ではなく overlay に置く。** /data はベンダのログ置き場
// (`/data/knos`, `/data/mdlog`) で、初期化の対象でもある。overlay 側は
// `/etc/config/sbair` は再起動をまたいで残る。
// 70 通ぶんの本文は数十 KB で、容量は問題にならない。
const smsDBPath = "/etc/sbair/sms.db"

// 純 Go の SQLite (modernc.org/sqlite) を使う。**CGO は使えない** —
// この機体向けは CGO_ENABLED=0 の静的ビルドが前提で、`libsqlite3.so` に
// 動的リンクするとその前提が崩れる。バイナリは 6.1 MB → 10.4 MB になる。
func openSMSDB() (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(smsDBPath), 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", smsDBPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	schema := []string{
		`CREATE TABLE IF NOT EXISTS sim (
			iccid      TEXT PRIMARY KEY,
			number     TEXT,
			label      TEXT,
			first_seen INTEGER,
			last_seen  INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS message (
			id        INTEGER PRIMARY KEY,
			iccid     TEXT NOT NULL,
			sender    TEXT,
			received  INTEGER,
			tz_offset INTEGER,
			body      TEXT,
			unread    INTEGER,
			parts     INTEGER,
			missing   TEXT,
			hash      TEXT NOT NULL UNIQUE,
			imported  INTEGER,
			raw       TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS message_by_sim ON message(iccid, received DESC)`,
	}
	for _, q := range schema {
		if _, err := db.Exec(q); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", strings.SplitN(q, "(", 2)[0], err)
		}
	}
	// 既に作られている DB への追加。**列が既にあればエラーになるので捨てる。**
	_, _ = db.Exec(`ALTER TABLE message ADD COLUMN raw TEXT`)
	return db, nil
}

// smsImport reads the modem and stores whatever is new.
//
// **これだけがモデムを読む。** 画面を開くだけでは走らせない — 読むと未読が
// 消えるので、取り込みは明示の操作にする。
func smsImport(ch *ATChannel) map[string]any {
	iccid := currentICCID(ch)
	if iccid == "" {
		return map[string]any{"error": "SIM の ICCID を読めません。"}
	}
	number := currentNumber(ch)

	msgs, bad, err := collectSMS(ch)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}

	db, err := openSMSDB()
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("データベースを開けません: %v", err)}
	}
	defer db.Close()

	now := time.Now().Unix()
	if _, err := db.Exec(`INSERT INTO sim(iccid, number, first_seen, last_seen)
		VALUES(?,?,?,?)
		ON CONFLICT(iccid) DO UPDATE SET
			last_seen = excluded.last_seen,
			number    = COALESCE(NULLIF(excluded.number,''), sim.number)`,
		iccid, number, now, now); err != nil {
		return map[string]any{"error": fmt.Sprintf("SIM を記録できません: %v", err)}
	}

	added := 0
	for _, m := range msgs {
		_, off := m.when.Zone()
		var missing []string
		for _, n := range m.Missing {
			missing = append(missing, strconv.Itoa(n))
		}
		// **INSERT OR IGNORE。** 2 回目以降の取り込みで unread を上書きしない。
		// 消したものは `deleted` に載っているので入れ直さない。
		res, err := db.Exec(`INSERT OR IGNORE INTO message
			(iccid, sender, received, tz_offset, body, unread, parts, missing, hash, imported, raw)
			VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			iccid, m.From, m.when.Unix(), off, m.Text, boolInt(m.Unread),
			m.Parts, strings.Join(missing, ","), m.Hash, now,
			strings.Join(m.raw, "|"))
		if err != nil {
			return map[string]any{"error": fmt.Sprintf("保存できません: %v", err)}
		}
		if n, _ := res.RowsAffected(); n > 0 {
			added++
		}
	}
	out := map[string]any{"result": "ok", "iccid": iccid, "number": number,
		"read": len(msgs), "added": added}
	if len(bad) > 0 {
		out["errors"] = bad // 解けなかった PDU は黙って落とさない
	}
	return out
}

// smsSIMs lists the SIMs the database knows about.
//
// **番号が空でも必ず出す。** EF_MSISDN を持たない profile は珍しくなく、
// 番号でしか選べない作りにすると、その SIM のメッセージに手が届かなくなる。
func smsSIMs() map[string]any {
	db, err := openSMSDB()
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("データベースを開けません: %v", err)}
	}
	defer db.Close()

	rows, err := db.Query(`SELECT s.iccid, COALESCE(s.number,''), COALESCE(s.label,''),
		COALESCE(s.last_seen,0),
		(SELECT COUNT(*) FROM message m WHERE m.iccid = s.iccid),
		(SELECT COUNT(*) FROM message m WHERE m.iccid = s.iccid AND m.unread = 1)
		FROM sim s ORDER BY s.last_seen DESC`)
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("読み出せません: %v", err)}
	}
	defer rows.Close()

	sims := []map[string]any{}
	for rows.Next() {
		var iccid, number, label string
		var lastSeen int64
		var count, unread int
		if err := rows.Scan(&iccid, &number, &label, &lastSeen, &count, &unread); err != nil {
			continue
		}
		sims = append(sims, map[string]any{
			"iccid": iccid, "number": number, "label": label,
			"last_seen": lastSeen, "count": count, "unread": unread,
		})
	}
	return map[string]any{"sims": sims}
}

// smsMessages returns the stored messages of one SIM, newest first.
func smsMessages(iccid string, limit int) map[string]any {
	if strings.TrimSpace(iccid) == "" {
		return map[string]any{"error": "ICCID が指定されていません。"}
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	db, err := openSMSDB()
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("データベースを開けません: %v", err)}
	}
	defer db.Close()

	rows, err := db.Query(`SELECT sender, received, tz_offset, body, unread, parts, missing, hash
		FROM message WHERE iccid = ? ORDER BY received DESC, id DESC LIMIT ?`, iccid, limit)
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("読み出せません: %v", err)}
	}
	defer rows.Close()

	out := []map[string]any{}
	for rows.Next() {
		var sender, body, missing, hash string
		var received int64
		var off, unread, parts int
		if err := rows.Scan(&sender, &received, &off, &body, &unread, &parts, &missing, &hash); err != nil {
			continue
		}
		// hash は削除に使う。**索引ではなく生 PDU の指紋**なので、
		// モデムの索引が使い回されても取り違えない。
		m := map[string]any{
			"from": sender, "text": body, "unread": unread == 1, "hash": hash,
			// 受信したときのタイムゾーンのまま出す。網が付けた時刻なので、
			// 端末の時計に寄せると意味が変わる。
			"time": time.Unix(received, 0).In(time.FixedZone("", off)).Format(time.RFC3339),
		}
		if parts > 0 {
			m["parts"] = parts
		}
		if missing != "" {
			var ns []int
			for _, s := range strings.Split(missing, ",") {
				if n, err := strconv.Atoi(s); err == nil {
					ns = append(ns, n)
				}
			}
			m["missing"] = ns
		}
		out = append(out, m)
	}
	return map[string]any{"iccid": iccid, "messages": out, "count": len(out)}
}

// currentNumber reads the line's own number. **空を返すことがある** —
// EF_MSISDN を持たない profile は珍しくない。
func currentNumber(ch *ATChannel) string {
	lines, err := ch.Command("AT+CNUM")
	if err != nil {
		return ""
	}
	v, ok := First(lines, "+CNUM:")
	if !ok {
		return ""
	}
	f := splitAT(v)
	if len(f) < 2 {
		return ""
	}
	return strings.Trim(strings.TrimSpace(f[1]), "\"")
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
