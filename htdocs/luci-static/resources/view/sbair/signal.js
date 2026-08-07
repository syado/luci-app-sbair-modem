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
var callImsStatus   = rpc.declare({ object: 'sbair', method: 'ims_status' });
var callImsSet      = rpc.declare({ object: 'sbair', method: 'ims_set', params: [ 'on' ] });
var callBandSet     = rpc.declare({ object: 'sbair', method: 'band_set', params: [ 'lte', 'nr' ] });
var callBandStatus  = rpc.declare({ object: 'sbair', method: 'band_status' });

// IMS。**出荷状態ではモデム側で無効。** SMS は IMS 経由で配送されるので、
// 未登録だと 1 通も届かない。
//
// ⚠ **判定に config の値を使わない。** モデムは登録しているのに
// `ims_get_config` が Off を返すことがある(書く先と読む先が違うらしい)。
// 真偽は AT+CIREG?、config は参考として小さく出すだけ。
function imsSection(self) {
	var d = self.data || {}, ims = d.ims || {};
	var on = !!ims.registered;
	var children = [];

	if (d.imsBusy)
		children.push(E('div', { 'class': 'alert-message' }, [
			E('span', { 'class': 'spinning' }, ' '), '切り替えています…'
		]));
	else if (d.imsNote)
		children.push(E('div', { 'class': 'alert-message success' }, d.imsNote));

	var rows = [
		sbair.row('登録状態', on ? '登録済み' : '未登録',
			on ? null : 'この状態では SMS が届きません'),
		sbair.row('使えるサービス', (ims.services && ims.services.length)
			? ims.services.join(' / ') : null),
	];
	// **生の応答と参考値はデバッグ表示のときだけ。** 参考値は登録状態と
	// 食い違うことがあり、並べると「どちらが本当か」を迷わせる。
	if (sbair.isDebug())
		rows.push(
			sbair.row('+CIREG (生)', ims.cireg),
			sbair.row('モデムの設定値', ims.config ? String(ims.config).trim() : null,
				'参考値。登録状態と食い違うことがあります'));
	children.push(sbair.table(rows));

	children.push(E('div', {}, [
		E('button', {
			'class': 'cbi-button ' + (on ? 'cbi-button-remove' : 'cbi-button-action'),
			'disabled': d.imsBusy ? '' : null,
			'click': ui.createHandlerFn(self, function() { return self.setIms(!on); })
		}, ims.config_on ? 'IMS を無効にする' : 'IMS を有効にする')
	]));

	children.push(E('div', { 'class': 'cbi-value-description' }, [
		E('strong', {}, 'SMS は IMS 経由で配送されます。'),
		' 未登録だと 1 通も届きません。出荷状態では無効です。',
		E('br'),
		'再起動をまたぎます。ただし設定を初期化すると SIM ロックが出荷既定に戻り、' +
		'圏外になるので IMS も登録できなくなります。',
		E('br'),
		'⚠ このスイッチはモデムの VoLTE フラグです。未登録から有効にすると ' +
		'10〜30 秒で登録しますが、登録済みの状態で無効にしても ' +
		'登録と SMS over IMS は残ることがあります。'
	]));

	return sbair.section('IMS', children);
}

function sameSet(a, b) {
	a = a || []; b = b || [];
	return a.length === b.length && a.every(function(v, i) { return v === b[i]; });
}

// 選択状態を data から作り直す。**編集中の選択を毎回の更新で消さないため**、
// モデム側の有効バンドが変わったときだけ入れ直す。
function syncBandSel(self, b) {
	var enabled = { lte: b.lte_enabled || [], nr: b.nr_enabled || [] };
	if (!self.bandSel || !self.bandApplied ||
	    !sameSet(self.bandApplied.lte, enabled.lte) ||
	    !sameSet(self.bandApplied.nr, enabled.nr)) {
		self.bandSel = { lte: enabled.lte.slice(), nr: enabled.nr.slice() };
		self.bandApplied = { lte: enabled.lte.slice(), nr: enabled.nr.slice() };
	}
}

// バンド 1 個のタイル。**色だけで区別しない** — テーマによっては背景色が
// 効かないので、接続中は ● と太字、無効は薄字と、字面でも分かるようにする。
function bandTile(label, on, serving, onClick) {
	var attrs = {
		'class': 'ifacebadge',
		'style': 'margin:.15em .3em 0 0' + (on ? '' : ';opacity:.45') +
			(onClick ? ';cursor:pointer;user-select:none' : ''),
		'title': onClick ? (on ? 'クリックで無効にする' : 'クリックで有効にする')
		                 : (on ? '有効' : '対応しているが無効')
	};
	if (onClick)
		attrs.click = onClick;
	return E('span', attrs, serving ? [ '● ', E('strong', {}, label) ] : label);
}

