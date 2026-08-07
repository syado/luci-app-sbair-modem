<!-- SPDX-License-Identifier: MIT -->
# ubus API と LuCI 画面

rpcd は exec バックエンドを**呼び出しごとに 1 プロセス起こす**。
`/usr/libexec/rpcd/sbair` は 2 行の shim で、中身は `sbair-modem rpcd`。

```
sbair list              → メソッド表を JSON で
sbair call <method>     → 引数を JSON で stdin、結果を stdout
```

守ること:

- **`list` は AT を一切叩かない。** rpcd は起動時と ACL 列挙のたびに呼ぶ。
  ここで eUICC を開くと、LuCI にログインするたびカードに論理チャネルが増える
- **メソッドは粗く。** 1 呼び出し = 1 プロセス + 1 flock + 1 AT セッション
- **引数の型は値で宣言する。** rpcd はサンプル値の blobmsg 型から型を決めるので、
  `{"mapping": 0}` が整数、`{"mapping": "int"}` は**文字列**の宣言になる
- **失敗しても終了コードは 0。** 理由は payload に入れる。rpcd は非ゼロ終了を
  「壊れたバックエンド」と見なして LuCI に何も渡さない

## メソッド

| | 引数 | |
|---|---|---|
| `overview` | — | モデム状態一式 |
| `esim_status` | — | SIM マッピング、eUICC の可否、EID、profile 一覧 |
| `esim_list` | — | profile 一覧のみ |
| `esim_enable` | `iccid` | profile 有効化 |
| `esim_disable` | `iccid` | profile 無効化 |
| `esim_delete` | `iccid` | profile 削除(不可逆) |
| `esim_nickname` | `iccid` `nickname` | profile に名前を付ける(空で消す) |
| `simmap_get` | — | SIM マッピング |
| `simmap_set` | `mapping` | SIM マッピングの切替を**開始**する(すぐ返る) |
| `simmap_status` | — | 切替の進捗。**AT に触らない** |
| `esim_download` | `activation_code` `confirmation_code` | eSIM のインストールを**開始**する(すぐ返る) |
| `esim_download_status` | — | インストールの進捗。**AT に触らない** |
| `simlock_get` | — | SIM ロックの状態とカテゴリ別の残り試行回数 |
| `simlock_set` | `on` | ロックの切替を**開始**する(すぐ返る) |
| `simlock_status` | — | 切替の進捗。**AT に触らない** |
| `apn_status` | — | 保存済みの APN 一覧、現在の SIM、`network.wan` の値 |
| `apn_set` | `iccid` `apn` `auth` `username` `password` `iptype` `label` | 保存 |
| `apn_delete` | `iccid` | 削除 |
| `apn_apply` | — | 今の SIM に対応するものを `network.wan` **と `/etc/config/lte`** へ流して `ifup wan` |
| `apn_probe` | — | **SIM に APN を聞く。**提案を返すだけで保存も適用もしない |

> ⚠ **`apn_apply` が `/etc/config/lte` も書くのは必須。** ベンダの `/usr/bin/knsh` が
> 起動時に `lte.<mode>.apn` / `apn_auth_type` / `apn_userid` / `apn_passwd` を
> `network.wan.*` へ流し込むので、**`network.wan` だけ直すと再起動で戻る**。
>
> ⚠ **登録が無い SIM では `apn_apply` は `skipped` を返し、`lte.*` も触らない。**
> つまり**前の SIM の APN が `/etc/config/lte` に残る**。登録のある SIM に
> 差し替えれば追随するが、登録の無い SIM を入れると knos が前の SIM の
> APN を `network.wan` へ流す。
>
> ⚠ **`apn_probe`(`ql_datacall --apn_provision_by_sim`)の答えを鵜呑みにしない。**
> au の SIM で `unod.au-net.ne.jp` を返すが、**それでは繋がらない**(正解は
> `uno.au-net.ne.jp`)。あくまで候補。

### SMS (受信のみ)

