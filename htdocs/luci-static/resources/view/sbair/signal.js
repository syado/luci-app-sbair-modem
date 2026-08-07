// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912
//
// 電波状況。ubus の sbair.overview 1 本だけを叩く。

'use strict';
'require view';
'require poll';
'require rpc';
'require dom';
'require ui';
'require tools.sbair as sbair';

var callOverview    = rpc.declare({ object: 'sbair', method: 'overview' });
var callResetStart  = rpc.declare({ object: 'sbair', method: 'modem_reset' });
var callResetStatus = rpc.declare({ object: 'sbair', method: 'modem_reset_status' });
function render(self) {
	var data = self.data || {};
	var body = [];

	// **一番上に強度を出す。** Network → Wireless と同じく、数字より先に
	// 「どのくらい入っているか」が分かる形にする。基準は RSRP、無ければ RSSI。
	// **割合はバッジの中だけ**。外にも出すと二重になる。
	var dbm = data.rsrp_dbm || data.rssi_dbm || null;
	body.push(E('div', { 'class': 'cbi-section' },
		E('div', { 'style': 'display:flex;align-items:center;gap:1em;flex-wrap:wrap' }, [
			sbair.signalBadge(dbm, data.rsrp_dbm ? 'RSRP' : (data.rssi_dbm ? 'RSSI' : '')),
			E('span', {}, data.registered
				? ((data.operator || '') + ' ' + (data.access_tech || '')).trim()
				: '未登録')
		])));

	body.push(sbair.section('ネットワーク登録', [ sbair.table([
		sbair.row('登録状態', data.registration, data.registered ? null : '通信できません'),
		sbair.row('登録先', data.reg_domain),
		sbair.row('事業者', data.operator),
		sbair.row('接続方式', data.access_tech),
		sbair.row('TAC', data.tac),
		sbair.row('Cell ID', sbair.mask(data.cell_id))
	]) ]));

	// この機体はセル情報系のベンダ拡張 AT を実装していないので、
	// バンドや PCI は取りようがない。+CSQ と +CESQ が取れる全部。
	var rows = [
		sbair.row('RSSI', data.rssi_dbm ? data.rssi_dbm + ' dBm' : null),
		sbair.row('RSRP', data.rsrp_dbm ? data.rsrp_dbm + ' dBm' : null),
		sbair.row('RSRQ', data.rsrq_db ? data.rsrq_db + ' dB' : null)
	];
	if (data.signal_note)
		rows.push(sbair.row('注記', data.signal_note));
	// 規格外の余りフィールドは意味を確かめていないので生で出す。
	if (data.cesq)
		rows.push(sbair.row('+CESQ (生)', data.cesq));

	// **ネットワークに登録されていないときに素の数字だけ出さない。**
	// 登録されていなくても +CESQ は見えているセルの測定値を返すので、
	// そのまま出すと「繋がっている」ように見える。
	var signal = [];
	if (!data.registered)
		signal.push(E('div', { 'class': 'alert-message warning' },
			'ネットワークに登録されていません。以下は受信できているセルの測定値で、' +
			'接続中の回線の品質ではありません。'));
	signal.push(sbair.table(rows));
	body.push(sbair.section('電波', signal));

	body.push(resetSection(self));
	body.push(sbair.errorBox(data.errors));
	return body;
}

return view.extend({
	load: function() {
		return Promise.all([
			callOverview().catch(function(err) { return { errors: [ String(err) ] }; }),
			callResetStatus().catch(function() { return { state: 'idle' }; })
		]).then(function(r) {
			var d = r[0] || {};
			d.reset = r[1];
			return d;
		});
	},

	render: function(data) {
		var self = this;
		self.data = data;

		var container = E('div', {}, render(self));
		var redraw = function() { dom.content(container, render(self)); };
		self.redraw = redraw;

		// リセットが走っている間だけ進捗を見に行く。**modem_reset_status は
		// ファイルを読むだけで AT を開かない** ので、ワーカーが flock を
		// 握っている最中でも答える。
		self.watchReset = function() {
			return callResetStatus().then(function(j) {
				var was = self.data.reset && self.data.reset.state;
				self.data.reset = j;
				if (j.state !== 'running' && was === 'running') {
					poll.remove(self.watchReset);
					// 終わったので電波の状態を読み直す。
					return callOverview().then(function(o) {
						o.reset = j;
						self.data = o;
						redraw();
					});
				}
				redraw();
			}).catch(function() {});
		};
		if (data.reset && data.reset.state === 'running')
			poll.add(self.watchReset, 3);

		// 15 秒。呼び出し 1 回がプロセス 1 つ + AT セッション 1 本なので、
		// これ以上詰めるとモデムを叩き続けることになる。
		// **リセット中は回さない。** ワーカーが flock を握っているので
		// 待たされるだけで、進捗は watchReset が出す。
		poll.add(function() {
			if (self.data.reset && self.data.reset.state === 'running')
				return;
			return callOverview().then(function(res) {
				res.reset = self.data.reset;
				self.data = res;
				redraw();
			}).catch(function() { /* 次の周期で復帰させる */ });
		}, 15);

		return E('div', { 'class': 'cbi-map' }, [
			E('div', { 'class': 'cbi-map-descr' }, '15 秒ごとに更新。'),
			sbair.revealToggle('識別子 (Cell ID) を表示する', redraw),
			container
		]);
	},

	confirmReset: function() {
		var self = this;
		return ui.showModal('モデムのリセット', [
			E('p', {}, 'モデムの電波を一度落として上げ直し、WAN を張り直します。'),
			E('ul', {}, [
				E('li', {}, '通信が切れます (AT+CFUN=0 → 1)'),
				E('li', {}, '30〜60 秒かかります'),
				E('li', {}, 'APN の設定は変わりません')
			]),
			E('div', { 'class': 'right' }, [
				E('button', { 'class': 'cbi-button', 'click': ui.hideModal }, 'やめる'),
				' ',
				E('button', {
					'class': 'cbi-button cbi-button-action',
					'click': ui.createHandlerFn(self, function() {
						return callResetStart().then(function(res) {
							ui.hideModal();
							if (res && res.error) {
								ui.addNotification(null, E('p', {}, res.error), 'warning');
								return;
							}
							self.data.reset = { state: 'running', step: '起動' };
							poll.add(self.watchReset, 3);
							self.redraw();
						});
					})
				}, 'リセットする')
			])
		]);
	},

	handleSave: null,
	handleSaveApply: null,
	handleReset: null
});
