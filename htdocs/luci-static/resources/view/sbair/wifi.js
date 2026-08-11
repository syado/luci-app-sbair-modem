// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912
//
// Wi-Fi の表示 + 編集(Phase 2)。
//
// mt_wifi は netifd の無線ドライバスクリプトに未対応で、LuCI標準の
// Network → Wireless では触れない(docs/WIFI_SUPPORT.md)。ここでは
// uci の wireless 設定を直接読み書きする自前の画面を用意している。
//
// 🔴 **チャンネルはインターフェース単位ではなく無線(radio)単位**で、
// `uci set wireless.<iface>.channel` は効かない(docs/OPENWRT_WIRELESS.md)。
// 純正UI同様 `knsh wlan <band> channel <値>` を使うので、band別に別枠で編集する。
//
// バッチ編集: SSID/暗号方式/パスワード/ステルス/無効化(テーブル行内で直接編集、
// 2026-08-10にモーダル方式から変更)・チャンネル・帯域幅・通信規格のどれを触っても、
// その場ではサーバへ送らずこの画面のローカル状態(pendingIface/pendingBand)に
// 溜めておくだけにする。画面上部の「変更を適用」ボタンを押した時に初めて、
// 溜まった変更をすべて apply="0"(反映を保留)で送ってから、最後に1回だけ
// wifi_apply(knsh save + knsh wlan restart)を呼ぶ。これで複数箇所を
// まとめて変えても、Wi-Fiが数秒切断される瞬間は1回だけで済む
// (2026-08-10、/lib/wifi/mtwifi.lua の解析で「保存だけでは反映されず、
// restartでドライバを再読み込みすれば本体再起動なしで反映できる」と判明した後の対応。
// docs/KNSH_COMMAND_AUDIT.md §6-4参照)。

'use strict';
'require view';
'require rpc';
'require ui';
'require dom';
'require tools.sbair as sbair';

var callWifiStatus = rpc.declare({ object: 'sbair', method: 'wifi_status' });
var callWifiSet = rpc.declare({
	object: 'sbair', method: 'wifi_set',
	params: [ 'iface', 'ssid', 'hidden', 'disabled', 'password', 'encryption', 'apply' ]
});
var callWifiSetChannel = rpc.declare({
	object: 'sbair', method: 'wifi_set_channel',
	params: [ 'band', 'channel', 'apply' ]
});
var callWifiSetBandwidth = rpc.declare({
	object: 'sbair', method: 'wifi_set_bandwidth',
	params: [ 'band', 'width', 'apply' ]
});
var callWifiSetProtocol = rpc.declare({
	object: 'sbair', method: 'wifi_set_protocol',
	params: [ 'band', 'protocol', 'apply' ]
});
var callWifiApply = rpc.declare({ object: 'sbair', method: 'wifi_apply' });
var callReboot = rpc.declare({ object: 'sbair', method: 'system_reboot' });

var bandLabel = { '2.4G': '2.4GHz', '5G': '5GHz', '6G': '6GHz' };

var encryptionOptions = [
	{ value: 'sae', label: 'WPA3-SAE' },
	{ value: 'sae-mixed', label: 'WPA2/WPA3(混在)' },
	{ value: 'psk2+ccmp', label: 'WPA2-PSK' },
	{ value: 'psk-mixed+ccmp', label: 'WPA/WPA2(混在)' },
	{ value: 'owe', label: 'OWE(Enhanced Open)' },
	{ value: 'none', label: '暗号化なし' }
];

// 純正UIの実際のテンプレート(angouka.phtml)では2.4GHz/5GHzに「WPA3単体」「OWE」の
// 選択肢が無いが、実機で試したところ普通に動作することを確認した(2026-08-09)。
// 純正UIに無いのはハードウェア制約ではなく製品判断(互換性重視)と判断し、
// このアプリでは2.4G/5Gにも解禁する。WEP・WPA(TKIP)単体は脆弱なため除外のまま。
function encryptionOptionsForBand(band) {
	var allowed = (band === '6G')
		? ['owe', 'sae']
		: ['sae-mixed', 'psk-mixed+ccmp', 'sae', 'owe', 'none'];
	return encryptionOptions.filter(function(o) { return allowed.indexOf(o.value) >= 0; });
}