| メソッド | 引数 | |
|---|---|---|
| `sms_status` | — | `AT+CPMS?` だけ。件数と容量。**本文を読まない** |
| `sms_list` | — | `AT+CMGF=0` → `AT+CMGL=4`。PDU を解いて連結を繋ぐ。保存しない |
| `sms_import` | — | `sms_list` の結果を保管庫へ。**モデムを読むのはこれだけ** |
| `sms_sims` | — | 保管庫が知っている SIM(ICCID / 電話番号 / 件数) |
| `sms_messages` | `iccid` `limit` | その SIM のメッセージ。新しい順。削除に使う `hash` を含む |
| `sms_delete` | `hash` | 1 通を保管庫とモデムの両方から削除。**取り消せない** |
| `sms_purge` | `iccid` | その SIM の全件 + モデムの保存領域を空に。**取り消せない** |

#### 削除

**保管庫だけ消しても意味が無い。** モデムには残るので次の取り込みで戻ってくる。

モデム側は **`AT+CMGD` で消す**が、**索引は保存しない** — モデムの索引は
使い回されるので、取り込んだときの番号は後から当てにならない。消す直前に
`AT+CMGL` を読み直し、**生の PDU が一致したものだけ**を消す。

`deleted` テーブルに hash を残すので、モデム側の削除に失敗しても
(既に無い / AT が落ちている)**取り込み直しで戻らない**。

> ⚠ **`sms_purge` はモデムの保存領域を丸ごと空にする**(選んでいる SIM が
> いま刺さっているものと同じときだけ)。取り込む前に届いたぶんも消える。
> 「この SIM の全件を削除」という表示どおりの挙動にしてある。

> ⚠ **`raw` 列は後から足した。** それ以前に取り込んだ行は空で、そのままだと
> 削除でモデム側の該当を特定できない。取り込み時に埋め戻す。
> **`unread` は触らない。**

#### 保管庫 (SQLite)

`/etc/sbair/sms.db`。**モデムに置いたままにできない理由が 2 つある:**

1. 保存領域は 70 通で頭打ち。溢れれば消える
2. `AT+CMGL` が未読を既読に変える。**最初に取り込んだときの未読状態は、
   そのとき記録しないと二度と分からない**

なので取り込みは `INSERT OR IGNORE`。**2 回目以降の取り込みで未読を上書きしない。**

同一性は**生の PDU の SHA-256**。送信者・時刻・本文の組では足りない —
一括送信は同じ秒に同じ本文を複数投げてくる(実際に届いた 3 通は SCTS の秒
だけが違っていた)。

> **/data ではなく overlay に置く。** `/data` はベンダのログ置き場
> (`/data/knos`, `/data/mdlog`)で初期化の対象。overlay 側は
> `/etc/config/sbair` は再起動をまたいで残る。70 通ぶんで数十 KB。

> **純 Go の SQLite (`modernc.org/sqlite`) を使う。** この機体向けは
> `CGO_ENABLED=0` の静的ビルドが前提で、機体にある `libsqlite3.so.0` に
> 動的リンクするとその前提が崩れる。**バイナリは 6.1 MB → 10.4 MB。**

> **保管庫は再起動をまたぐ。** ただし **`AT+CNMI` は `0,0,0,0,0` に戻る**ので、
> `init.d/sbair-apn` が起動時に `1,1,0,0,0` を入れ直す。

> **電話番号 (`AT+CNUM`) は空のことがある。** EF_MSISDN を持たない profile は
> 珍しくないので、`sim.number` は NULL 可、画面は ICCID でも選べるようにする。

> ⚠ **`sms_list` は未読を既読に変える。** TS 27.007 で `+CMGL` は
> REC UNREAD を REC READ にする。`AT+CMGL=?` はモード引数を申告しないので
> 避けようがない。応答の `<stat>` は変更**前**の値なので 1 回目の未読表示は
> 正しいが、開き直すと全部既読になる。**純正 WebUI の未読表示も一緒に消える。**
>
> そのため画面は**開いただけでは読みに行かない**。定期更新は `sms_status`
> (件数のみ) で回し、本文は明示のボタンで取る。

**テキストモード (`AT+CMGF=1`) は使わない。** 楽に見えるが連結の UDH が落ちて
分割を繋げられなくなり、タイムゾーンも落ちる。日本語は UCS2 なのでどのみち
自前で解く必要がある。

デコーダは `pdu.go`。`pdu_test.go` に GSM 7bit / UCS2 / 連結 (septet offset) /
SCTS のタイムゾーン / DCS のベクタがある — **実機に 1 通も入っていない状態でも
壊れたことが分かるように**、`go test ./src/sbair-modem` で回る。

返る 1 通の形:

```json
{ "indexes": [1,2], "from": "+8190...", "time": "2026-08-07T19:22:31+09:00",
  "text": "…", "unread": true, "parts": 2, "missing": [3] }
```

`missing` は届いていない分割の番号。**揃うまで伏せずに、届いているぶんを出す。**

> **解けることを確かめた範囲。** GSM 7bit / UCS2 日本語 / 文字表記の送信者
> (TOA `0xD0` → `LINE`) / 数字の送信者 (TOA `0x81`、15 桁 + `F` 詰め) /
> SCTS のタイムゾーン (+09:00) を、生 PDU と突き合わせて一致を確認。
>
> ⚠ **連結メッセージ (UDH) だけは実データで未検証。** 単体テストは通っている。
>
> ⚠ **この機体で受信直後に未読になるのかは未確認。** 届いたものはすべて
> `<stat>=1` (REC READ) だった — ベンダの RIL (`ril.unsol.sms.pdu`) が先に
> 読んでいる可能性がある。

> **SMS が届かないときはまず IMS を見ること。** SMS は IMS 経由で配送される。
> IMS が未登録だと網が配送できず、モデムには何も来ない (`AT+CPMS` が 0 のまま、
> `AT+CNMI=1,1` にしても増えず、`ubus subscribe ril.unsol.sms.pdu` にも来ない)。
> **出荷状態ではモデムの IMS が Off。** → 下の「IMS」

### IMS

| メソッド | 引数 | |
|---|---|---|
| `ims_status` | — | `AT+CIREG?` + `mipc_wan_cli --ims_get_config` |
| `ims_set` | `on` | `mipc_wan_cli --ims_set_config 0\|1` |

**出荷状態ではモデム側で無効。** SoftBank Air は音声サービスを持たないので
ベンダが切っている。**SMS は IMS 経由で配送されるので、未登録だと
1 通も届かない**。

> ⚠ **判定に `--ims_get_config` を使わない。** 書く先と読む先が食い違っており、
> **登録しているのに `Off` を返す**。真偽は `AT+CIREG?`。

> ⚠ **このフラグは実質 VoLTE の切替**(`ims_set_config success. volte: %d`)。
> ```
> 未登録 (2,0,0) で 1 → 10 秒ほどで 2,1,5 (音声 + SMS over IMS)
> 登録済み (2,1,5) で 0 → 2,1,4。**登録と SMS over IMS は残る**
> ```
>
> **0 にしても IMS が完全に落ちるとは限らない。** 画面は `<ext_info>` の
> ビットをそのまま出し、「オン/オフ」と断定しない。

`<ext_info>` は TS 27.007 のビットマップ: 1=音声(MMTEL) / 2=テキスト /
**4=SMS over IMS** / 8=ビデオ。

> ⚠ **再起動をまたがない。** そのつど入れ直す。

### モデムのリセット

| メソッド | 引数 | |
|---|---|---|
| `modem_reset` | — | `AT+CFUN=0` → `1` → 在圏待ち → `ifup wan`。非同期 (30〜60 秒) |
| `modem_reset_status` | — | 進捗。`/tmp/sbair-reset.json` を読むだけで **AT を開かない** |

> ベンダの `ql_datacall` は失敗のしかたによってはモデム側のセッションが詰まり、
> **`ql_datacall.sh` の retry ループでは抜けられない**ことがある
> (`data call result:1316611` が返り続ける)。そのときの逃げ道。
> **自動では走らせない** — 固着しない失敗もあり、毎回落とすと通信が余計に切れる。

### 時間のかかる処理 (切替 約 80 秒 / インストール 20〜30 秒)

どちらも同じ形。足回りは `job.go`、状態は `/tmp/sbair-<job>.json`。


1. `simmap_set` は**ワーカーを `setsid` で切り離して起動し、すぐ返る**。
   rpcd は呼び出し終了時にプロセスグループを殺すので、`setsid` が無いと
   **電波を止めたまま途中で切られる**
2. ワーカーは進捗を `/tmp/sbair-simmap.json` に書く
3. `simmap_status` は**そのファイルだけ**を読む。**AT には触らない** —
   ワーカーが flock を握っている間こそ進捗を見たいため

手順そのものは [ESIM.md](ESIM.md)。

## APN

UCI は**外部コマンドで触る**。libuci の Go バインディングは CGO が要り、
static バイナリという前提を壊すため。

