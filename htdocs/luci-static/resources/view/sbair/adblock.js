// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912
//
// MAC単位の広告ブロック管理画面。SSIDでは分けず、有線・無線・帯域を問わず
// 送信元MACだけで判定する(adblock.go参照)。
//
// 各端末が自分でON/OFFできる自己登録ページ(認証無し、:8090)も別途ある。
// この画面は管理者が全端末を見渡して手動でも切り替えられるようにするもの。

'use strict';
'require view';
'require rpc';
'require ui';
'require dom';
'require tools.sbair as sbair';

var callAdblockList = rpc.declare({ object: 'sbair', method: 'adblock_list' });
var callAdblockSet = rpc.declare({ object: 'sbair', method: 'adblock_set', params: [ 'mac', 'enabled' ] });

var bandLabel = { '2.4G': '2.4GHz', '5G': '5GHz', '6G': '6GHz' };
function linkLabel(link) {
	if (link === 'wired')
		return '有線';
	return bandLabel[link] || link || '-';
}

function clientTable(list, onToggle) {
	if (!list || !list.length)
		return E('p', {}, '接続機器を取得できませんでした(またはまだいません)。');

	var rows = [ E('tr', { 'class': 'tr table-titles' }, [
		E('th', { 'class': 'th' }, '名前'),
		E('th', { 'class': 'th' }, 'IPアドレス'),
		E('th', { 'class': 'th' }, 'MACアドレス'),
		E('th', { 'class': 'th' }, '接続方式'),
		E('th', { 'class': 'th' }, '広告ブロック')
	]) ];

	list.forEach(function(c) {
		rows.push(E('tr', { 'class': 'tr' }, [
			E('td', { 'class': 'td left' }, c.name || '-'),
			E('td', { 'class': 'td left' }, c.ip || '-'),
			E('td', { 'class': 'td left' }, c.mac || '-'),
			E('td', { 'class': 'td left' }, linkLabel(c.link)),
			E('td', { 'class': 'td left' }, [
				E('button', {
					'class': c.adblock ? 'cbi-button cbi-button-positive' : 'cbi-button cbi-button-neutral',
					'click': function() { onToggle(c.mac, !c.adblock); }
				}, c.adblock ? '有効(クリックで無効化)' : '無効(クリックで有効化)')
			])
		]));
	});
	return sbair.table(rows);
}

function render(data, opts) {
	data = data || {};
	opts = opts || {};
	var body = [];

	body.push(E('p', { 'style': 'opacity:.8' },
		'登録した端末(MACアドレス単位)だけ、広告ドメインのDNSを潰します。' +
		'SSIDや有線/無線には関係なく、その端末が使う限り常に効きます。' +
		'DNS over HTTPS/TLSを使うアプリ・ブラウザには効きません。'));

	body.push(E('p', {}, [
		'各端末は管理画面を使わなくても、',
		E('a', { 'href': 'http://' + window.location.hostname + ':8090/', 'target': '_blank' },
			'http://' + window.location.hostname + ':8090/'),
		' を開けば自分の端末だけを自分でON/OFFできます(ログイン不要)。'
	]));

	body.push(sbair.section('接続機器', [ clientTable(data.clients, opts.onToggle) ]));
	body.push(sbair.errorBox(data.error ? [ data.error ] : null));
	return body;
}

return view.extend({
	load: function() {
		return callAdblockList().catch(function(err) {
			return { error: String(err) };
		});
	},

	render: function(data) {
		var self = this;
		self.data = data;

		var container = E('div', {});
		var redraw = function() {
			dom.content(container, render(self.data, { onToggle: onToggle }));
		};

		var reload = function() {
			return callAdblockList().then(function(res) {
				self.data = res;
				redraw();
			}).catch(function(err) {
				ui.addNotification(null, E('p', {}, String(err)), 'warning');
			});
		};

		function onToggle(mac, enabled) {
			callAdblockSet(mac, enabled ? '1' : '0').then(function(res) {
				if (res && res.error) {
					ui.addNotification(null, E('p', {}, res.error), 'danger');
					return;
				}
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