// 日本国内(技適)で使える範囲を基準にした選択肢。0=自動。
// 5GHz/6GHzはDFS/未検証帯域を含め一通り並べているが、実際に選べるかは
// 地域設定(country)とハードウェアの対応状況による。
var channelChoices = {
	'2.4G': [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13],
	'5G': [0, 36, 40, 44, 48, 52, 56, 60, 64, 100, 104, 108, 112, 116, 120, 124, 128, 132, 136, 140, 144],
	'6G': [0, 5, 21, 37, 53, 69, 85, 101, 117, 133, 149, 165, 181, 197, 213]
};

// 帯域ごとに選べる帯域幅(MHz)。2.4GHzは20/40のみ、6GHzは320まで選べる
// (実機のPHPソース Wlan.class.php の set_bandwith と同じ組み合わせ)。
var bandwidthChoices = {
	'2.4G': ['20', '40'],
	'5G': ['20', '40', '80', '160'],
	'6G': ['20', '40', '80', '160', '320']
};

// 帯域ごとに選べる通信規格(実機のPHPソース Wlan.class.php の
// CFG_MODE_2G_*/CFG_MODE_5G_*/CFG_MODE_6G_* とそのまま対応)。
var protocolChoices = {
	'2.4G': [
		{ value: 'b', label: '802.11b' },
		{ value: 'bg', label: '802.11b/g' },
		{ value: 'n', label: '802.11n/b/g' },
		{ value: 'ax', label: '802.11ax/n/b/g' },
		{ value: 'be', label: '802.11be(Wi-Fi 7)' }
	],
	'5G': [
		{ value: 'a', label: '802.11a' },
		{ value: 'n', label: '802.11n/a' },
		{ value: 'ac', label: '802.11ac/n/a' },
		{ value: 'ax', label: '802.11ax/ac/n/a' },
		{ value: 'be', label: '802.11be(Wi-Fi 7)' }
	],
	'6G': [
		{ value: 'ax', label: '802.11ax' },
		{ value: 'be', label: '802.11be(Wi-Fi 7)' }
	]
};

