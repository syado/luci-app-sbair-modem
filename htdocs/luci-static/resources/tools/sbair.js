// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912
//
// タブで共通に使う小物。

'use strict';
'require baseclass';

// 識別子は既定で伏せる。IMEI / IMSI / ICCID / EID / Cell ID は
// 設置場所と契約者の特定に直結するので、画面を撮って貼る前提で既定を選ぶ。
// タブをまたいで同じ状態を使う。
var revealed = false;

// セクションの余白。**1 回だけ差し込む。** node で描画を試すときは
// document が無いので、その場合は何もしない。
var sectionStyled = false;
function injectSectionStyle() {
	if (sectionStyled || typeof document === 'undefined')
		return;
	sectionStyled = true;
	var s = document.createElement('style');
	s.textContent =
		'.sbair-section{margin-bottom:18px}' +
		'.sbair-section > .table:last-child,.sbair-section > pre:last-child{margin-bottom:0}';
	document.head.appendChild(s);
}

return baseclass.extend({
	isRevealed: function() {
		return revealed;
	},

	setRevealed: function(v) {
		revealed = !!v;
	},

	// **短いものほど伏せる必要がある。** Cell ID はちょうど 8 文字なので、
	// 「8 文字以下は素通し」にすると一番隠したいものが常に出てしまう。
	mask: function(s) {
		if (!s)
			return '-';
		s = String(s);
		if (revealed)
			return s;
		if (s.length <= 4)
			return '*'.repeat(s.length);
		if (s.length < 12)
			return s.slice(0, 4) + '*'.repeat(s.length - 4);
		// ICCID のように長いものは末尾 2 桁だけ残す。一覧で見分けが付く。
		return s.slice(0, 4) + '*'.repeat(s.length - 6) + s.slice(-2);
	},

	// ⚠ **値を無条件に String() しないこと。** 呼び出し側はバッジの並びなど
	// DOM ノードも渡す。文字列化すると `[object HTMLSpanElement]` が並ぶ
	// (実際にバンドの行でこれを出した)。ノードはそのまま通す。
	row: function(label, value, extra) {
		var v;
		if (value === undefined || value === null || value === '')
			v = '-';
		else if (typeof Node === 'function' && value instanceof Node)
			v = value;
		else
			v = String(value);
		return E('tr', { 'class': 'tr' }, [
			E('td', { 'class': 'td left', 'width': '35%' }, label),
			E('td', { 'class': 'td left' }, [
				v,
				extra ? E('span', { 'class': 'ifacebadge', 'style': 'margin-left:.5em' }, extra) : ''
			])
		]);
	},

	// ⚠ **セクションの下の余白は表が持っている。** テーマの CSS には
	// `.cbi-section` にも `h3` にも余白が無く、`.table { margin-bottom: 18px }`
	// だけが間隔を作っている。**だから表以外(説明文やボタン)で終わると
	// 次のタイトルが直に続いてしまう。**
	//
	// 余白をセクション側へ移し、**表が最後のときはその余白を消して**
	// 二重にしない。どちらで終わっても同じ 18px になる。
	section: function(title, children) {
		injectSectionStyle();
		return E('div', { 'class': 'cbi-section sbair-section' },
			[ E('h3', {}, title) ].concat(children));
	},

	table: function(rows) {
		return E('table', { 'class': 'table' }, rows);
	},

	// 識別子の表示切替。押されたら再描画を呼び出し元に任せる。
	revealToggle: function(label, redraw) {
		return E('label', { 'style': 'display:block;margin-bottom:1em' }, [
			E('input', {
				'type': 'checkbox',
				'class': 'cbi-input-checkbox',
				'checked': revealed ? '' : null,
				'change': function(ev) {
					revealed = ev.target.checked;
					redraw();
				}
			}),
			' ' + label
		]);
	},

	// Network → Wireless と同じ見せ方の信号バッジ。
	// LuCI が持っている signal-*.png をそのまま使うので追加の画像は要らない。
	//
	// **セルラーには「品質 %」が無い。** Wi-Fi は signal/noise から出せるが、
	// こちらは RSRP を段階に割り当てるしかない。-110 dBm を下限、
	// -80 dBm を上限とする。
	signalPercent: function(dbm) {
		var v = parseFloat(dbm);
		if (isNaN(v))
			return null;
		return Math.max(0, Math.min(100, Math.round((v + 110) / 30 * 100)));
	},

	signalIcon: function(pct) {
		var name = 'signal-none';
		if (pct !== null) {
			if (pct <= 0)      name = 'signal-0';
			else if (pct < 25) name = 'signal-0-25';
			else if (pct < 50) name = 'signal-25-50';
			else if (pct < 75) name = 'signal-50-75';
			else               name = 'signal-75-100';
		}
		return L.resource('icons/%s.png'.format(name));
	},

	signalBadge: function(dbm, label) {
		var pct = this.signalPercent(dbm);
		return E('span', {
			'class': 'ifacebadge',
			'title': (dbm != null && dbm !== '') ? (dbm + ' dBm') : '不明'
		}, [
			E('img', { 'src': this.signalIcon(pct) }),
			' ',
			(pct === null) ? '不明' : (pct + '%'),
			label ? E('span', { 'style': 'margin-left:.5em;opacity:.7' }, label) : ''
		]);
	},

	simMappingLabel: function(n) {
		if (n === 1) return '物理スロット (uSIM)';
		if (n === 2) return '内蔵 eSIM';
		return n ? ('不明 (' + n + ')') : '-';
	},

	// 取得できなかった AT を出す。モデムが黙っているのか、こちらが読めて
	// いないのかを区別できるようにするため。
	errorBox: function(errors) {
		if (!errors || !errors.length)
			return '';
		injectSectionStyle();
		return E('div', { 'class': 'cbi-section sbair-section' }, [
			E('h3', {}, '取得できなかった項目'),
			E('pre', { 'style': 'white-space:pre-wrap' }, errors.join('\n'))
		]);
	}
});
