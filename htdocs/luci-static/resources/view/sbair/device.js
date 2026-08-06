// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912
//
// デバイスの詳細情報。sbair.overview を 1 本だけ叩く。
//
// 自動更新はしない。機種もファームウェアも動かないし、温度のためだけに
// 30 秒ごとにモデムを叩く理由が無い。更新は明示のボタンで。

'use strict';
'require view';
'require rpc';
'require ui';
'require dom';
'require tools.sbair as sbair';

var callOverview = rpc.declare({ object: 'sbair', method: 'overview' });

function tempTable(list) {
	if (!list || !list.length)
		return E('p', {}, '温度を取得できませんでした。');

	// 27 個あるので、一番熱いものを先頭に。数値にならないものは末尾へ。
	var sorted = list.slice().sort(function(a, b) {
		var x = parseFloat(a.celsius), y = parseFloat(b.celsius);
		if (isNaN(x)) return 1;
		if (isNaN(y)) return -1;
		return y - x;
	});

	var rows = [ E('tr', { 'class': 'tr table-titles' }, [
		E('th', { 'class': 'th' }, 'センサ'),
		E('th', { 'class': 'th' }, '温度')
	]) ];
	sorted.forEach(function(t) {
		var v = parseFloat(t.celsius);
		rows.push(E('tr', { 'class': 'tr' }, [
			E('td', { 'class': 'td left' }, t.sensor),
			E('td', { 'class': 'td left' }, [
				isNaN(v) ? String(t.celsius) : (v.toFixed(1) + ' ℃'),
				(!isNaN(v) && v >= 80)
					? E('span', { 'class': 'ifacebadge', 'style': 'margin-left:.5em' }, '高温')
					: ''
			])
		]));
	});
	return sbair.table(rows);
}

function render(data) {
	data = data || {};
	var body = [];

	body.push(sbair.section('モデム', [ sbair.table([
		sbair.row('メーカー', data.manufacturer),
		sbair.row('機種', data.model),
		sbair.row('ファームウェア', data.revision),
		sbair.row('IMEI', sbair.mask(data.imei))
	]) ]));

	// ATI は Quectel と名乗るが中身は MediaTek。セル情報系のベンダ AT は
	// 全て +CME ERROR: 4 で、AT+QTEMP と AT+QUIMSLOT? だけが応答する。
	body.push(sbair.section('温度 (AT+QTEMP)', [ tempTable(data.temperatures) ]));

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

		var reload = function() {
			return callOverview().then(function(res) {
				self.data = res;
				redraw();
			}).catch(function(err) {
				ui.addNotification(null, E('p', {}, String(err)), 'warning');
			});
		};

		return E('div', { 'class': 'cbi-map' }, [
			sbair.revealToggle('識別子 (IMEI) を表示する', redraw),
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