// SSID・暗号方式・パスワード・ステルス・無効化のすべてを行内で直接編集できる
// テーブル(2026-08-10、モーダル方式から変更)。channelTableと同じく、
// 各入力欄の変更はその場ではサーバへ送らずonIfaceChangeでpendingIfaceに
// 溜めるだけにする。SSID/パスワードの入力欄は'change'(blur/確定時)で
// 拾う — 'input'(1文字ごと)にすると、そのたびにredraw()がテーブル全体を
// 作り直してしまい、入力中のフォーカスが飛んで打てなくなるため。
function ifaceTable(list, pendingIface, onIfaceChange) {
	if (!list || !list.length)
		return E('p', {}, 'Wi-Fiインターフェースを取得できませんでした。');

	var rows = [ E('tr', { 'class': 'tr table-titles' }, [
		E('th', { 'class': 'th' }, '帯域'),
		E('th', { 'class': 'th' }, 'SSID'),
		E('th', { 'class': 'th' }, '暗号方式'),
		E('th', { 'class': 'th' }, 'パスワード'),
		E('th', { 'class': 'th' }, 'ステルス'),
		E('th', { 'class': 'th' }, '無効化'),
		E('th', { 'class': 'th' }, '')
	]) ];

	list.forEach(function(w) {
		var pend = pendingIface[w.iface];
		// 表示は「保留中の変更」があればそちらを優先する(適用ボタンを押すまで
		// サーバへは送らないが、画面には反映予定の内容を見せる)。
		var ssid = (pend && pend.ssid !== undefined) ? pend.ssid : w.ssid;
		var hidden = (pend && pend.hidden !== undefined) ? (pend.hidden === '1') : w.hidden;
		var disabled = (pend && pend.disabled !== undefined) ? (pend.disabled === '1') : w.disabled;
		var encryption = (pend && pend.encryption !== undefined) ? pend.encryption : w.encryption;

		var ssidInput = E('input', {
			'class': 'cbi-input-text', 'type': 'text', 'value': ssid || '', 'style': 'width:100%'
		});
		ssidInput.addEventListener('change', function() { onIfaceChange(w.iface, 'ssid', ssidInput.value); });

		var encInput = E('select', { 'class': 'cbi-input-select' },
			encryptionOptionsForBand(w.band).map(function(o) {
				return E('option', {
					'value': o.value,
					'selected': (o.value === encryption) ? '' : null
				}, o.label);
			})
		);
		encInput.addEventListener('change', function() { onIfaceChange(w.iface, 'encryption', encInput.value); });

		var pwInput = E('input', {
			'class': 'cbi-input-text', 'type': 'password',
			'value': (pend && pend.password) ? pend.password : '',
			'placeholder': '変更する場合のみ', 'style': 'width:100%'
		});
		pwInput.addEventListener('change', function() { onIfaceChange(w.iface, 'password', pwInput.value); });

		var hiddenInput = E('input', {
			'class': 'cbi-input-checkbox', 'type': 'checkbox',
			'checked': hidden ? '' : null
		});
		hiddenInput.addEventListener('change', function() { onIfaceChange(w.iface, 'hidden', hiddenInput.checked ? '1' : '0'); });

		var disabledInput = E('input', {
			'class': 'cbi-input-checkbox', 'type': 'checkbox',
			'checked': disabled ? '' : null
		});
		disabledInput.addEventListener('change', function() { onIfaceChange(w.iface, 'disabled', disabledInput.checked ? '1' : '0'); });

		rows.push(E('tr', { 'class': 'tr' }, [
			E('td', { 'class': 'td left' }, bandLabel[w.band] || w.band || '-'),
			E('td', { 'class': 'td left' }, ssidInput),
			E('td', { 'class': 'td left' }, encInput),
			E('td', { 'class': 'td left' }, pwInput),
			E('td', { 'class': 'td left' }, hiddenInput),
			E('td', { 'class': 'td left' }, disabledInput),
			E('td', { 'class': 'td left' }, pend
				? E('span', { 'class': 'ifacebadge', 'style': 'background:#f0ad4e;color:#fff' }, '適用待ち')
				: '')
		]));
	});
	return sbair.table(rows);
}

// radios(帯域ごとの現在値)+ pendingBand(保留中の変更)から、選択中として
// 見せるべき値を1つ決める。
function effectiveBandValue(r, pendingBand, field, fallback) {
	var pend = pendingBand[r.band];
	if (pend && pend[field] !== undefined)
		return pend[field];
	return r[field] || fallback;
}

