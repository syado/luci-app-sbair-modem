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
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// 受信 SMS の保管。SIM (ICCID) ごとに分けて持つ。
//
// **モデムに置いたままにできない理由が 2 つある。**
//
//  1. モデムの保存領域は溢れれば消える
//  2. `AT+CMGL` が未読を既読に変えてしまう (→ docs/API.md)。**最初に取り込んだ
//     ときの未読状態は、そのとき記録しないと二度と分からない**
//
// なので取り込みは `INSERT OR IGNORE`。同じメッセージを読み直しても
// **未読の記録を後から書き換えない**。

// 保管先。既定は overlay の `/etc/sbair/sms.db`。**uci で変えられる。**
//
//	uci set sbair.sms=sms
//	uci set sbair.sms.db=/data/sbair/sms.db
//	uci commit sbair
//	mv /etc/sbair/sms.db /data/sbair/sms.db      # ★ 自分で移すこと
//
// ⚠ **既定の置き場は、この機体でいちばん壊れやすい場所である。** 実測:
//
//	                       再起動   A/B 切替   rootfs を dd で焼き直す
//	/etc (overlay)         残る     消える     消える
//	/data (user_data)      残る     残る       残る
//
// overlay は **rootfs パーティションの中**にあるので、その面を焼くと消える
// (sbair6-rs の docs/STRIP_STOCK_UI.md §7-1)。この DB は
// 「モデムの保存領域は溢れれば消える」「`AT+CMGL` が未読を既読に変える」から
// 作ったものなので、**本当はモデムより長く持つ場所に置きたい。**
//
// **それでも既定を `/etc` のままにしてある。** `/data` はベンダ領域
// (`/data/knos`, `/data/mdlog`) で、**工場出荷リセットでどうなるかを
// 確かめていない**ため。確かめたら既定を変えてよい。
//
// ⚠ **自動移行はしない。** 黙って動かすと、どちらが本物か分からなくなる。
// 代わりに、置き去りになった既定の DB があれば警告する。
const smsDBDefault = "/etc/sbair/sms.db"

var (
	smsDBOnce     sync.Once
	smsDBResolved string
	smsDBNote     string // 画面に出す注意書き。空なら何も無い
)

// smsDBFile resolves the configured database path.
//
// ⚠ **相対パスは受けない。** rpcd から呼ばれるときの作業ディレクトリは
// 何も保証されないので、相対で受けると呼ばれ方によって別のファイルを開く。
func smsDBFile() string {
	smsDBOnce.Do(func() {
		smsDBResolved = smsDBDefault

		p := strings.TrimSpace(os.Getenv("SBAIR_SMS_DB"))
		if p == "" {
			if v, err := uci("get", "sbair.sms.db"); err == nil {
				p = strings.TrimSpace(v)
			}
		}
		if p == "" {
			return
		}
		if !filepath.IsAbs(p) {
			smsDBWarn("SMS の保管先 %q は絶対パスでないので使いません。%s を使います。",
				p, smsDBDefault)
			return
		}
		smsDBResolved = p

		// 既定の場所に古い DB が残っていたら言う。**移し忘れると
		// 「SMS が全部消えた」ように見える**が、実際には置き去りなだけ。
		if p != smsDBDefault {
			if _, err := os.Stat(smsDBDefault); err == nil {
				smsDBWarn("以前の保管庫 %s が残っています。中身は引き継がれないので、"+
					"要るなら手で %s へ移してください。", smsDBDefault, p)
			}
		}
	})
	return smsDBResolved
}

// smsDBWarn records a warning and prints it.
//
// ⚠ **`slog` を使わないこと。** 既定のハンドラは `-v` を付けないと
// `io.Discard` に捨てる(stdout を JSON に保つため)ので、**警告が
// 誰にも届かない**。実際にこれで「警告を出したつもり」になっていた。
// 画面にも出せるよう note にも積む。
func smsDBWarn(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if smsDBNote != "" {
		smsDBNote += " "
	}
	smsDBNote += msg
	fmt.Fprintln(os.Stderr, "sbair-modem: "+msg)
}

