// SPDX-License-Identifier: MIT
// Copyright (c) 2026 syado

package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// br-lan に今いる端末の一覧(有線・無線を問わない)。読み取りのみ。
//
// **正は `bridge fdb show br br-lan`(ブリッジが学習した全MAC)。**
// 当初 `ip neigh`(Air6自身がARPした相手)だけを見ていたが、Air6が
// 光回線ルータへのダンブAPとして動いている間は、Air6自身が無線クライアント
// 宛に何かを送る理由が無いため、無線で繋いだ端末が全く出てこなかった
// (実機で確認済み: 有線2台のみ表示され、無線接続の端末が漏れた)。
// ブリッジのFDBなら、有線・無線どちらの端末も br-lan 上でフレームを
// 送った時点で学習されるので漏れない。
//
// IPアドレスとホスト名は分かる範囲でのベストエフォート
// (`ip neigh` / `/tmp/dhcp.leases`)。Air6自身がDHCPサーバでない間
// (光回線APモード中)は、この2つが空でIPが引けないことがある。
// 🔴 **信号強度/ノイズ/送受信レートは実機で取得不可と確認済み**: `iwinfo <iface> info`は
// Signal/Noise/Bit Rate/ESSIDが常に`unknown`、`iwinfo <iface> assoclist`は`bridge fdb`で
// 実在が確認できる接続端末がいる状態でも常に「No station connected」を返す(2026-08-09実機確認)。
// `/proc/net/wireless`も全インターフェース同一のダミー値(level -256 固定)で実データではない。
// mt_wifiのnl80211シムがこれらの統計を実装していないためと見られ、SSID名(UCIの静的設定)
// 以外の無線リンク品質情報はこの機体では表示できない。
type clientEntry struct {
	Name   string `json:"name"`
	MAC    string `json:"mac"`
	IP     string `json:"ip"`
	Link   string `json:"link"` // "wired" か 帯域("2.4G"/"5G"/"6G")
	SSID   string `json:"ssid,omitempty"`
	Vendor string `json:"vendor,omitempty"`
	Note   string `json:"note,omitempty"`
	OS     string `json:"os,omitempty"` // pingのTTLからの推測(目安)
}

// arpSweep は br-lan の自分の/24以下のサブネットへ並列に1発ずつpingを打ち、
// ARPキャッシュ(ip neigh)を埋める。ついでに応答のTTLも拾い、OS推定に使う
// (Windowsは128、Linux/macOS/Android/iOSは64、ネットワーク機器は255から
// 開始することが多く、LAN内(1ホップ)ならほぼそのままの値で返ってくる)。
//
// Air6がダンブAP(光回線側のDHCPを使う側)の間は、Air6自身が各クライアント
// 宛に通信する理由が無く、ip neighがほとんど埋まらない。IPを一覧に出すには
// 能動的にARPを引くしかない。/24より大きい(ホスト数の多い)ネットワークでは
// 走査に時間がかかりすぎるため行わない。
//
// 🔴 **実機で踏んだ問題(2026-08-12)**: br-lan に本来のDHCPで得たサブネット
// (例: 192.168.0.0/24)と工場出荷の 172.16.255.0/24 の両方が乗っている状態
// (dumb AP運用で実際によくある)だと、/24を2本走査して**最大508個の`ping`を
// 無制限に同時起動**することになり、この機体(組み込みARM)では
// プロセス生成だけで詰まってclient_listが数分単位で返らなくなった
// (呼び出し元の`select`は4秒で諦めるが、起動済みの`exec.Command`には
// タイムアウトが無いため、諦めた後も大量のpingプロセスが残り続けて
// 次回以降の呼び出しも巻き添えにする)。
// → 同時実行数を絞り、`context`で確実に打ち切ってプロセスを残さないようにする。
const arpSweepConcurrency = 32
const arpSweepBudget = 4 * time.Second

