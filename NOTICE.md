# 第三者のソフトウェアについて

ライセンスは [LICENSE](LICENSE)(MIT)。
**`sbair-modem` は 1 つだけ外部ライブラリに依存する。**

## github.com/damonto/euicc-go v1.1.2 — MIT

SGP.22 (RSP) の実装としてこのライブラリを使う。
ES9+ の profile ダウンロードは SM-DP+ との TLS と ECDSA の署名検証を伴い、
規格に沿った実装が要るため、自前で書き直す対象にはしていない。

- ライセンス全文: [`licenses/euicc-go.LICENSE`](licenses/euicc-go.LICENSE)
- Copyright (c) 2025 Damon To

> ⚠ **`sbair-modem` は `CGO_ENABLED=0` の静的リンクで作る。**
> 生成されるバイナリには euicc-go のコードが含まれるため、
> **バイナリを配布するときは上記のライセンス表示を一緒に配ること**(MIT の条件)。
> `licenses/` をそのまま同梱すればよい。

ソースコードそのものは Go モジュールとして取得され、このリポジトリには入っていない
(`src/sbair-modem/go.mod` / `go.sum` が版を固定している)。