function channelTable(radios, pendingBand, onBandChange) {
	if (!radios || !radios.length)
		return E('p', {}, '無線デバイスの情報を取得できませんでした。');

	var rows = [ E('tr', { 'class': 'tr table-titles' }, [
		E('th', { 'class': 'th' }, '帯域'),
		E('th', { 'class': 'th' }, '通信規格'),
		E('th', { 'class': 'th' }, 'チャンネル'),
		E('th', { 'class': 'th' }, '帯域幅'),
		E('th', { 'class': 'th' }, '')
	]) ];

	radios.forEach(function(r) {
		var curProto = effectiveBandValue(r, pendingBand, 'protocol', '');
		var curCh = effectiveBandValue(r, pendingBand, 'channel', '0');
		var curBw = effectiveBandValue(r, pendingBand, 'bandwidth', '20');
		var pend = pendingBand[r.band];

		var protoInput = E('select', { 'class': 'cbi-input-select' },
			(protocolChoices[r.band] || []).map(function(p) {
				return E('option', {
					'value': p.value,
					'selected': (p.value === curProto) ? '' : null
				}, p.label);
			})
		);
		var chInput = E('select', { 'class': 'cbi-input-select' },
			(channelChoices[r.band] || [0]).map(function(ch) {
				return E('option', {
					'value': String(ch),
					'selected': (String(ch) === curCh) ? '' : null
				}, ch === 0 ? '自動' : ('ch ' + ch));
			})
		);
		var bwInput = E('select', { 'class': 'cbi-input-select' },
			(bandwidthChoices[r.band] || ['20']).map(function(bw) {
				return E('option', {
					'value': bw,
					'selected': (bw === curBw) ? '' : null
				}, bw + 'MHz');
			})
		);
		protoInput.addEventListener('change', function() { onBandChange(r.band, 'protocol', protoInput.value); });
		chInput.addEventListener('change', function() { onBandChange(r.band, 'channel', chInput.value); });
		bwInput.addEventListener('change', function() { onBandChange(r.band, 'bandwidth', bwInput.value); });

		rows.push(E('tr', { 'class': 'tr' }, [
			E('td', { 'class': 'td left' }, bandLabel[r.band] || r.band || '-'),
			E('td', { 'class': 'td left' }, protoInput),
			E('td', { 'class': 'td left' }, chInput),
			E('td', { 'class': 'td left' }, bwInput),
			E('td', { 'class': 'td left' }, pend
				? E('span', { 'class': 'ifacebadge', 'style': 'background:#f0ad4e;color:#fff' }, '適用待ち')
				: '')
		]));
	});
	return sbair.table(rows);
}

function applyBar(pendingCount, onApply, onDiscard) {
	if (pendingCount === 0)
		return E('div', { 'style': 'margin:.5em 0;opacity:.7' },
			'未適用の変更はありません。SSID編集やチャンネル/帯域幅/通信規格の変更は、ここで「変更を適用」を押すまで実機には反映されません。');

	return E('div', {
		'class': 'alert-message notice',
		'style': 'margin:.5em 0;display:flex;align-items:center;gap:.6em'
	}, [
		E('span', {}, '未適用の変更が' + pendingCount + '件あります。'),
		E('button', { 'class': 'cbi-button cbi-button-positive', 'click': onApply }, '変更を適用'),
		E('button', { 'class': 'cbi-button cbi-button-neutral', 'click': onDiscard }, '破棄')
	]);
}

function manualRebootBox(onReboot) {
	return E('div', { 'style': 'margin:.5em 0;opacity:.8' }, [
		E('span', {}, '変更の適用時はWi-Fiドライバだけを再読み込みします(数秒切断されます。本体再起動は不要)。'),
		' ',
		E('span', {}, 'それでも反映されない場合(SSIDの追加/削除時など)は、'),
		E('button', {
			'class': 'cbi-button cbi-button-neutral',
			'style': 'margin-left:.3em',
			'click': onReboot
		}, '本体を再起動'),
		E('span', {}, ' してください。')
	]);
}

function render(data, opts) {
	data = data || {};
	opts = opts || {};
	var body = [];

	body.push(E('p', { 'style': 'opacity:.8' },
		'この機体の無線ドライバ(mt_wifi)は OpenWrt 標準の Network → Wireless 画面に対応していないため、' +
		'ここで直接編集します。'));
	body.push(manualRebootBox(opts.onReboot));
	body.push(applyBar(opts.pendingCount, opts.onApply, opts.onDiscard));

	body.push(sbair.section('Wi-Fi', [ ifaceTable(data.ifaces, opts.pendingIface, opts.onIfaceChange) ]));
	body.push(sbair.section('通信規格・チャンネル・帯域幅(帯域ごと・全SSID共通)', [
		channelTable(data.radios, opts.pendingBand, opts.onBandChange)
	]));
	body.push(sbair.errorBox(data.error ? [ data.error ] : null));
	return body;
}