// **対応バンドを基準に並べる。** 有効なものだけ出すと「ほかに何があるのか」が
// 分からない。対応が取れなかったときだけ有効側で代用する。
function bandTiles(self, kind, prefix, supported, enabled, serving, editable) {
	var sup = (supported && supported.length) ? supported : (enabled || []);
	if (!sup.length)
		return null;
	var sel = self.bandSel[kind];
	var cur = {};
	(serving || []).forEach(function(v) { cur[v] = true; });

	return E('span', {}, sup.map(function(n) {
		var on = sel.indexOf(n) >= 0;
		return bandTile(prefix + n, on, cur[n], editable ? function() {
			// **最後の 1 つは外させない。** LTE が空だとバックエンドが
			// 弾くので、押した結果が拒否になるより押せないほうがよい。
			if (on && kind === 'lte' && sel.length <= 1)
				return;
			self.bandSel[kind] = on ? sel.filter(function(v) { return v !== n; })
			                        : sel.concat([ n ]).sort(function(x, y) { return x - y; });
			self.redraw();
		} : null);
	}));
}

// バンド。**どの AT で取れるかは機体依存**で、その調べ方は sbair6-rs 側。
// ここは band.go が返した値を並べるだけ。
function bandSection(self) {
	var b = (self.data || {}).band;
	if (!b)
		return null;
	syncBandSel(self, b);

	var job = self.data.bandJob || {};
	var running = (job.state === 'running');
	var editable = !running;
	var children = [];

	if (running)
		children.push(E('div', { 'class': 'alert-message' }, [
			E('span', { 'class': 'spinning' }, ' '),
			'適用中: ' + (job.step || '') + ' — 30〜60 秒かかります。'
		]));
	else if (job.state === 'error')
		children.push(E('div', { 'class': 'alert-message warning' },
			(job.step ? job.step + ': ' : '') + (job.message || '失敗しました。')));
	else if (job.state === 'done')
		children.push(E('div', { 'class': 'alert-message success' }, job.message || '完了しました。'));

	var serving = null;
	if (b.serving_bands && b.serving_bands.length)
		serving = (b.serving_rat ? b.serving_rat + ' ' : '') +
			b.serving_bands.map(function(n) { return 'B' + n; }).join(' + ');

	var rows = [ sbair.row('接続中のバンド', serving,
		serving ? null : 'モデムが報告していません') ];

	// 搬送波ごとの内訳。**CA が効いているかはここでしか分からない。**
	if (b.carriers && b.carriers.length)
		rows.push(sbair.row('搬送波', E('span', {}, b.carriers.map(function(c, i) {
			return E('span', { 'style': 'display:inline-block;margin-right:1.2em' }, [
				E('strong', {}, c.role + ' B' + c.band),
				' EARFCN ' + c.earfcn + ' / PCI ' + c.pci
			]);
		})), b.carriers.length > 1 ? 'CA ' + b.carriers.length + ' 波' : null));
	var lte = bandTiles(self, 'lte', 'B', b.lte_supported, b.lte_enabled, b.serving_bands, editable);
	// **5G のタイルに接続中のバンドを渡さない。** +ECBDINFO は接続中の RAT の
	// バンドしか返さないので、LTE につながっているときの 41 を n41 として光らせてしまう。
	var nr = bandTiles(self, 'nr', 'n', b.nr_supported, b.nr_enabled, null, editable);
	if (lte)
		rows.push(sbair.row('LTE', lte));
	if (nr)
		rows.push(sbair.row('5G NR', nr));
	if (sbair.isDebug()) {
		if (b.dmfapp)
			rows.push(sbair.row('+EDMFAPP 6,3 (生)', b.dmfapp));
		if (b.ecbdinfo)
			rows.push(sbair.row('+ECBDINFO (生)', b.ecbdinfo));
		if (b.epbseh)
			rows.push(sbair.row('+EPBSEH (生)', b.epbseh));
	}
	children.push(sbair.table(rows));

	var dirty = !sameSet(self.bandSel.lte, self.bandApplied.lte) ||
	            !sameSet(self.bandSel.nr, self.bandApplied.nr);

	var buttons = [
		E('button', {
			'class': 'cbi-button cbi-button-action important',
			'disabled': (running || !dirty) ? '' : null,
			'click': ui.createHandlerFn(self, 'confirmBands')
		}, '適用'),
		' ',
		E('button', {
			'class': 'cbi-button',
			'disabled': running ? '' : null,
			'click': ui.createHandlerFn(self, function() {
				self.bandSel = {
					lte: (b.lte_supported || self.bandApplied.lte).slice(),
					nr: (b.nr_supported || self.bandApplied.nr).slice()
				};
				self.redraw();
			})
		}, '対応バンドを全部選ぶ')
	];
	if (dirty)
		buttons.push(' ', E('button', {
			'class': 'cbi-button',
			'disabled': running ? '' : null,
			'click': ui.createHandlerFn(self, function() {
				self.bandSel = {
					lte: self.bandApplied.lte.slice(),
					nr: self.bandApplied.nr.slice()
				};
				self.redraw();
			})
		}, '選び直しをやめる'));
	// 直前の設定はバックエンドが uci に残している。**再起動をまたいで戻せる。**
	if (!dirty && (b.prev_lte || b.prev_nr))
		buttons.push(' ', E('button', {
			'class': 'cbi-button',
			'disabled': running ? '' : null,
			'click': ui.createHandlerFn(self, function() {
				self.bandSel = { lte: (b.prev_lte || []).slice(), nr: (b.prev_nr || []).slice() };
				self.redraw();
			})
		}, '変更前の組み合わせに戻す'));
	children.push(E('div', {}, buttons));

	var note = [
		'番号を押すと選び直せます。薄い番号は', E('strong', {}, '無効'),
		'、● が付いているものがいま掴んでいるバンドです。',
		E('br'),
		E('strong', {}, '適用すると数秒だけ電波が切れます。'),
		' 時間内にネットワークにつながらなければ', E('strong', {}, '自動で元の設定に巻き戻します'), '。',
		E('br'),
		'この画面と SSH は LAN 側なので、バンドを外して通信できなくなっても操作は続けられます。',
		E('br'),
		'モデム側の設定は再起動で出荷既定に戻りますが、選んだ組み合わせは保存してあり、' +
		'起動時に自動で入れ直します。'
	];

	// **「接続方式 NR5G」なのに接続中のバンドが LTE、は矛盾ではない。**
	// 5G NSA では LTE のアンカーに繋いだうえで NR を足す形なので、
	// NR がまだ足されていない間はアンカーの LTE バンドだけが出る。
	// 上の 5G 測定値が空なら、実際に NR が足されていない。
	if (b.serving_rat === 'LTE' && self.data.access_tech &&
	    self.data.access_tech.indexOf('NR') >= 0 && !(b.nr_rsrp && b.nr_rsrp.length))
		note.push(E('br'), E('em', {},
			'接続方式が NR5G でも、5G NSA では LTE のアンカーに繋いでから 5G を足します。' +
			'いまは 5G 側の測定値が空なので、まだ足されていません。'));

	children.push(E('div', { 'class': 'cbi-value-description' }, note));
	return sbair.section('バンド', children);
}