// 純 Go の SQLite (modernc.org/sqlite) を使う。**CGO は使えない** —
// この機体向けは CGO_ENABLED=0 の静的ビルドが前提で、`libsqlite3.so` に
// 動的リンクするとその前提が崩れる。バイナリは 6.1 MB → 10.4 MB になる。
func openSMSDB() (*sql.DB, error) {
	path := smsDBFile()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
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
		// **消したものが取り込み直しで戻らないようにする。** モデム側の削除に
		// 失敗しても (既に無い / AT が落ちている)、こちらで蓋をする。
		`CREATE TABLE IF NOT EXISTS deleted (
			hash TEXT PRIMARY KEY,
			at   INTEGER
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
			SELECT ?,?,?,?,?,?,?,?,?,?,?
			WHERE NOT EXISTS (SELECT 1 FROM deleted WHERE hash = ?)`,
			iccid, m.From, m.when.Unix(), off, m.Text, boolInt(m.Unread),
			m.Parts, strings.Join(missing, ","), m.Hash, now,
			strings.Join(m.raw, "|"), m.Hash)
		if err != nil {
			return map[string]any{"error": fmt.Sprintf("保存できません: %v", err)}
		}
		if n, _ := res.RowsAffected(); n > 0 {
			added++
		}
		// **既存行の raw を埋め戻す。** raw 列は後から足したので、それ以前に
		// 取り込んだ行は空。空のままだと削除でモデム側の該当を特定できず、
		// 「保管庫からは消えたのにモデムには残る」ことになる。
		// **unread は触らない** — 上書きしないのが保管庫の存在理由。
		if _, err := db.Exec(`UPDATE message SET raw = ?
			WHERE hash = ? AND (raw IS NULL OR raw = '')`,
			strings.Join(m.raw, "|"), m.Hash); err != nil {
			return map[string]any{"error": fmt.Sprintf("保存できません: %v", err)}
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
	out := map[string]any{"sims": sims, "db": smsDBFile()}
	if smsDBNote != "" {
		out["note"] = smsDBNote
	}
	return out
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

// smsDelete removes one message from the store **and from the modem**.
//
// **両方消さないと意味が無い。** DB だけ消してもモデムには残るので、
// 次の取り込みで戻ってくる。モデム側の削除に失敗しても `deleted` に
// 記録するので、少なくとも画面には二度と出ない。
func smsDelete(ch *ATChannel, hash string) map[string]any {
	if strings.TrimSpace(hash) == "" {
		return map[string]any{"error": "hash が指定されていません。"}
	}
	db, err := openSMSDB()
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("データベースを開けません: %v", err)}
	}
	defer db.Close()

	var raw string
	if err := db.QueryRow(`SELECT COALESCE(raw,'') FROM message WHERE hash = ?`, hash).
		Scan(&raw); err != nil {
		return map[string]any{"error": "そのメッセージは保管庫にありません。"}
	}
	return finishDelete(db, ch, map[string]bool{hash: true}, rawSet([]string{raw}))
}

// smsPurge removes every stored message of one SIM, and their modem copies.
func smsPurge(ch *ATChannel, iccid string) map[string]any {
	if strings.TrimSpace(iccid) == "" {
		return map[string]any{"error": "ICCID が指定されていません。"}
	}
	db, err := openSMSDB()
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("データベースを開けません: %v", err)}
	}
	defer db.Close()

	rows, err := db.Query(`SELECT hash, COALESCE(raw,'') FROM message WHERE iccid = ?`, iccid)
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("読み出せません: %v", err)}
	}
	hashes := map[string]bool{}
	var raws []string
	for rows.Next() {
		var h, r string
		if rows.Scan(&h, &r) == nil {
			hashes[h] = true
			raws = append(raws, r)
		}
	}
	rows.Close()

	// **全件削除ではモデムの保存領域も空にする。** そこにあるのは今刺さって
	// いる SIM 宛のものなので、選んでいる SIM がそれと同じときだけ。
	// 取り込む前に届いたぶんや、raw を持たない古い行の取りこぼしもこれで消える。
	wipe := iccid == currentICCID(ch)
	if len(hashes) == 0 && !wipe {
		return map[string]any{"result": "ok", "deleted": 0, "modem": 0}
	}
	out := finishDelete(db, ch, hashes, rawSet(raws))
	if wipe {
		if n, notes := wipeModem(ch); n > 0 || len(notes) > 0 {
			out["modem"] = out["modem"].(int) + n
			if len(notes) > 0 {
				out["errors"] = append(toStrings(out["errors"]), notes...)
			}
		}
	}
	return out
}