**ICCID をキーに `/etc/config/sbair` へ保存する。** `network.wan` は 1 つしか
無いので、SIM を差し替えたり eSIM の profile を切り替えるたびに手で入れ直す
ことになる。ICCID ごとに覚えておけば、その SIM に対応するものが当たる。

```
config apn 's8981****************'
	option iccid '8981…'
	option apn   'au.au-net.ne.jp'
	option auth  '3'
	option username '…'
	option password '…'
	option label 'au'
```

適用先は `network.wan`(proto `ql_datacall`)の apn / auth / username /
password / iptype。書いたあと `ifup wan`。
起動時は `/etc/init.d/sbair-apn` が `sbair-modem apn apply` を呼ぶ。

**全項目を手で入れる前提。** ただし SIM は自分の APN を知っているので、
`apn_probe` で読み出して欄を埋められる:

```sh
sbair-modem apn probe
→ {"suggestion":{"apn":"unod.au-net.ne.jp","auth":"2","username":"…","password":"…","iptype":"3"}}
```

中身はベンダの `ql_datacall --apn_provision_by_sim`。事業者の DB を持つのは
モデム側で、こちらが MCC/MNC の表を抱える必要はない。返るキーは netifd の
proto が使うものと同じ対応(`apn`/`auth_type`/`user`/`password`/`protocol`
→ `apn`/`auth`/`username`/`password`/`iptype`)。

> **提案するだけで保存も適用もしない。** 事業者から降ってきた値がその契約で
> 正しいとは限らないので、確認してから保存する。

> ⚠ **登録が無ければ WAN を触らない。** 空の APN を書き込むと、ベンダの
> `check_auto_apn_prov`(SIM から APN を引く仕組み)を潰しかねない。

> ⚠ **`uci get` の stderr を値として読まないこと。** 未設定の option に対して
> `uci: Entry not found` を標準エラーへ出すので、`CombinedOutput` で拾うと
> それが値になり、書き戻すと設定として保存されてしまう
> (実際に `network.wan.iptype='uci: Entry not found'` を作ってしまった)。

## 画面

`admin/sbair` の下に 3 タブ。共通の小物は
`htdocs/luci-static/resources/tools/sbair.js`(`require tools.sbair`)。

| | |
|---|---|
| `admin/sbair/signal` | 電波状況。15 秒ごとに更新 |
| `admin/sbair/sim` | SIM マッピングと切替、カード種別、電話番号、SIM ロック、APN、profile 操作とインストール |
| `admin/sbair/device` | 機種 / ファームウェア / IMEI と `AT+QTEMP` の温度 |

自動更新するのは電波状況だけ(15 秒)。他は明示のボタンで読み直す。
切替中だけ `simmap_status` を 3 秒間隔で見に行き、終わったら止める。

**メニューは `admin/sbair` に置いてある。** `admin/modem` は他のモデム系アプリと
親ノードを奪い合うので使わない。

- **識別子(IMEI / IMSI / ICCID / EID / Cell ID)は既定で伏せる**
- **ネットワークに登録されていないときは電波の数値に注記を出す。** `+CESQ` は登録されていなくても
  見えているセルの測定値を返すので、素の数字だけ出すと繋がっているように見える
- **eUICC の操作は `ESIMMAP: 1` かつカードが eUICC のときだけ。**
  通常の SIM が挿さっているのは正常な状態として出す(→ [ESIM.md](ESIM.md))
- **切替と profile 操作には確認ダイアログを出す。** 切替は電波を止めるし、
  `delete` は取り消せない

## 動作確認

```sh
ubus list | grep sbair
ubus -v list sbair
ubus call sbair overview
ubus call sbair esim_status
```

画面が使う経路(LuCI セッション → `/ubus/`)まで見るなら:

```sh
curl -s -c cj -d 'luci_username=root&luci_password=PASS' http://<ip>/cgi-bin/luci
SID=$(curl -s -b cj http://<ip>/cgi-bin/luci/admin/sbair/overview \
      | grep -oE 'sessionid"?:"?[0-9a-f]{32}' | grep -oE '[0-9a-f]{32}')
curl -s -b cj -H 'Content-Type: application/json' \
  -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"call\",\"params\":[\"$SID\",\"sbair\",\"overview\",{}]}" \
  http://<ip>/ubus/
```