function resetSection(self) {
	var job = self.data.reset || {};
	var running = (job.state === 'running');
	var children = [];

	if (running) {
		children.push(E('div', { 'class': 'alert-message' }, [
			E('span', { 'class': 'spinning' }, ' '),
			'リセット中: ' + (job.step || '') + ' — 30〜60 秒かかります。'
		]));
	} else if (job.state === 'error') {
		children.push(E('div', { 'class': 'alert-message warning' },
			(job.step ? job.step + ': ' : '') + (job.message || '失敗しました。')));
	} else if (job.state === 'done') {
		children.push(E('div', { 'class': 'alert-message success' },
			job.message || '完了しました。'));
	}

	children.push(E('button', {
		'class': 'cbi-button cbi-button-action',
		'disabled': running ? '' : null,
		'click': ui.createHandlerFn(self, 'confirmReset')
	}, 'モデムをリセット'));

	children.push(E('div', { 'class': 'cbi-value-description' }, [
		'電波を落として上げ直し (AT+CFUN=0 → 1)、WAN を張り直します。',
		E('br'),
		E('strong', {}, 'データコールの失敗のしかたによってはモデム側のセッションが詰まり、' +
			'netifd がいくら再試行しても上がらなくなることがあります。'),
		' APN が正しいのに繋がらないときはこれで抜けられます。'
	]));

	return sbair.section('モデムのリセット', children);
}

