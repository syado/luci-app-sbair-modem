// SPDX-License-Identifier: MIT
// Copyright (c) 2026 syado
//
// Wi-Fi の追加機能(Phase 3、2026-08-10)。`docs/KNSH_COMMAND_AUDIT.md` §6の
// 逆アセンブル調査と純正UI PHPソースの読み込みで裏取りした`knsh`コマンドを
// 画面化したもの。バックエンドはsbair-modemの`wifi_advanced.go`。
//
// ここのトグルはWi-Fi全体ON/OFFを除きどれもWi-Fi自体を切断しない
// (ドライバ再読み込みが不要)ため、wifi.jsのような「まとめて適用」方式ではなく
// 各項目をその場で反映する単純な作りにしている。

'use strict';
'require view';
'require rpc';
'require ui';
'require dom';
'require tools.sbair as sbair';

var callWifiEnabledStatus = rpc.declare({ object: 'sbair', method: 'wifi_enabled_status' });
var callWifiEnabledSet = rpc.declare({ object: 'sbair', method: 'wifi_enabled_set', params: [ 'enabled' ] });
var callBandsteeringStatus = rpc.declare({ object: 'sbair', method: 'bandsteering_status' });
var callBandsteeringSet = rpc.declare({ object: 'sbair', method: 'bandsteering_set', params: [ 'enabled' ] });
var callIsolationStatus = rpc.declare({ object: 'sbair', method: 'isolation_status' });
var callIsolationSet = rpc.declare({ object: 'sbair', method: 'isolation_set', params: [ 'kind', 'enabled' ] });
var call11rStatus = rpc.declare({ object: 'sbair', method: 'wifi_11r_status' });
var call11rSet = rpc.declare({ object: 'sbair', method: 'wifi_11r_set', params: [ 'enabled' ] });
var callMacfilterStatus = rpc.declare({ object: 'sbair', method: 'macfilter_status' });
var callMacfilterModeSet = rpc.declare({ object: 'sbair', method: 'macfilter_mode_set', params: [ 'enabled' ] });
var callMacfilterAdd = rpc.declare({ object: 'sbair', method: 'macfilter_add', params: [ 'mac', 'enabled' ] });
var callMacfilterDelete = rpc.declare({ object: 'sbair', method: 'macfilter_delete', params: [ 'mac' ] });
var callWpsStatus = rpc.declare({ object: 'sbair', method: 'wps_status' });
var callWpsRun = rpc.declare({ object: 'sbair', method: 'wps_run', params: [ 'band', 'mode', 'pin' ] });
var callWpsPinRandom = rpc.declare({ object: 'sbair', method: 'wps_pin_random' });
var callWpsReset = rpc.declare({ object: 'sbair', method: 'wps_reset', params: [ 'band' ] });

function loadAll() {
	return Promise.all([
		callWifiEnabledStatus().catch(function(e) { return { error: String(e) }; }),
		callBandsteeringStatus().catch(function(e) { return { error: String(e) }; }),
		callIsolationStatus().catch(function(e) { return { error: String(e) }; }),
		call11rStatus().catch(function(e) { return { error: String(e) }; }),
		callMacfilterStatus().catch(function(e) { return { error: String(e) }; }),
		callWpsStatus().catch(function(e) { return { error: String(e) }; })
	]).then(function(r) {
		return { enabled: r[0], bandsteering: r[1], isolation: r[2], dot11r: r[3], macfilter: r[4], wps: r[5] };
	});
}

// switchRow はラベル + トグルスイッチ + 説明文の1行。onChangeはチェック状態(bool)を渡す。
function switchRow(label, desc, checked, onChange, confirmMsg) {
	var input = E('input', {
		'type': 'checkbox', 'class': 'cbi-input-checkbox',
		'checked': checked ? '' : null
	});
	input.addEventListener('change', function() {
		if (confirmMsg && !confirm(confirmMsg)) {
			input.checked = !input.checked;
			return;
		}
		onChange(input.checked);
	});
	return E('div', { 'class': 'cbi-value' }, [
		E('label', { 'class': 'cbi-value-title' }, label),
		E('div', { 'class': 'cbi-value-field' }, [
			input,
			desc ? E('div', { 'style': 'opacity:.7;font-size:.9em;margin-top:.3em' }, desc) : ''
		])
	]);
}

