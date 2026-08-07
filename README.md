# luci-app-sbair-modem

**SoftBank Air 6 (RG620T-SBK / RUDOLF) 専用**の LuCI アプリ。
モデムの状態表示、eSIM 管理、SIM ロック、APN、SMS の受信を扱う。

---

## 対象と前提

| | |
|---|---|
| 機体 | SoftBank Air 6 / RG620T-SBK(MediaTek MT6990 + MD800 内蔵モデム) |
| OS | OpenWrt 21.02.7(ベンダ改造版) |
| AT の入口 | **`/dev/adb_atci_socket`**(`atcid` が listen する unix ソケット) |
| SIM | 物理スロット 1 + 内蔵 eSIM 1。**同時に有効なのは 1 つ**(`AT+ESIMMAP?`) |
| 無いもの | `ttyUSB*` / `cdc-wdm*` / `qmi_wwan` / `cdc_mbim` |

---

## 画面

`admin/sbair` の下に 4 タブ。

| | |
|---|---|
| **電波状況** | ネットワーク登録 / 事業者 / 接続方式 / TAC / Cell ID と RSSI・RSRP・RSRQ。15 秒ごとに更新。<br>**バンドの表示と選択**(LTE / 5G。ネットワークにつながらなければ自動で巻き戻す)と**アンテナごとの RSRP・SINR**。<br>**IMS の有効/無効**と**モデムのリセット**(`AT+CFUN=0` → `1` → `ifup wan`)もここ |
| **SIM** | マッピングと切替 / カード種別 / 電話番号 / **SIM ロックの解除・再設定** / **APN**(ICCID ごとに保存)/ eUICC の EID と profile の一覧・命名・操作・インストール (ES9+) |
| **SMS** | **受信専用。** SIM(電話番号)ごとに一覧。取り込み・削除。保管は SQLite |
| **デバイス情報** | 機種 / ファームウェア / IMEI と `AT+QTEMP` の温度 27 センサ |

**識別子(IMEI / IMSI / ICCID / EID / Cell ID / 電話番号 / SMS の送信者)は既定で伏せる。**
チェックボックスで表示に切り替わる。

---

## 中身

```
luci-app-sbair-modem/
├── root/usr/libexec/rpcd/sbair        rpcd の入口 (2 行。中身は sbair-modem rpcd)
├── root/usr/share/rpcd/acl.d/         ACL
├── root/usr/share/luci/menu.d/        メニュー
├── htdocs/luci-static/resources/
│   ├── tools/sbair.js                 タブ共通の小物
│   ├── protocol/ql_datacall.js        Network → Interfaces で WAN を扱えるようにする
│   └── view/sbair/{signal,sim,sms,device}.js
├── root/etc/init.d/sbair-apn          起動時に APN を流し、AT+CNMI を入れ直す
├── src/sbair-modem/                   バックエンド (Go)
└── docs/API.md                       ubus API と LuCI 画面 (SMS / IMS / バンド / リセット)
```

```
sbair-modem at [-r] [-t SEC] '<AT>'   AT を 1 本流す
            simlock [on|off]          SIM ロックの表示 / 切替
            ims [on|off]              IMS の表示 / 切替
            reset                     モデムのリセット (CFUN 0/1) と ifup wan
            sms                       受信 SMS を保管庫へ取り込む
            apn [apply|probe]         APN の表示 / 適用 / SIM から読み出す
            overview                  モデム状態を JSON で
            status                    SIM マッピングとカードの種別
            simmap [1|2]              SIM マッピングの表示 / 切替
            list / enable / disable / delete
            nickname <ICCID> [<NAME>] profile に名前を付ける
            download / discovery      ES9+ / ES11
            gc                        漏れた論理チャネルを回収する
            rpcd list | call <method> rpcd バックエンド (rpcd が呼ぶ)
```

---

## ビルドと導入

```sh
./build.sh              # out/sbair-modem を aarch64 向けに作る
./install.sh /          # 動いている実機に入れる
./install.sh <tree>     # 展開済み rootfs ツリーへ導入する
```

**static / CGO 無し**で作るので、ビルドホストが glibc でも OpenWrt (musl) で動く。
Go 1.25+ が要る(OpenWrt 21.02 の golang は 1.18 で**足りない**)。
goenv を使うなら `export PATH=$HOME/.goenv/bin:$HOME/.goenv/shims:$PATH`。

確認は [docs/API.md](docs/API.md)。

---

## ⚠ 注意

- **内蔵 eSIM に ISD-R は無い。** eUICC を操作できるのは物理スロットのカードだけで、
  `AT+ESIMMAP?` が `1` のときに限られる。**しかも、そのカードが eUICC とは限らない** —
  通常の SIM が挿さっているのは正常な状態として扱う(→ [ESIM_AT.md](https://github.com/soralis0912/sbair6-rs/blob/main/docs/ESIM_AT.md))
- **`AT+ESIMMAP=<n>` を素で打たない。** 必ず `AT+CFUN=4` で落としてから。
  切替後 20〜30 秒は AT が無応答
- **再起動すると物理スロット側へ移る。** そこに有効な profile が無ければ圏外になる
- **SIM ロックは `AT+ESMLCK` を直接発行して解除する**(ベンダの `/bin/sim_lock.sh` は
  戻り値を見ないので使わない)。解除後は `AT+CFUN=0` → `1` が要る(→ [AT.md](https://github.com/soralis0912/sbair6-rs/blob/main/docs/AT.md))
- **IMS は出荷状態で無効。**
  **SMS は IMS 経由で配送されるので、未登録だと 1 通も届かない。**
  電波状況タブから有効にできる。**再起動はまたぐ**が、設定を初期化すると
  SIM ロックが戻って在圏しなくなり、IMS も登録できなくなる
- **SMS の取り込みは、モデム側の未読を既読に変える**(`AT+CMGL` の仕様で避けられない)。
  保管庫には最初に取り込んだときの未読状態が残る。純正 WebUI の未読表示は消える
- **eSIM のインストールには、この機体からインターネットへ出られることが要る。**
  既定ではデフォルトルートが無い(→ [ESIM_AT.md](https://github.com/soralis0912/sbair6-rs/blob/main/docs/ESIM_AT.md))

---

## ライセンス

MIT(→ [LICENSE](LICENSE))。第三者ソフトウェアの扱いは [NOTICE.md](NOTICE.md)。