func arpSweep() map[string]int {
	ttls := map[string]int{}
	addrs, err := exec.Command("ip", "-4", "-o", "addr", "show", "dev", "br-lan").Output()
	if err != nil {
		return ttls
	}
	// 🔴 br-lan には複数のIPv4アドレスが載りうる(DHCPで得た本来のサブネットに加え、
	// 工場出荷の 172.16.255.254/24 が残っている)。**最後に見つけた1本だけを使うと、
	// 使われていない方だけを走査して肝心の方を素通りする事故になる**(実機で踏んだ)。
	// 見つかった行すべてを対象にする。
	var cidrs []string
	for _, line := range strings.Split(string(addrs), "\n") {
		f := strings.Fields(line)
		for i, tok := range f {
			if tok == "inet" && i+1 < len(f) {
				cidrs = append(cidrs, f[i+1])
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), arpSweepBudget)
	defer cancel()

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, arpSweepConcurrency)
	for _, cidr := range cidrs {
		ip, ipnet, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		ones, bits := ipnet.Mask.Size()
		if bits-ones > 8 {
			continue // /24 より広い(ホストが多すぎる)ので走査しない。
		}
		base := ip.Mask(ipnet.Mask).To4()
		if base == nil {
			continue
		}
		hostBits := bits - ones
		total := 1 << uint(hostBits)
		for i := 1; i < total-1; i++ { // ネットワークアドレスとブロードキャストは除く
			target := make(net.IP, 4)
			copy(target, base)
			v := (uint32(target[0])<<24 | uint32(target[1])<<16 | uint32(target[2])<<8 | uint32(target[3])) + uint32(i)
			target[0], target[1], target[2], target[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
			wg.Add(1)
			go func(ipStr string) {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					return // 予算を使い切った。これから起動する分はもう待たない。
				}
				defer func() { <-sem }()
				// CommandContext なら、ctx が切れた時点でまだ終わっていない
				// pingプロセスをこちらから確実に殺せる(素の exec.Command には
				// タイムアウトが無く、諦めた後もプロセスが残り続けてしまう)。
				out, err := exec.CommandContext(ctx, "ping", "-c", "1", "-W", "2", ipStr).Output()
				if err != nil {
					return
				}
				if ttl, ok := parsePingTTL(string(out)); ok {
					mu.Lock()
					ttls[ipStr] = ttl
					mu.Unlock()
				}
			}(target.String())
		}
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		// 遅い端末を待ちすぎない。ここまでに応答した分だけ ip neigh に載る。
	}
	return ttls
}

// parsePingTTL は busybox ping の "... ttl=64 ..." 行からTTLを取り出す。
func parsePingTTL(out string) (int, bool) {
	i := strings.Index(out, "ttl=")
	if i < 0 {
		return 0, false
	}
	i += len("ttl=")
	j := i
	for j < len(out) && out[j] >= '0' && out[j] <= '9' {
		j++
	}
	if j == i {
		return 0, false
	}
	n := 0
	for _, c := range out[i:j] {
		n = n*10 + int(c-'0')
	}
	return n, true
}

// guessOSFromTTL はLAN内(1ホップ)想定でTTLからOS系統を推測する。
// 経由するホップ数が増えるほどTTLは減るので、あくまで目安。
func guessOSFromTTL(ttl int) string {
	switch {
	case ttl == 0:
		return ""
	case ttl >= 60 && ttl <= 64:
		return "Linux/Unix系"
	case ttl >= 120 && ttl <= 128:
		return "Windows"
	case ttl >= 250:
		return "ネットワーク機器"
	default:
		return "不明"
	}
}

// firstValidName は候補を順に見て、最初に「もっともらしい」ものを返す。
//
// 🔴 **実機で踏んだ問題**: 一部の機器(壊れた/簡易なmDNS実装を積んだプリンタ等)が
// 途中で切れた応答を返し、"F"のような1文字だけの断片を拾ってしまうことがあった。
// 各取得元(dhcp.leases/DNS/mDNS/NBNS/SSDP)ごとに直すのではなく、
// **採用する直前の1箇所でまとめて検証する**ようにした。
func firstValidName(candidates ...string) string {
	for _, c := range candidates {
		if looksLikeValidName(c) {
			return c
		}
	}
	return ""
}

