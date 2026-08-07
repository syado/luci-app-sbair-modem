// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912
//
// 受信 SMS。SIM (電話番号) ごとに保管庫を見る。受信と削除だけで、送信はしない。

'use strict';
'require view';
'require poll';
'require rpc';
'require dom';
'require ui';
'require tools.sbair as sbair';

var callSims     = rpc.declare({ object: 'sbair', method: 'sms_sims' });
var callMessages = rpc.declare({ object: 'sbair', method: 'sms_messages', params: [ 'iccid', 'limit' ] });
var callImport   = rpc.declare({ object: 'sbair', method: 'sms_import' });
var callStatus   = rpc.declare({ object: 'sbair', method: 'sms_status' });
var callDelete   = rpc.declare({ object: 'sbair', method: 'sms_delete', params: [ 'hash' ] });
var callPurge    = rpc.declare({ object: 'sbair', method: 'sms_purge', params: [ 'iccid' ] });

// 受信時刻は網が付けたもので、タイムゾーンごと来る。**端末の時計に寄せない。**
function when(iso) {
	if (!iso) return '';
	var m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})/.exec(iso);
	if (!m) return iso;
	return m[1] + '-' + m[2] + '-' + m[3] + ' ' + m[4] + ':' + m[5];
}

// 番号が空の profile は珍しくない。**その SIM を選べなくしない。**
function simLabel(s) {
	if (s.label) return s.label;
	if (s.number) return sbair.mask(s.number);
	return '番号なし (' + sbair.mask(s.iccid) + ')';
}

function message(self, m) {
	var head = [
		E('strong', {}, sbair.mask(m.from) || '(送信者不明)'),
		E('span', { 'style': 'color:#888;margin-left:1em' }, when(m.time))
	];
	if (m.unread)
		head.push(E('span', {
			'style': 'margin-left:1em;padding:0 .4em;border-radius:3px;' +
			         'background:#5a8;color:#fff;font-size:90%'
		}, '未読'));
	if (m.parts)
		head.push(E('span', { 'style': 'color:#888;margin-left:1em;font-size:90%' },
			m.parts + ' 分割'));

	var body = [ E('div', { 'style': 'margin-bottom:.3em' }, head) ];
	// **欠けた分割は隠さない。** 届いているぶんは出したうえで欠番を添える。
	if (m.missing && m.missing.length)
		body.push(E('div', { 'class': 'alert-message warning', 'style': 'margin:.3em 0' },
			'分割の ' + m.missing.join(', ') + ' 番目が届いていません。本文が欠けています。'));
	body.push(E('div', { 'style': 'white-space:pre-wrap;word-break:break-word' }, m.text || ''));

	if (m.hash)
		body.push(E('div', { 'style': 'margin-top:.4em' }, E('button', {
			'class': 'cbi-button cbi-button-remove',
			'click': ui.createHandlerFn(self, function() { return self.confirmDelete(m); })
		}, '削除')));

	return E('div', {
		'style': 'padding:.6em .2em;border-top:1px solid rgba(128,128,128,.3)'
	}, body);
}

