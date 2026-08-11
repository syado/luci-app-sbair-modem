// SPDX-License-Identifier: MIT
// Copyright (c) 2026 syado
//
// br-lan に今いる端末の一覧(有線・無線問わない)。
//
// 正はブリッジのFDB(clients.go参照)。IP/名前/メーカーは分かる範囲での
// ベストエフォート(DHCPリース・DNS逆引き・mDNS・NetBIOS・SSDP)。
// 列見出しをクリックすると並べ替えられる。メモは自由入力で保存できる
// (/etc/config/sbair)。ポートスキャンは個別にオンデマンドで実行する。

'use strict';
'require view';
'require rpc';
'require ui';
'require dom';
'require tools.sbair as sbair';

var callClientList = rpc.declare({ object: 'sbair', method: 'client_list' });
var callNoteSet = rpc.declare({ object: 'sbair', method: 'client_note_set', params: [ 'mac', 'note' ] });
var callScanPorts = rpc.declare({ object: 'sbair', method: 'client_scan_ports', params: [ 'ip' ] });
var callDisconnect = rpc.declare({ object: 'sbair', method: 'client_disconnect', params: [ 'mac' ] });

var bandLabel = { '2.4G': '2.4GHz', '5G': '5GHz', '6G': '6GHz' };

function linkLabel(link) {
	if (link === 'wired')
		return '有線';
	return bandLabel[link] || link || '-';
}

var columns = [
	{ key: 'name', label: '名前' },
	{ key: 'ip', label: 'IPアドレス', numeric: true },
	{ key: 'mac', label: 'MACアドレス' },
	{ key: 'vendor', label: 'メーカー' },
	{ key: 'os', label: 'OS(推定)' },
	{ key: 'link', label: '接続方式' },
	{ key: 'ssid', label: 'SSID' },
	{ key: 'note', label: 'メモ' }
];

// IPは文字列比較だと "192.168.0.9" > "192.168.0.10" になってしまうので、
// オクテットごとの数値比較にする。"-"(未解決)は常に末尾へ。
function ipKey(s) {
	if (!s || s === '-')
		return null;
	var parts = s.split('.').map(Number);
	if (parts.length !== 4 || parts.some(isNaN))
		return null;
	return parts[0] * 0x1000000 + parts[1] * 0x10000 + parts[2] * 0x100 + parts[3];
}

function sortClients(list, key, dir) {
	var col = columns.filter(function(c) { return c.key === key; })[0];
	var sorted = (list || []).slice();
	sorted.sort(function(a, b) {
		var av = a[key], bv = b[key];
		if (col && col.numeric) {
			var ak = ipKey(av), bk = ipKey(bv);
			if (ak === null && bk === null) return 0;
			if (ak === null) return 1;  // 未解決は末尾
			if (bk === null) return -1;
			return (ak - bk) * dir;
		}
		av = (av === undefined || av === null || av === '') ? '-' : String(av);
		bv = (bv === undefined || bv === null || bv === '') ? '-' : String(bv);
		if (key === 'link') { av = linkLabel(av); bv = linkLabel(bv); }
		return av.localeCompare(bv) * dir;
	});
	return sorted;
}

function showPortScanResult(ip, res) {
	if (res.error) {
		ui.showModal(ip + ' のポートスキャン', [
			E('p', {}, res.error),
			E('div', { 'style': 'text-align:right' }, [
				E('button', { 'class': 'cbi-button', 'click': function() { ui.hideModal(); } }, '閉じる')
			])
		]);
		return;
	}
	var open = res.open || [];
	var body = !open.length
		? E('p', {}, '開いているポートは見つかりませんでした(よく使う約25ポートのみ確認)。')
		: sbair.table([ E('tr', { 'class': 'tr table-titles' }, [
			E('th', { 'class': 'th' }, 'ポート'),
			E('th', { 'class': 'th' }, 'サービス'),
			E('th', { 'class': 'th' }, 'バナー')
		]) ].concat(open.map(function(p) {
			return E('tr', { 'class': 'tr' }, [
				E('td', { 'class': 'td left' }, String(p.port)),
				E('td', { 'class': 'td left' }, p.service || '-'),
				E('td', { 'class': 'td left' }, p.banner || '-')
			]);
		})));
	ui.showModal(ip + ' のポートスキャン', [
		body,
		E('div', { 'style': 'text-align:right;margin-top:1em' }, [
			E('button', { 'class': 'cbi-button', 'click': function() { ui.hideModal(); } }, '閉じる')
		])
	]);
}