func looksLikeValidName(s string) bool {
	if len(s) < 2 {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// clientListBudget は clientList 全体の上限。内部のどこかが想定外に詰まっても、
// 画面自体は必ずこの時間内に(エラーであれ)返るようにする。
//
// 🔴 **実機で踏んだ問題(2026-08-12)**: 各ステップは個別には数秒で終わる設計だが、
// 実際に稼働している(実在の端末が20台超いる)LANでは、原因を1箇所に絞り切れない
// 詰まりが起き、呼び出しが数分単位で返らなくなった。各ステップを個別に直すより、
// 「全体に必ず上限を設ける」方を先に固定しておく(原因調査は別途続ける)。
const clientListBudget = 10 * time.Second

func clientList() map[string]any {
	ch := make(chan map[string]any, 1)
	go func() { ch <- clientListImpl() }()
	select {
	case v := <-ch:
		return v
	case <-time.After(clientListBudget):
		return map[string]any{"error": "接続機器一覧の取得がタイムアウトしました。この機体のLANが混み合っている可能性があります。しばらくしてからもう一度お試しください。"}
	}
}

func clientListImpl() map[string]any {
	ttls := arpSweep()

	fdbOut, err := exec.Command("bridge", "fdb", "show", "br", "br-lan").Output()
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("bridge fdb show: %v", err)}
	}

	type fdbEntry struct{ mac, dev string }
	seenMAC := map[string]bool{}
	var entries []fdbEntry
	for _, line := range strings.Split(string(fdbOut), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		// "self" は各ポート自身のアドレス、"permanent" は静的登録
		// (ブリッジ自身のMAC・マルチキャストグループ等)であって、
		// 学習された実在のクライアントではない。両方とも除く。
		if strings.Contains(line, "self") || strings.Contains(line, "permanent") {
			continue
		}
		mac := strings.ToLower(f[0])
		if seenMAC[mac] {
			continue
		}
		var dev string
		for i, tok := range f {
			if tok == "dev" && i+1 < len(f) {
				dev = f[i+1]
			}
		}
		if dev == "" {
			continue
		}
		seenMAC[mac] = true
		entries = append(entries, fdbEntry{mac: mac, dev: dev})
	}

	// ip neigh: MAC → IP。Air6自身がARP済みの相手しか埋まらないベストエフォート。
	ips := map[string]string{}
	if out, err := exec.Command("ip", "neigh", "show", "dev", "br-lan").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			f := strings.Fields(line)
			if len(f) < 1 || strings.Contains(f[0], ":") {
				continue // IPv6は対象外。v4だけ見る。
			}
			for i, tok := range f {
				if tok == "lladdr" && i+1 < len(f) {
					ips[strings.ToLower(f[i+1])] = f[0]
				}
			}
		}
	}

	// dhcp.leases: このAir6自身がDHCPサーバの間だけ埋まるベストエフォート。
	names := map[string]string{}
	if f, err := os.Open("/tmp/dhcp.leases"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fl := strings.Fields(sc.Text())
			if len(fl) >= 4 && fl[3] != "*" {
				names[strings.ToLower(fl[1])] = fl[3]
			}
		}
		f.Close()
	}

	// 無線APのassoclistで、どのMACがどの帯域に無線接続しているかを確定する。
	// (fdbのdevだけでも判別できるが、band名への変換にassoclistではなく
	// wifiAPBandsのdev→band対応をそのまま使う。)
	apBand := wifiAPBands()
	apSSID := wifiAPSSIDs()

	// dhcp.leasesに無い分は、逆引きDNS(PTR)でも試す。
	//
	// Air6自身はDHCPサーバではないが、上位の光回線ルータ(Aterm等)は
	// 自分が配ったDHCPクライアントのホスト名をDNSでも引けるようにしている
	// ことが多い。Air6の/etc/resolv.confはDHCPで得たそのルータを指している
	// はずなので、そこへ問い合わせが飛べば名前が引ける可能性がある。
	// 引けなくても実害はないベストエフォート。
	//
	// 🔴 **実機では上位ルータ(Aterm)が逆引きに非対応でNXDOMAINだった。**
	// そのため、それでも埋まらない分は mDNS(RFC 6762)での逆引きPTRも試す。
	// iOS/macOS等、多くの端末はこちらに応答する。
	//
	// NBNS(NetBIOS、nbns.go)はWindows機の名前解決。IPが既に分かっている
	// 相手にユニキャストで直接問い合わせるだけなので単純。SSDP(UPnP)はスマートTV・プリンター等、
	// mDNS/DNSに応答しない機器の最後の手段として追加した。
	//
	// 🔴 **実機で踏んだ問題(2026-08-12)**: この4つは互いに独立しているのに
	// 逐次実行だと待ち時間(最大 2+1.5+1.2+4 秒程度)がそのまま積み上がる。
	// 実機の稼働LAN(実在の端末が20台超)のような賑やかな環境ではそれぞれが
	// 上限いっぱいまでかかりやすく、画面が返るまで数十秒単位になっていた。
	// 独立クエリなので並列に投げ、一番遅いものの時間だけで済ませる。
	var pending []string
	for _, ip := range ips {
		pending = append(pending, ip)
	}
	var ptrNames, mdnsNames, nbnsNames, ssdpNames map[string]string
	var lookupWG sync.WaitGroup
	lookupWG.Add(4)
	go func() { defer lookupWG.Done(); ptrNames = reverseLookup(ips) }()
	go func() { defer lookupWG.Done(); mdnsNames = mdnsReverseLookup(pending) }()
	go func() { defer lookupWG.Done(); nbnsNames = nbnsLookup(pending) }()
	go func() { defer lookupWG.Done(); ssdpNames = ssdpDiscover() }()
	lookupWG.Wait()
	notes := clientNotes()

	var list []clientEntry
	for _, e := range entries {
		link := "wired"
		ssid := ""
		if b, ok := apBand[e.dev]; ok {
			link = b
			ssid = apSSID[e.dev]
		}
		ip := ips[e.mac]
		name := firstValidName(names[e.mac], ptrNames[e.mac])
		if name == "" && ip != "" {
			name = firstValidName(mdnsNames[ip], nbnsNames[ip], ssdpNames[ip])
		}
		if name == "" {
			name = "-"
		}
		os := ""
		if ip != "" {
			os = guessOSFromTTL(ttls[ip])
		}
		if ip == "" {
			ip = "-"
		}
		list = append(list, clientEntry{
			Name: name, MAC: e.mac, IP: ip, Link: link, SSID: ssid,
			Vendor: macVendor(e.mac), Note: notes[e.mac], OS: os,
		})
	}
	return map[string]any{"clients": list}
}

// reverseLookup は mac→ip の対応表を受け取り、引けた分だけ mac→ホスト名 を返す。
// 各問い合わせは短いタイムアウトで打ち切り、全体もタイムアウトで束ねる
// (DNSが落ちていても画面全体が固まらないように)。
func reverseLookup(ips map[string]string) map[string]string {
	resolver := &net.Resolver{PreferGo: true}
	out := map[string]string{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for mac, ip := range ips {
		wg.Add(1)
		go func(mac, ip string) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
			defer cancel()
			names, err := resolver.LookupAddr(ctx, ip)
			if err != nil || len(names) == 0 {
				return
			}
			name := strings.TrimSuffix(names[0], ".")
			mu.Lock()
			out[mac] = name
			mu.Unlock()
		}(mac, ip)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	return out
}
