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
| `apn_apply` | — | 今の SIM に対応するものを `network.wan` へ流して `ifup wan` |
| `apn_probe` | — | **SIM に APN を聞く。**提案を返すだけで保存も適用もしない |

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