function clientTable(list, sortKey, sortDir, onSort, onSaveNote, onScan, onDisconnect) {
	if (!list || !list.length)
		return E('p', {}, '接続機器を取得できませんでした(またはまだいません)。');

	var head = columns.map(function(c) {
		var arrow = (c.key === sortKey) ? (sortDir > 0 ? ' ▲' : ' ▼') : '';
		return E('th', {
			'class': 'th',
			'style': 'cursor:pointer;user-select:none',
			'click': function() { onSort(c.key); }
		}, c.label + arrow);
	});
	head.push(E('th', { 'class': 'th' }, ''));
	var rows = [ E('tr', { 'class': 'tr table-titles' }, head) ];

	sortClients(list, sortKey, sortDir).forEach(function(c) {
		var noteInput = E('input', {
			'class': 'cbi-input-text', 'type': 'text', 'style': 'width:10em',
			'value': c.note || ''
		});
		rows.push(E('tr', { 'class': 'tr' }, [
			E('td', { 'class': 'td left' }, c.name || '-'),
			E('td', { 'class': 'td left' }, c.ip || '-'),
			E('td', { 'class': 'td left' }, c.mac || '-'),
			E('td', { 'class': 'td left' }, c.vendor || '-'),
			E('td', { 'class': 'td left' }, c.os || '-'),
			E('td', { 'class': 'td left' }, linkLabel(c.link)),
			E('td', { 'class': 'td left' }, c.ssid || '-'),
			E('td', { 'class': 'td left' }, [
				noteInput,
				' ',
				E('button', {
					'class': 'cbi-button cbi-button-neutral',
					'click': function() { onSaveNote(c.mac, noteInput.value); }
				}, '保存')
			]),
			E('td', { 'class': 'td left' }, [
				E('button', {
					'class': 'cbi-button cbi-button-action',
					'disabled': (!c.ip || c.ip === '-') ? '' : null,
					'click': function() { onScan(c.ip); }
				}, 'スキャン'),
				' ',
				E('button', {
					'class': 'cbi-button cbi-button-remove',
					// 有線接続は切断コマンドの対象外(Wi-Fiクライアントのみ)。
					'disabled': (c.link === 'wired' || !c.mac || c.mac === '-') ? '' : null,
					'click': function() {
						if (confirm((c.name || c.mac) + ' をWi-Fiから切断します。よろしいですか?'))
							onDisconnect(c.mac);
					}
				}, '切断')
			])
		]));
	});
	return sbair.table(rows);
}

function render(data, opts) {
	data = data || {};
	opts = opts || {};
	var body = [];
	body.push(sbair.section('接続機器 (' + ((data.clients || []).length) + '台)', [
		clientTable(data.clients, opts.sortKey, opts.sortDir, opts.onSort, opts.onSaveNote, opts.onScan, opts.onDisconnect)
	]));
	body.push(sbair.errorBox(data.error ? [ data.error ] : null));
	return body;
}

return view.extend({
	sortKey: 'ip',
	sortDir: 1,

	load: function() {
		return callClientList().catch(function(err) {
			return { error: String(err) };
		});
	},

	render: function(data) {
		var self = this;
		self.data = data;

		var container = E('div', {});
		var redraw = function() {
			dom.content(container, render(self.data, {
				sortKey: self.sortKey,
				sortDir: self.sortDir,
				onSort: function(key) {
					if (self.sortKey === key)
						self.sortDir = -self.sortDir;
					else {
						self.sortKey = key;
						self.sortDir = 1;
					}
					redraw();
				},
				onSaveNote: function(mac, note) {
					callNoteSet(mac, note).then(function(res) {
						if (res && res.error) {
							ui.addNotification(null, E('p', {}, res.error), 'danger');
							return;
						}
						return reload();
					}).catch(function(err) {
						ui.addNotification(null, E('p', {}, String(err)), 'danger');
					});
				},
				onScan: function(ip) {
					callScanPorts(ip).then(function(res) {
						showPortScanResult(ip, res || {});
					}).catch(function(err) {
						ui.addNotification(null, E('p', {}, String(err)), 'danger');
					});
				},
				onDisconnect: function(mac) {
					callDisconnect(mac).then(function(res) {
						if (res && res.error) {
							ui.addNotification(null, E('p', {}, res.error), 'danger');
							return;
						}
						ui.addNotification(null, E('p', {}, '切断を要求しました。再接続されるまで少し時間がかかることがあります。'), 'info');
						return reload();
					}).catch(function(err) {
						ui.addNotification(null, E('p', {}, String(err)), 'danger');
					});
				}
			}));
		};

		var reload = function() {
			return callClientList().then(function(res) {
				self.data = res;
				redraw();
			}).catch(function(err) {
				ui.addNotification(null, E('p', {}, String(err)), 'warning');
			});
		};

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