function render(self) {
	var d = self.data || {};
	var body = [];

	if (d.simsError)
		body.push(E('div', { 'class': 'alert-message warning' }, d.simsError));

	// 保管先まわりの注意。**出さないと気付けない類** — 保管先を変えたのに
	// 中身を移していないと「SMS が全部消えた」ように見える(実際は置き去り)。
	if (d.simsNote)
		body.push(E('div', { 'class': 'alert-message warning' }, d.simsNote));

	// --- 取り込み -----------------------------------------------------------
	var st = d.status || {};
	var imp = [];
	if (d.importing)
		imp.push(E('div', { 'class': 'alert-message' }, [
			E('span', { 'class': 'spinning' }, ' '), 'モデムから取り込んでいます…'
		]));
	else if (d.importNote)
		imp.push(E('div', { 'class': 'alert-message success' }, d.importNote));

	imp.push(E('button', {
		'class': 'cbi-button cbi-button-action',
		'disabled': d.importing ? '' : null,
		'click': ui.createHandlerFn(self, 'doImport')
	}, 'モデムから取り込む'));
	imp.push(E('span', { 'style': 'margin-left:1em;color:#888' },
		(st.used != null && st.total != null)
			? 'モデム内: ' + st.used + ' / ' + st.total + ' 通' : ''));
	imp.push(E('div', { 'class': 'cbi-value-description' }, [
		'保管庫はこの機体の ', E('code', {}, '/etc/sbair/sms.db'), ' (SQLite)。',
		E('br'),
		E('strong', {}, '⚠ 取り込むと、モデム側で未読が既読に変わります'),
		'(AT+CMGL の仕様で避けられません)。保管庫には',
		E('strong', {}, '最初に取り込んだときの未読状態がそのまま残ります'),
		' — 2 回目以降の取り込みで上書きしません。'
	]));
	body.push(sbair.section('取り込み', imp));

	// --- SIM の選択 ---------------------------------------------------------
	if (sbair.isDebug() && d.smsDB)
		body.push(E('div', { 'class': 'cbi-value-description' }, '保管先: ' + d.smsDB));

	var sims = d.sims || [];
	if (!sims.length) {
		body.push(sbair.section('受信メッセージ', [
			E('div', { 'class': 'cbi-value-description' },
				'まだ保管庫が空です。「モデムから取り込む」を押してください。')
		]));
		return body;
	}

	var sel = E('select', {
		'class': 'cbi-input-select',
		'change': ui.createHandlerFn(self, function(ev) { return self.selectSIM(ev.target.value); })
	});
	sims.forEach(function(s) {
		var t = simLabel(s) + ' — ' + s.count + ' 通';
		if (s.unread) t += ' (未読 ' + s.unread + ')';
		sel.appendChild(E('option', {
			'value': s.iccid, 'selected': s.iccid == d.iccid ? '' : null
		}, t));
	});

	var list = [ E('div', { 'style': 'margin-bottom:.5em' }, [
		E('label', { 'style': 'margin-right:.5em' }, 'SIM / 電話番号:'), sel
	]) ];

	var cur = sims.filter(function(s) { return s.iccid == d.iccid; })[0];
	if (cur)
		list.push(sbair.table([
			sbair.row('電話番号', cur.number ? sbair.mask(cur.number) : '(SIM に入っていません)'),
			sbair.row('ICCID', sbair.mask(cur.iccid))
		]));

	var msgs = d.messages || [];
	if (d.loading)
		list.push(E('div', { 'class': 'alert-message' }, '読み込み中…'));
	else if (!msgs.length)
		list.push(E('div', { 'class': 'cbi-value-description' },
			'この SIM のメッセージはありません。'));
	else
		msgs.forEach(function(m) { list.push(message(self, m)); });

	if (msgs.length)
		list.push(E('div', { 'style': 'margin-top:1em;padding-top:.6em;' +
			'border-top:1px solid rgba(128,128,128,.3)' }, [
			E('button', {
				'class': 'cbi-button cbi-button-negative',
				'click': ui.createHandlerFn(self, 'confirmPurge')
			}, 'この SIM の全件を削除'),
			E('div', { 'class': 'cbi-value-description' },
				'保管庫から消し、あわせてモデムの保存領域も空にします。取り消せません。')
		]));

	body.push(sbair.section('受信メッセージ', list));
	return body;
}