// 画面のいちばん上。**数字より先に「どのくらい入っているか」**が分かる形。
// 基準は RSRP、無ければ RSSI。**割合はバッジの中だけ** — 外にも出すと二重になる。
//
// **識別子のチェックボックスより上に置く。** ここだけ別の器に描くのは、
// チェックボックスを更新のたびに作り直さないため。
function statusRow(self) {
	var data = self.data || {};
	var dbm = data.rsrp_dbm || data.rssi_dbm || null;
	// 余白の付け方は sbair.section と揃える。**下のチェックボックスと
	// くっつかないように。**
	return E('div', { 'class': 'cbi-section sbair-section' },
		E('div', { 'style': 'display:flex;align-items:center;gap:1em;flex-wrap:wrap' }, [
			sbair.signalBadge(dbm, data.rsrp_dbm ? 'RSRP' : (data.rssi_dbm ? 'RSSI' : '')),
			E('span', {}, data.registered
				? ((data.operator || '') + ' ' + (data.access_tech || '')).trim()
				: '未登録')
		]));
}

function render(self) {
	var data = self.data || {};
	var body = [];

	body.push(sbair.section('ネットワーク登録', [ sbair.table([
		sbair.row('登録状態', data.registration, data.registered ? null : '通信できません'),
		sbair.row('登録先', data.reg_domain),
		sbair.row('事業者', data.operator),
		sbair.row('接続方式', data.access_tech),
		sbair.row('TAC', data.tac),
		sbair.row('Cell ID', sbair.mask(data.cell_id))
	]) ]));

	// セル情報系のベンダ拡張は無いので PCI は取りようがない。
	// +CSQ / +CESQ に加えて、AT+QNWCFG がアンテナごとの値を持っている。
	var rows = [
		sbair.row('RSSI', data.rssi_dbm ? data.rssi_dbm + ' dBm' : null),
		sbair.row('RSRP', data.rsrp_dbm ? data.rsrp_dbm + ' dBm' : null),
		sbair.row('RSRQ', data.rsrq_db ? data.rsrq_db + ' dB' : null)
	];

	// **アンテナごとの値は +CESQ より細かい。** つながっていない RAT は
	// バックエンドが落としてくるので、来た分だけ出す。
	var band = data.band || {};
	var perAnt = function(label, arr, unit) {
		if (!arr || !arr.length)
			return;
		rows.push(sbair.row(label, arr.map(function(v) { return v + ' ' + unit; }).join(' / '),
			'アンテナ ' + arr.length + ' 本'));
	};
	perAnt('RSRP (LTE 各アンテナ)', band.lte_rsrp, 'dBm');
	perAnt('SINR (LTE 各アンテナ)', band.lte_sinr, 'dB');
	perAnt('RSRP (5G 各アンテナ)', band.nr_rsrp, 'dBm');
	perAnt('SINR (5G 各アンテナ)', band.nr_sinr, 'dB');

	if (data.signal_note)
		rows.push(sbair.row('注記', data.signal_note));
	// 規格外の余りフィールドは意味を確かめていないので生で出す。
	if (sbair.isDebug() && data.cesq)
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

	var bands = bandSection(self);
	if (bands)
		body.push(bands);

	body.push(imsSection(self));
	body.push(resetSection(self));
	body.push(sbair.errorBox(data.errors));
	return body;
}