function macFilterTable(list, onDelete) {
	if (!list || !list.length)
		return E('p', { 'style': 'opacity:.7' }, '登録済みのMACアドレスはありません。');
	var rows = [ E('tr', { 'class': 'tr table-titles' }, [
		E('th', { 'class': 'th' }, 'MACアドレス'),
		E('th', { 'class': 'th' }, '状態'),
		E('th', { 'class': 'th' }, '')
	]) ];
	list.forEach(function(e) {
		rows.push(E('tr', { 'class': 'tr' }, [
			E('td', { 'class': 'td left' }, e.mac),
			E('td', { 'class': 'td left' }, e.enabled ? '有効' : '無効'),
			E('td', { 'class': 'td left' }, [
				E('button', {
					'class': 'cbi-button cbi-button-remove',
					'click': function() { onDelete(e.mac); }
				}, '削除')
			])
		]));
	});
	return sbair.table(rows);
}

function render(data, opts) {
	data = data || {};
	var body = [];

	body.push(sbair.errorBox([data.enabled, data.bandsteering, data.isolation, data.dot11r, data.macfilter, data.wps]
		.map(function(d) { return d && d.error; }).filter(Boolean)));

	body.push(sbair.section('Wi-Fi全体', [
		switchRow('Wi-Fi機能',
			'オフにすると2.4/5/6GHzすべてのSSIDが停止します(有線LAN・SSHには影響しません)。',
			data.enabled && data.enabled.enabled, opts.onEnabledChange,
			'Wi-Fi全体を停止します。よろしいですか?(有線LANには影響しません)'),
		switchRow('バンドステアリング',
			'デュアルバンド対応端末を混雑しにくい帯域(主に5GHz)へ誘導します。',
			data.bandsteering && data.bandsteering.enabled, opts.onBandsteeringChange)
	]));

	var iso = data.isolation || {};
	body.push(sbair.section('通信の分離設定', [
		switchRow('メインSSIDの有線LAN到達',
			'オフにすると、メインSSID(SSID1)から有線LAN側の機器へ到達できなくなります。' +
			'※この項目はメインSSID専用で、ゲストSSID(SSID2)には効きません' +
			'(SSID2の有線LAN到達は本アプリの常駐処理で別途確保しており、現在は問題なく到達できます)。',
			iso.wlan2lan, function(v) { opts.onIsolationChange('wlan2lan', v); }),
		switchRow('メインSSIDの帯域間通信',
			'オフにすると、2.4GHz/5GHz/6GHzそれぞれのメインSSID同士が互いに通信できなくなります。',
			iso.intercommunication, function(v) { opts.onIsolationChange('intercommunication', v); }),
		switchRow('メイン⇔ゲストSSID間の通信',
			'オフにすると、メインSSID(SSID1)とゲストSSID(SSID2)の端末同士が通信できなくなります' +
			'(いわゆるゲストネットワーク分離)。',
			iso.ssid1to2, function(v) { opts.onIsolationChange('ssid1to2', v); })
	]));

	body.push(sbair.section('高速ローミング (802.11r)', [
		switchRow('802.11r',
			'複数APを跨ぐ環境向けの高速ローミング規格です(単体使用では効果はありません)。全SSID共通の設定です。',
			data.dot11r && data.dot11r.enabled, opts.on11rChange)
	]));

	var mf = data.macfilter || {};
	body.push(sbair.section('MACフィルタ(許可リスト方式)', [
		switchRow('フィルタを有効化',
			'🔴 有効にすると、下の一覧に登録され「有効」になっている端末以外は接続できなくなります。' +
			'一覧が空のまま有効化すると誰も接続できなくなるので注意してください。',
			mf.enabled, opts.onMacfilterModeChange,
			'MACフィルタ(許可リスト)を有効にします。一覧に無い端末は接続できなくなります。よろしいですか?'),
		macFilterTable(mf.list, opts.onMacfilterDelete),
		E('div', { 'style': 'display:flex;gap:.5em;align-items:center;margin-top:.5em' }, [
			opts.macInput,
			E('button', { 'class': 'cbi-button cbi-button-positive', 'click': opts.onMacfilterAdd }, '追加')
		])
	]));

	var wps = data.wps || {};
	body.push(sbair.section('WPS', [
		E('p', { 'style': 'opacity:.7' }, 'プッシュボタン方式(PBC)を実行すると、一定時間だれでもWi-Fiに参加できる状態になります。'),
		E('p', {}, '現在のPIN: ' + (wps.pin || '-')),
		E('div', { 'style': 'display:flex;gap:.5em;flex-wrap:wrap' }, [
			E('button', { 'class': 'cbi-button cbi-button-action', 'click': function() { opts.onWpsRun('2.4G', 'pbc'); } }, '2.4GHzでPBC開始'),
			E('button', { 'class': 'cbi-button cbi-button-action', 'click': function() { opts.onWpsRun('5G', 'pbc'); } }, '5GHzでPBC開始'),
			E('button', { 'class': 'cbi-button cbi-button-neutral', 'click': opts.onWpsPinRandom }, 'PINを再生成'),
			E('button', { 'class': 'cbi-button cbi-button-reset', 'click': function() { opts.onWpsReset('2.4G'); } }, '2.4GHz WPSリセット'),
			E('button', { 'class': 'cbi-button cbi-button-reset', 'click': function() { opts.onWpsReset('5G'); } }, '5GHz WPSリセット')
		])
	]));

	return body;
}