return view.extend({
	load: function() {
		return callWifiStatus().catch(function(err) {
			return { error: String(err) };
		});
	},

	render: function(data) {
		var self = this;
		self.data = data;
		self.pendingIface = {};   // { [iface]: {ssid,hidden,disabled,password,encryption} }
		self.pendingBand = {};    // { [band]: {channel?, bandwidth?, protocol?} }

		var container = E('div', {});

		function pendingCount() {
			return Object.keys(self.pendingIface).length + Object.keys(self.pendingBand).length;
		}

		var redraw = function() {
			dom.content(container, render(self.data, {
				pendingIface: self.pendingIface,
				pendingBand: self.pendingBand,
				pendingCount: pendingCount(),
				onIfaceChange: onIfaceChange,
				onBandChange: onBandChange,
				onApply: applyAll,
				onDiscard: discardAll,
				onReboot: doReboot
			}));
		};

		var reload = function() {
			return callWifiStatus().then(function(res) {
				self.data = res;
				self.pendingIface = {};
				self.pendingBand = {};
				redraw();
			}).catch(function(err) {
				ui.addNotification(null, E('p', {}, String(err)), 'warning');
			});
		};

		function onIfaceChange(iface, field, value) {
			if (!self.pendingIface[iface])
				self.pendingIface[iface] = {};
			self.pendingIface[iface][field] = value;
			redraw();
		}

		function onBandChange(band, field, value) {
			if (!self.pendingBand[band])
				self.pendingBand[band] = {};
			self.pendingBand[band][field] = value;
			redraw();
		}

		function discardAll() {
			self.pendingIface = {};
			self.pendingBand = {};
			redraw();
		}

		// 保留中の変更をすべて apply="0" で送ってから、最後に1回だけ
		// wifi_apply(knsh save + knsh wlan restart)を呼ぶ。
		function applyAll() {
			var ifaceKeys = Object.keys(self.pendingIface);
			var bandKeys = Object.keys(self.pendingBand);
			if (ifaceKeys.length === 0 && bandKeys.length === 0)
				return;

			var tasks = [];
			ifaceKeys.forEach(function(iface) {
				var p = self.pendingIface[iface];
				tasks.push(callWifiSet(iface, p.ssid, p.hidden, p.disabled, p.password, p.encryption, '0'));
			});
			bandKeys.forEach(function(band) {
				var p = self.pendingBand[band];
				if (p.protocol !== undefined)
					tasks.push(callWifiSetProtocol(band, p.protocol, '0'));
				if (p.channel !== undefined)
					tasks.push(callWifiSetChannel(band, p.channel, '0'));
				if (p.bandwidth !== undefined)
					tasks.push(callWifiSetBandwidth(band, p.bandwidth, '0'));
			});

			Promise.all(tasks).then(function(results) {
				var err = results.map(function(r) { return r && r.error; }).filter(Boolean)[0];
				if (err) {
					ui.addNotification(null, E('p', {}, err), 'danger');
					return;
				}
				return callWifiApply().then(function(res) {
					if (res && res.error) {
						ui.addNotification(null, E('p', {}, res.error), 'danger');
						return;
					}
					ui.addNotification(null, E('p', {},
						'設定を反映しました。Wi-Fiが数秒切断・再接続されます。'), 'info');
					return reload();
				});
			}).catch(function(err) {
				ui.addNotification(null, E('p', {}, String(err)), 'danger');
			});
		}

		function doReboot() {
			if (!confirm('本体を再起動します。よろしいですか?(Wi-Fi・LANとも一時的に切断されます)'))
				return;
			callReboot().then(function() {
				ui.addNotification(null, E('p', {}, '再起動しています。1〜2分後にページを再読み込みしてください。'), 'info');
			}).catch(function(err) {
				ui.addNotification(null, E('p', {}, String(err)), 'warning');
			});
		}

		redraw();

		return E('div', { 'class': 'cbi-map' }, [
			E('div', { 'style': 'margin-bottom:1em' }, [
				E('button', {
					'class': 'cbi-button cbi-button-neutral',
					'click': ui.createHandlerFn(this, reload)
				}, '再読み込み(未適用の変更は破棄されます)')
			]),
			container
		]);
	},

	handleSave: null,
	handleSaveApply: null,
	handleReset: null
});