return view.extend({
	load: function() {
		return Promise.all([
			callOverview().catch(function(err) { return { errors: [ String(err) ] }; }),
			callResetStatus().catch(function() { return { state: 'idle' }; }),
			callImsStatus().catch(function() { return {}; }),
			callBandStatus().catch(function() { return { state: 'idle' }; })
		]).then(function(r) {
			var d = r[0] || {};
			d.reset = r[1];
			d.ims = r[2];
			d.bandJob = r[3];
			return d;
		});
	},

	render: function(data) {
		var self = this;
		self.data = data;

		// **状況とその下は別の器にする。** 間にチェックボックスを挟むので、
		// 1 つにまとめると更新のたびにチェックボックスまで作り直すことになる。
		var status = E('div', {}, statusRow(self));
		var container = E('div', {}, render(self));
		var redraw = function() {
			dom.content(status, statusRow(self));
			dom.content(container, render(self));
		};
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

		// バンド適用の進捗。**band_status もファイルを読むだけ**なので、
		// ワーカーが flock を握っている間でも答える。
		self.watchBand = function() {
			return callBandStatus().then(function(j) {
				var was = self.data.bandJob && self.data.bandJob.state;
				self.data.bandJob = j;
				if (j.state !== 'running' && was === 'running') {
					poll.remove(self.watchBand);
					// 適用後の実際の値を読み直す。**選択もそこから作り直す。**
					return callOverview().then(function(o) {
						o.reset = self.data.reset;
						o.ims = self.data.ims;
						o.bandJob = j;
						self.data = o;
						self.bandApplied = null;
						redraw();
					});
				}
				redraw();
			}).catch(function() {});
		};
		if (data.bandJob && data.bandJob.state === 'running')
			poll.add(self.watchBand, 3);

		// 15 秒。呼び出し 1 回がプロセス 1 つ + AT セッション 1 本なので、
		// これ以上詰めるとモデムを叩き続けることになる。
		// **リセット中は回さない。** ワーカーが flock を握っているので
		// 待たされるだけで、進捗は watchReset が出す。
		poll.add(function() {
			if ((self.data.reset && self.data.reset.state === 'running') ||
			    (self.data.bandJob && self.data.bandJob.state === 'running'))
				return;
			return callOverview().then(function(res) {
				res.reset = self.data.reset;
				res.ims = self.data.ims;
				res.bandJob = self.data.bandJob;
				self.data = res;
				redraw();
			}).catch(function() { /* 次の周期で復帰させる */ });
		}, 15);

		return E('div', { 'class': 'cbi-map' }, [
			status,
			sbair.revealToggle('識別子 (Cell ID) を表示する', redraw),
			container,
			sbair.debugToggle(redraw)
		]);
	},

	// 有効化すると登録まで 10〜30 秒かかる。押しっぱなしに見えないよう、
	// 切り替えた後に何度か読み直す。
	setIms: function(on) {
		var self = this;
		self.data.imsBusy = true;
		self.data.imsNote = null;
		self.redraw();
		return callImsSet(on).then(function(res) {
			self.data.imsBusy = false;
			if (res && res.error) {
				ui.addNotification(null, E('p', {}, res.error), 'warning');
				self.redraw();
				return;
			}
			self.data.ims = res;
			self.data.imsNote = res.note;
			self.redraw();
			if (!on) return;
			var tries = 0;
			self.imsWatch = function() {
				return callImsStatus().then(function(s) {
					self.data.ims = s;
					self.redraw();
					if (s.registered || ++tries >= 10) poll.remove(self.imsWatch);
				}).catch(function() {});
			};
			poll.add(self.imsWatch, 3);
		}).catch(function(e) {
			self.data.imsBusy = false;
			self.redraw();
			ui.addNotification(null, E('p', {}, String(e)), 'warning');
		});
	},

	// 適用の前に、何がどう変わるかを並べて見せる。
	confirmBands: function() {
		var self = this, b = self.data.band || {};
		var fmt = function(prefix, arr) {
			return (arr && arr.length) ? arr.map(function(n) { return prefix + n; }).join(' ') : 'なし';
		};
		var line = function(label, prefix, before, after) {
			var same = sameSet(before, after);
			return E('li', {}, [
				label + ': ' + fmt(prefix, before),
				same ? ' (変更なし)' : E('span', {}, [ ' → ', E('strong', {}, fmt(prefix, after)) ])
			]);
		};
		// 接続中のバンドを外すのは止めない(戻せるので)が、**必ず名指しする。**
		var dropping = (b.serving_bands || []).filter(function(n) {
			return self.bandSel.lte.indexOf(n) < 0;
		});

		var body = [
			E('p', {}, '有効にするバンドを次のように変更します。'),
			E('ul', {}, [
				line('LTE', 'B', self.bandApplied.lte, self.bandSel.lte),
				line('5G NR', 'n', self.bandApplied.nr, self.bandSel.nr)
			])
		];
		if (dropping.length)
			body.push(E('div', { 'class': 'alert-message warning' }, [
				E('strong', {}, 'いま掴んでいる B' + dropping.join(' / B') + ' を外します。'),
				' 別のバンドに移れなければ圏外になりますが、その場合は自動で元に戻します。'
			]));
		body.push(E('ul', {}, [
			E('li', {}, '数秒だけ電波が切れます (再スキャンが走ります)'),
			E('li', {}, '30〜60 秒かかります'),
			E('li', {}, '時間内にネットワークにつながらなければ自動で巻き戻します'),
			E('li', {}, 'この画面と SSH は LAN 側なので切れません')
		]));
		body.push(E('div', { 'class': 'right' }, [
			E('button', { 'class': 'cbi-button', 'click': ui.hideModal }, 'やめる'),
			' ',
			E('button', {
				'class': 'cbi-button cbi-button-action important',
				'click': ui.createHandlerFn(self, function() {
					return callBandSet(self.bandSel.lte.join(','), self.bandSel.nr.join(','))
						.then(function(res) {
							ui.hideModal();
							if (res && res.error) {
								ui.addNotification(null, E('p', {}, res.error), 'warning');
								return;
							}
							self.data.bandJob = { state: 'running', step: '起動' };
							poll.add(self.watchBand, 3);
							self.redraw();
						});
				})
			}, '適用する')
		]));
		return ui.showModal('バンドの変更', body);
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