return view.extend({
	load: function() {
		return loadAll();
	},

	render: function(data) {
		var self = this;
		self.data = data;

		var container = E('div', {});
		var macInput = E('input', { 'class': 'cbi-input-text', 'type': 'text', 'placeholder': 'AA:BB:CC:DD:EE:FF' });

		var redraw = function() {
			dom.content(container, render(self.data, {
				macInput: macInput,
				onEnabledChange: function(v) {
					callWifiEnabledSet(v ? '1' : '0').then(reportAndReload);
				},
				onBandsteeringChange: function(v) {
					callBandsteeringSet(v ? '1' : '0').then(reportAndReload);
				},
				onIsolationChange: function(kind, v) {
					callIsolationSet(kind, v ? '1' : '0').then(reportAndReload);
				},
				on11rChange: function(v) {
					call11rSet(v ? '1' : '0').then(reportAndReload);
				},
				onMacfilterModeChange: function(v) {
					callMacfilterModeSet(v ? '1' : '0').then(reportAndReload);
				},
				onMacfilterAdd: function() {
					var mac = macInput.value.trim();
					if (!mac) return;
					callMacfilterAdd(mac, '1').then(function(res) {
						macInput.value = '';
						reportAndReload(res);
					});
				},
				onMacfilterDelete: function(mac) {
					callMacfilterDelete(mac).then(reportAndReload);
				},
				onWpsRun: function(band, mode) {
					var label = (band === '2.4G' ? '2.4GHz' : '5GHz');
					if (!confirm(label + 'でWPSプッシュボタンを開始します。一定時間だれでも参加できる状態になります。よろしいですか?'))
						return;
					callWpsRun(band, mode, '').then(reportAndReload);
				},
				onWpsPinRandom: function() {
					callWpsPinRandom().then(reportAndReload);
				},
				onWpsReset: function(band) {
					if (!confirm((band === '2.4G' ? '2.4GHz' : '5GHz') + 'のWPS状態をリセットします。よろしいですか?'))
						return;
					callWpsReset(band).then(reportAndReload);
				}
			}));
		};

		var reload = function() {
			return loadAll().then(function(res) {
				self.data = res;
				redraw();
			});
		};

		function reportAndReload(res) {
			if (res && res.error) {
				ui.addNotification(null, E('p', {}, res.error), 'danger');
			}
			return reload();
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
