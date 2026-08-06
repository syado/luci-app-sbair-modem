// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912
//
// 電波状況。ubus の sbair.overview 1 本だけを叩く。
//
// **メソッドを細かく割らないこと。** rpcd は呼び出しごとにバックエンドの
// プロセスを起こし、その 1 回が 1 flock + 1 AT セッションになる。
// RSRP と RSRQ を別メソッドにすると、それが丸ごと 2 倍になる。
// ポーリング間隔を詰めないのも同じ理由。

'use strict';
'require view';
'require poll';
'require rpc';
'require dom';
'require tools.sbair as sbair';

var callOverview = rpc.declare({ object: 'sbair', method: 'overview' });

function render(data) {
	data = data || {};
	var body = [];

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

	body.push(sbair.errorBox(data.errors));
	return body;
}

return view.extend({
	load: function() {
		return callOverview().catch(function(err) {
			return { errors: [ String(err) ] };
		});
	},

	render: function(data) {
		var self = this;
		self.data = data;

		var container = E('div', {}, render(data));
		var redraw = function() { dom.content(container, render(self.data)); };

		// 15 秒。呼び出し 1 回がプロセス 1 つ + AT セッション 1 本なので、
		// これ以上詰めるとモデムを叩き続けることになる。
		poll.add(function() {
			return callOverview().then(function(res) {
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

	handleSave: null,
	handleSaveApply: null,
	handleReset: null
});
