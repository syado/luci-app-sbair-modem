# luci-app-sbair-modem

**SoftBank Air 6 (RG620T-SBK / RUDOLF) 専用**のモデム情報表示と eSIM 管理を行う LuCI アプリ。

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

`admin/sbair` の下に 3 タブ。

| | |
|---|---|
| **電波状況** | ネットワーク登録 / 事業者 / 接続方式 / TAC / Cell ID と RSSI・RSRP・RSRQ。15 秒ごとに更新 |
| **SIM** | マッピングと切替 / カード種別 / 電話番号 / **SIM ロックの解除・再設定** / **APN**(ICCID ごとに保存)/ eUICC の EID と profile の一覧・操作・インストール (ES9+) |
| **デバイス情報** | 機種 / ファームウェア / IMEI と `AT+QTEMP` の温度 27 センサ |

**識別子(IMEI / IMSI / ICCID / EID / Cell ID)は既定で伏せる。**
チェックボックスで表示に切り替わる。

---

## 中身

```
luci-app-sbair-modem/
├── root/usr/libexec/rpcd/sbair        rpcd の入口 (2 行。中身は sbair-modem rpcd)
├── root/usr/share/rpcd/acl.d/         ACL
├── root/usr/share/luci/menu.d/        メニュー
├── htdocs/luci-static/resources/
│   ├── tools/sbair.js                 3 タブ共通の小物
│   └── view/sbair/{signal,sim,device}.js
├── src/sbair-modem/                   バックエンド (Go)
└── docs/
    ├── AT.md                          AT 経路の実測
    ├── ESIM.md                        eUICC / ESIMMAP の実測
    └── API.md                         ubus API と LuCI 画面
```

```
sbair-modem at [-r] [-t SEC] '<AT>'   AT を 1 本流す
            simlock [on|off]          SIM ロックの表示 / 切替
            apn [apply|probe]         APN の表示 / 適用 / SIM から読み出す
            overview                  モデム状態を JSON で
            status                    SIM マッピングとカードの種別
            simmap [1|2]              SIM マッピングの表示 / 切替
            list / enable / disable / delete
            download / discovery      ES9+ / ES11
            gc                        漏れた論理チャネルを回収する
            rpcd list | call <method> rpcd バックエンド (rpcd が呼ぶ)
```

---

## ビルドと導入

```sh
./build.sh              # out/sbair-modem を aarch64 向けに作る
./install.sh /          # 動いている実機に入れる
./install.sh <tree>     # 展開済み rootfs ツリーに焼き込む
```

**static / CGO 無し**で作るので、ビルドホストが glibc でも OpenWrt (musl) で動く。
Go 1.25+ が要る(OpenWrt 21.02 の golang は 1.18 で**足りない**)。
goenv を使うなら `export PATH=$HOME/.goenv/bin:$HOME/.goenv/shims:$PATH`。

確認は [docs/API.md](docs/API.md)。

---

## 状態

| | |
|---|---|
| AT の入口 (`sbair-modem at`) | ✅ 実機確認済み |
| ES10(一覧 / EID / 有効化 / 無効化 / 削除) | ✅ 実機確認済み(eSTK.me) |
| rpcd バックエンド | ✅ 実機確認済み |
| SIM マッピングの切替 | ✅ 実機で往復確認済み(約 80 秒) |
| `install.sh`(ツリー / 実機直とも) | ✅ 実機確認済み |
| LuCI 画面の経路 | ✅ 3 ページとも 200、セッション ACL 越しの `/ubus/` で実データ |
| LuCI 画面の描画 | ✅ ブラウザで 3 タブと切替ボタンの動作を確認 |
| ES9+ ダウンロード(インストーラ) | ✅ 実機で完走(24 秒)。削除 → 再インストール → 有効化まで確認 |
| SIM ロックの解除 / 再設定 | ✅ 実機で確認(30 秒)。`AT+ESMLCK` を直接発行。解除後に au で登録まで成立 |
| APN の保存と適用 | ✅ ICCID ごとに `/etc/config/sbair` へ保存、`network.wan` へ反映して `ifup` まで確認 |
| APN を SIM から読み出す | ✅ `apn_probe`。欄を埋めるだけで保存・適用はしない |
| データ接続 (WAN) | 🚧 **上がらない。** ベンダの `ql_datacall` が `uci_load file failed` を繰り返す。APN とは別の問題 |

---

## ⚠ 注意

- **内蔵 eSIM に ISD-R は無い。** eUICC を操作できるのは物理スロットのカードだけで、
  `AT+ESIMMAP?` が `1` のときに限られる。**しかも、そのカードが eUICC とは限らない** —
  通常の SIM が挿さっているのは正常な状態として扱う(→ [docs/ESIM.md](docs/ESIM.md))
- **`AT+ESIMMAP=<n>` を素で打たない。** 必ず `AT+CFUN=4` で落としてから。
  切替後 20〜30 秒は AT が無応答
- **再起動すると物理スロット側へ移る。** そこに有効な profile が無ければ圏外になる
- **SIM ロックはファームウェア内の `/bin/sim_lock.sh off` で解除できる**(実機で確認、au で登録まで成立)。解除後は `AT+CFUN=0` → `1` が要る(→ [docs/AT.md](docs/AT.md))
- **eSIM のインストールには、この機体からインターネットへ出られることが要る。**
  既定ではデフォルトルートが無い(→ [docs/ESIM.md](docs/ESIM.md))

---

## ライセンス

MIT(→ [LICENSE](LICENSE))。第三者ソフトウェアの扱いは [NOTICE.md](NOTICE.md)。