return view.extend({
	// **開いただけではモデムを触らない。** 保管庫と件数だけ読む。
	load: function() {
		return Promise.all([
			callSims().catch(function(e) { return { error: String(e) }; }),
			callStatus().catch(function() { return {}; })
		]).then(function(r) {
			var sims = (r[0] && r[0].sims) || [];
			var d = { sims: sims, status: r[1], simsError: r[0] && r[0].error,
			          simsNote: r[0] && r[0].note, smsDB: r[0] && r[0].db,
			          messages: [], iccid: sims.length ? sims[0].iccid : null };
			if (!d.iccid) return d;
			return callMessages(d.iccid, 200).then(function(res) {
				d.messages = (res && res.messages) || [];
				return d;
			}).catch(function() { return d; });
		});
	},

	render: function(data) {
		var self = this;
		self.data = data;

		var container = E('div', {}, render(self));
		self.redraw = function() { dom.content(container, render(self)); };

		// モデム内の件数だけ見る。**AT+CPMS? は本文を読まない**ので未読を壊さない。
		poll.add(function() {
			return callStatus().then(function(st) {
				self.data.status = st;
				self.redraw();
			}).catch(function() {});
		}, 30);

		return E('div', { 'class': 'cbi-map' }, [
			E('div', { 'class': 'cbi-map-descr' }, 'モデム内の件数は 30 秒ごとに更新。'),
			sbair.revealToggle('電話番号と送信者を表示する', self.redraw),
			container
		]);
	},

	selectSIM: function(iccid) {
		var self = this;
		self.data.iccid = iccid;
		self.data.loading = true;
		self.redraw();
		return callMessages(iccid, 200).then(function(res) {
			self.data.messages = (res && res.messages) || [];
			self.data.loading = false;
			self.redraw();
		}).catch(function(e) {
			self.data.loading = false;
			self.redraw();
			ui.addNotification(null, E('p', {}, String(e)), 'warning');
		});
	},

	doImport: function() {
		var self = this;
		self.data.importing = true;
		self.data.importNote = null;
		self.redraw();
		return callImport().then(function(res) {
			self.data.importing = false;
			if (res && res.error) {
				ui.addNotification(null, E('p', {}, res.error), 'warning');
				self.redraw();
				return;
			}
			self.data.importNote = 'モデムから ' + (res.read || 0) + ' 通を読み、' +
				(res.added || 0) + ' 通を保管庫に追加しました。';
			// 取り込んだ SIM を選び直して読み込む。
			return callSims().then(function(r) {
				self.data.sims = (r && r.sims) || [];
				self.data.iccid = res.iccid || self.data.iccid;
				return callMessages(self.data.iccid, 200);
			}).then(function(m) {
				self.data.messages = (m && m.messages) || [];
				self.redraw();
			});
		}).catch(function(e) {
			self.data.importing = false;
			self.redraw();
			ui.addNotification(null, E('p', {}, String(e)), 'warning');
		});
	},

	// **削除は取り消せない。** 保管庫とモデムの両方から消す — 片方だけだと
	// 次の取り込みで戻ってくる。
	confirmDelete: function(m) {
		var self = this;
		return ui.showModal('メッセージの削除', [
			E('p', {}, '次のメッセージを保管庫とモデムの両方から削除します。'),
			E('div', { 'style': 'margin:.5em 0;padding:.5em;' +
				'background:rgba(128,128,128,.1);white-space:pre-wrap;' +
				'max-height:8em;overflow:auto' }, m.text || ''),
			E('p', {}, E('strong', {}, '取り消せません。')),
			E('div', { 'class': 'right' }, [
				E('button', { 'class': 'cbi-button', 'click': ui.hideModal }, 'やめる'),
				' ',
				E('button', {
					'class': 'cbi-button cbi-button-negative',
					'click': ui.createHandlerFn(self, function() {
						return callDelete(m.hash).then(function(res) {
							ui.hideModal();
							return self.afterDelete(res);
						});
					})
				}, '削除する')
			])
		]);
	},

	confirmPurge: function() {
		var self = this;
		var cur = (self.data.sims || []).filter(function(s) {
			return s.iccid == self.data.iccid;
		})[0] || {};
		return ui.showModal('この SIM の全件を削除', [
			E('p', {}, (cur.count || 0) + ' 通すべてを保管庫から削除し、モデムの保存領域も空にします。'),
			E('p', {}, E('strong', {}, '取り消せません。')),
			E('div', { 'class': 'right' }, [
				E('button', { 'class': 'cbi-button', 'click': ui.hideModal }, 'やめる'),
				' ',
				E('button', {
					'class': 'cbi-button cbi-button-negative',
					'click': ui.createHandlerFn(self, function() {
						return callPurge(self.data.iccid).then(function(res) {
							ui.hideModal();
							return self.afterDelete(res);
						});
					})
				}, 'すべて削除する')
			])
		]);
	},

	afterDelete: function(res) {
		var self = this;
		if (res && res.error) {
			ui.addNotification(null, E('p', {}, res.error), 'warning');
			return;
		}
		if (res && res.errors && res.errors.length)
			ui.addNotification(null, E('p', {},
				'モデム側で消せなかったものがあります: ' + res.errors.join(' / ') +
				' — 保管庫からは消えており、取り込み直しても戻りません。'), 'warning');
		return Promise.all([ callSims(), callMessages(self.data.iccid, 200) ])
			.then(function(r) {
				self.data.sims = (r[0] && r[0].sims) || [];
				self.data.messages = (r[1] && r[1].messages) || [];
				if (!self.data.sims.filter(function(s) { return s.iccid == self.data.iccid; }).length)
					self.data.iccid = self.data.sims.length ? self.data.sims[0].iccid : null;
				self.redraw();
			});
	},

	handleSave: null,
	handleSaveApply: null,
	handleReset: null
});