// wipeModem deletes whatever is left in the modem's own SMS storage.
func wipeModem(ch *ATChannel) (int, []string) {
	lines, err := ch.Command("AT+CMGL=4")
	if err != nil {
		return 0, []string{fmt.Sprintf("モデムを読めません: %v", err)}
	}
	parts, _ := parseCMGL(lines)
	gone := 0
	var notes []string
	for _, p := range parts {
		if _, err := ch.Command(fmt.Sprintf("AT+CMGD=%d", p.Index)); err != nil {
			notes = append(notes, fmt.Sprintf("#%d を消せません: %v", p.Index, err))
			continue
		}
		gone++
	}
	return gone, notes
}

func toStrings(v any) []string {
	if s, ok := v.([]string); ok {
		return s
	}
	return nil
}

// rawSet splits the stored "pdu|pdu" fields back into individual PDUs.
func rawSet(joined []string) map[string]bool {
	out := map[string]bool{}
	for _, j := range joined {
		for _, p := range strings.Split(j, "|") {
			if p = strings.TrimSpace(p); p != "" {
				out[p] = true
			}
		}
	}
	return out
}

func finishDelete(db *sql.DB, ch *ATChannel, hashes map[string]bool, raws map[string]bool) map[string]any {
	gone, notes := deleteFromModem(ch, raws)

	now := time.Now().Unix()
	deleted := 0
	for h := range hashes {
		if _, err := db.Exec(`DELETE FROM message WHERE hash = ?`, h); err != nil {
			return map[string]any{"error": fmt.Sprintf("消せません: %v", err)}
		}
		if _, err := db.Exec(`INSERT OR IGNORE INTO deleted(hash, at) VALUES(?,?)`, h, now); err != nil {
			return map[string]any{"error": fmt.Sprintf("消せません: %v", err)}
		}
		deleted++
	}
	out := map[string]any{"result": "ok", "deleted": deleted, "modem": gone}
	if len(notes) > 0 {
		out["errors"] = notes
	}
	return out
}

// deleteFromModem issues AT+CMGD for whichever slots hold these PDUs.
//
// **索引は保存しておかない。** モデムの索引は使い回されるので、取り込んだ
// ときの番号は後から当てにならない。消す直前に AT+CMGL を読み直し、
// **生の PDU が一致したものだけ**を消す。
func deleteFromModem(ch *ATChannel, raws map[string]bool) (int, []string) {
	if len(raws) == 0 {
		return 0, nil
	}
	if _, err := ch.Command("AT+CMGF=0"); err != nil {
		return 0, []string{fmt.Sprintf("PDU モードにできません: %v", err)}
	}
	lines, err := ch.Command("AT+CMGL=4")
	if err != nil {
		return 0, []string{fmt.Sprintf("モデムを読めません: %v", err)}
	}
	parts, _ := parseCMGL(lines)

	gone := 0
	var notes []string
	for _, p := range parts {
		if !raws[p.Raw] {
			continue
		}
		if _, err := ch.Command(fmt.Sprintf("AT+CMGD=%d", p.Index)); err != nil {
			notes = append(notes, fmt.Sprintf("#%d を消せません: %v", p.Index, err))
			continue
		}
		gone++
	}
	return gone, notes
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
