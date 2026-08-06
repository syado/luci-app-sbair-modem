// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912
//
// 3 つのタブで共通に使う小物。

'use strict';
'require baseclass';

// 識別子は既定で伏せる。IMEI / IMSI / ICCID / EID / Cell ID は
// 設置場所と契約者の特定に直結するので、画面を撮って貼る前提で既定を選ぶ。
// タブをまたいで同じ状態を使う。
var revealed = false;

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

	row: function(label, value, extra) {
		return E('tr', { 'class': 'tr' }, [
			E('td', { 'class': 'td left', 'width': '35%' }, label),
			E('td', { 'class': 'td left' }, [
				(value === undefined || value === null || value === '') ? '-' : String(value),
				extra ? E('span', { 'class': 'ifacebadge', 'style': 'margin-left:.5em' }, extra) : ''
			])
		]);
	},

	section: function(title, children) {
		return E('div', { 'class': 'cbi-section' }, [ E('h3', {}, title) ].concat(children));
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
		return E('div', { 'class': 'cbi-section' }, [
			E('h3', {}, '取得できなかった項目'),
			E('pre', { 'style': 'white-space:pre-wrap' }, errors.join('\n'))
		]);
	}
});
