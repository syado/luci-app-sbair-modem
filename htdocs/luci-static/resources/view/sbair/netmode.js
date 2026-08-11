// SPDX-License-Identifier: MIT
// Copyright (c) 2026 syado
//
// SIMルータ / 光回線AP化(ダンブAP)の手動切替。
// 実体は /usr/sbin/sbair-netmode(recovery/files/usr/sbin/sbair-netmode)。
//
// ✅ 光回線APモードでAir6自身が外部と通信できない問題は解決済み
// (真因はAterm側のIPv4パケットフィルタとDHCP固定割り当ての衝突。
// docs/SESSION_2026-08-09_FRESH_UNIT_LUCI.md §6-8参照)。

'use strict';
'require view';
'require rpc';
'require ui';
'require dom';
'require tools.sbair as sbair';

var callStatus = rpc.declare({ object: 'sbair', method: 'netmode_status' });
var callSet = rpc.declare({ object: 'sbair', method: 'netmode_set', params: [ 'mode' ] });

var modes = [
	{
		key: 'sim', label: 'SIMルータ',
		desc: 'SIM(セルラー)をWANとして使う、通常のルータ運用。192.168.3.1 / 自前DHCP。',
		warn: '切り替えるとLAN側は 192.168.3.1 に戻ります。'
	},
	{
		key: 'ap', label: '光回線AP(ダンブAP)',
		desc: '自前のDHCPを止め、既存の光回線ルータ側のネットワークへブリッジする。LAN機器・Air6自身ともにインターネットに出られる。',
		warn: '切り替えた瞬間 192.168.3.1 では到達できなくなります。接続先のDHCPリース一覧で新しいIPを確認してください。LANケーブルは切り替え後に挿すこと(先に挿すとDHCPサーバーが二重稼働します)。'
	}
];

function render(data, onSwitch) {
	data = data || {};
	var body = [];

	if (data.error) {
		body.push(sbair.errorBox([ data.error ]));
		return body;
	}

	var rows = modes.map(function(m) {
		var current = (data.mode === m.key);
		return E('tr', { 'class': 'tr' }, [
			E('td', { 'class': 'td left' }, [
				m.label,
				current ? E('span', { 'class': 'ifacebadge', 'style': 'margin-left:.5em;background:#5cb85c;color:#fff' }, '現在のモード') : ''
			]),
			E('td', { 'class': 'td left' }, m.desc),
			E('td', { 'class': 'td left' }, [
				E('button', {
					'class': current ? 'cbi-button' : 'cbi-button cbi-button-action',
					'disabled': current ? '' : null,
					'click': function() { onSwitch(m); }
				}, current ? '適用中' : 'このモードにする')
			])
		]);
	});

	body.push(sbair.section('接続モード', [
		sbair.table([ E('tr', { 'class': 'tr table-titles' }, [
			E('th', { 'class': 'th' }, 'モード'),
			E('th', { 'class': 'th' }, '説明'),
			E('th', { 'class': 'th' }, '')
		]) ].concat(rows))
	]));

	return body;
}

return view.extend({
	load: function() {
		return callStatus().catch(function(err) {
			return { error: String(err) };
		});
	},

	render: function(data) {
		var self = this;
		self.data = data;

		var container = E('div', {});
		var redraw = function() { dom.content(container, render(self.data, onSwitch)); };

		var reload = function() {
			return callStatus().then(function(res) {
				self.data = res;
				redraw();
			}).catch(function(err) {
				ui.addNotification(null, E('p', {}, String(err)), 'warning');
			});
		};

		function onSwitch(m) {
			if (!confirm(m.label + ' に切り替えます。\n\n' + m.warn + '\n\nよろしいですか?'))
				return;
			callSet(m.key).then(function(res) {
				if (res && res.error) {
					ui.addNotification(null, E('p', {}, res.error), 'danger');
					return;
				}
				ui.addNotification(null, E('p', {}, '切り替えました: ' + (res.output || '')), 'info');
				return reload();
			}).catch(function(err) {
				ui.addNotification(null, E('p', {}, String(err)), 'danger');
			});
		}

		redraw();

		return E('div', { 'class': 'cbi-map' }, [
			E('div', { 'style': 'margin-bottom:1em' }, [
				E('button', {
					'class': 'cbi-button cbi-button-neutral',
					'click': ui.createHandlerFn(this, reload)
				}, '再読み込み')
			]),
			container
		]);
	},

	handleSave: null,
	handleSaveApply: null,
	handleReset: null
});
