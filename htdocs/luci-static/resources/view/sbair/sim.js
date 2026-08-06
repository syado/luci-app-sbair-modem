// SPDX-License-Identifier: MIT
// Copyright (c) 2026 soralis0912
//
// SIM 関連。マッピングの切替と、eUICC の profile。

'use strict';
'require view';
'require poll';
'require rpc';
'require ui';
'require dom';
'require tools.sbair as sbair';

var callStatus    = rpc.declare({ object: 'sbair', method: 'esim_status' });
var callSimmapSet = rpc.declare({ object: 'sbair', method: 'simmap_set',
                                  params: [ 'mapping' ] });
var callSimmapGet = rpc.declare({ object: 'sbair', method: 'simmap_status' });
var callDownload  = rpc.declare({ object: 'sbair', method: 'esim_download',
                                  params: [ 'activation_code', 'confirmation_code' ] });
var callDlStatus  = rpc.declare({ object: 'sbair', method: 'esim_download_status' });
var callLockGet   = rpc.declare({ object: 'sbair', method: 'simlock_get' });
var callLockSet   = rpc.declare({ object: 'sbair', method: 'simlock_set', params: [ 'on' ] });
var callLockStat  = rpc.declare({ object: 'sbair', method: 'simlock_status' });
var callApnStatus = rpc.declare({ object: 'sbair', method: 'apn_status' });
var callApnSet    = rpc.declare({ object: 'sbair', method: 'apn_set',
                                  params: [ 'iccid', 'apn', 'auth', 'username',
                                            'password', 'iptype', 'label' ] });
var callApnApply  = rpc.declare({ object: 'sbair', method: 'apn_apply' });
var callApnDelete = rpc.declare({ object: 'sbair', method: 'apn_delete', params: [ 'iccid' ] });
var callApnProbe  = rpc.declare({ object: 'sbair', method: 'apn_probe' });
var callNickname  = rpc.declare({ object: 'sbair', method: 'esim_nickname',
                                  params: [ 'iccid', 'nickname' ] });

// ql_datacall の auth の値。proto_config_add_int auth。
var AUTH = { '0': 'なし', '1': 'PAP', '2': 'CHAP', '3': 'PAP/CHAP' };
var callProfileOp = function(method, iccid) {
	return rpc.declare({ object: 'sbair', method: method, params: [ 'iccid' ] })(iccid);
};

var CARD = {
	none:  '無し',
	sim:   '通常の SIM (eUICC ではない)',
	euicc: 'eUICC'
};

function stateBadge(state) {
	var on = (state === 'enabled');
	return E('span', {
		'class': 'ifacebadge',
		'style': on ? 'background-color:#5b5' : ''
	}, on ? '有効' : '無効');
}

return view.extend({
	load: function() {
		return Promise.all([
			callStatus().catch(function(err) { return { error: String(err) }; }),
			callSimmapGet().catch(function() { return { state: 'idle' }; }),
			callDlStatus().catch(function() { return { state: 'idle' }; }),
			callLockGet().catch(function() { return {}; }),
			callLockStat().catch(function() { return { state: 'idle' }; }),
			callApnStatus().catch(function() { return {}; })
		]).then(function(r) {
			return { status: r[0], sw: r[1], dl: r[2],
			         lock: r[3], lockJob: r[4], apn: r[5] };
		});
	},

	render: function(data) {
		var self = this;
		self.data = data;

		var container = E('div', {});
		var redraw = function() { dom.content(container, self.body()); };
		self.redraw = redraw;

		// 切替が走っている間だけ状態を見に行く。simmap_status はファイルを
		// 読むだけだが、それでも 1 回が 1 プロセスなので常時は回さない。
		self.watch = function() {
			return callSimmapGet().then(function(sw) {
				var was = self.data.sw && self.data.sw.state;
				self.data.sw = sw;
				if (sw.state !== 'running' && was === 'running') {
					poll.remove(self.watch);
					// 切替が終わったのでカードを読み直す。
					return callStatus().then(function(st) {
						self.data.status = st;
						redraw();
					});
				}
				redraw();
			}).catch(function() {});
		};
		// インストールの監視。終わったらカードを読み直して止める。
		self.watchDl = function() {
			return callDlStatus().then(function(dl) {
				var was = self.data.dl && self.data.dl.state;
				self.data.dl = dl;
				if (dl.state !== 'running' && was === 'running') {
					poll.remove(self.watchDl);
					return callStatus().then(function(st) {
						self.data.status = st;
						redraw();
					});
				}
				redraw();
			}).catch(function() {});
		};

		// SIM ロックの切替も CFUN の往復があるので監視する。
		self.watchLock = function() {
			return callLockStat().then(function(lj) {
				var was = self.data.lockJob && self.data.lockJob.state;
				self.data.lockJob = lj;
				if (lj.state !== 'running' && was === 'running') {
					poll.remove(self.watchLock);
					return Promise.all([ callLockGet(), callStatus() ]).then(function(r) {
						self.data.lock = r[0];
						self.data.status = r[1];
						redraw();
					});
				}
				redraw();
			}).catch(function() {});
		};

		if (data.sw && data.sw.state === 'running')
			poll.add(self.watch, 3);
		if (data.lockJob && data.lockJob.state === 'running')
			poll.add(self.watchLock, 3);
		if (data.dl && data.dl.state === 'running')
			poll.add(self.watchDl, 3);

		redraw();

		return E('div', { 'class': 'cbi-map' }, [
			sbair.revealToggle('識別子 (EID / ICCID) を表示する', redraw),
			container
		]);
	},

	body: function() {
		var self = this;
		var st = this.data.status || {};
		var sw = this.data.sw || {};
		var running = (sw.state === 'running');
		var body = [];

		// --- SIM マッピング -------------------------------------------------
		var rows = [
			sbair.row('マッピング', st.label),
			sbair.row('カード', CARD[st.card] || st.card),
			sbair.row('SIM の状態', st.sim_status),
			sbair.row('電話番号', st.msisdn ? sbair.mask(st.msisdn) : null),
			sbair.row('ICCID', sbair.mask(st.iccid))
		];

		var target = (st.mapping === 1) ? 2 : 1;
		var mapChildren = [ sbair.table(rows) ];

		if (running) {
			mapChildren.push(E('div', { 'class': 'alert-message' }, [
				E('p', {}, [ E('strong', {}, '切替中: '), sw.step || '' ]),
				E('p', {}, '30〜60 秒かかります。その間 AT は無応答になり、' +
				           '他のタブも一時的に読めなくなります。')
			]));
		} else {
			if (sw.state === 'error')
				mapChildren.push(E('div', { 'class': 'alert-message warning' },
					'前回の切替に失敗しました (' + (sw.step || '') + '): ' + (sw.message || '')));
			else if (sw.state === 'done')
				mapChildren.push(E('div', { 'class': 'alert-message' },
					'切替が完了しました。' + (sw.message || '')));

			mapChildren.push(E('div', { 'style': 'margin-top:1em' }, [
				E('button', {
					'class': 'cbi-button cbi-button-action',
					'click': ui.createHandlerFn(this, function() {
						return self.confirmSwitch(target);
					})
				}, sbair.simMappingLabel(target) + ' へ切り替える')
			]));
			mapChildren.push(E('div', { 'class': 'cbi-value-description' },
				'⚠ 切替中は電波を止めます (AT+CFUN=4)。' +
				'切替先に有効な profile が無ければ圏外になります。' +
				'再起動すると物理スロット側へ戻ります。'));
		}
		body.push(sbair.section('SIM マッピング', mapChildren));
		body.push(this.lockSection());
		body.push(this.apnSection());

		// --- eUICC ---------------------------------------------------------
		if (st.error) {
			body.push(E('div', { 'class': 'alert-message warning' }, st.error));
			return body;
		}

		if (!st.available) {
			body.push(sbair.section('eSIM (eUICC)', [
				E('p', {}, st.reason || 'eUICC はありません。'),
				(st.card === 'sim')
					? E('p', {}, '通常の SIM が挿さっています。profile の操作は eUICC カードでのみ行えます。')
					: ''
			]));
			return body;
		}

		body.push(sbair.section('eUICC', [ sbair.table([
			sbair.row('EID', sbair.mask(st.eid)),
			sbair.row('SGP.22', st.svn),
			sbair.row('ISD-R AID', sbair.mask(st.isdr_aid))
		]) ]));

		body.push(sbair.section('profile', [
			st.profiles_error
				? E('div', { 'class': 'alert-message warning' }, st.profiles_error)
				: this.profileTable(st.profiles, running)
		]));

		body.push(this.installer(running));
		return body;
	},

	// SIM ロック(ネットワークロック)。解除の鍵はファームウェア内の
	// /bin/sim_lock.sh が持っていて、こちらは持たない。
	lockSection: function() {
		var self = this;
		var lk = this.data.lock || {};
		var lj = this.data.lockJob || {};
		var busy = (lj.state === 'running');
		var locked = !!lk.locked;
		var children = [];

		if (busy) {
			children.push(E('div', { 'class': 'alert-message' }, [
				E('p', {}, [ E('strong', {}, '切替中: '), lj.step || '' ]),
				E('p', {}, 'SIM を読み直すため電波を止めます。40〜60 秒かかります。')
			]));
			return sbair.section('SIM ロック', children);
		}

		if (lj.state === 'error')
			children.push(E('div', { 'class': 'alert-message warning' },
				'前回の切替に失敗しました (' + (lj.step || '') + '): ' + (lj.message || '')));
		else if (lj.state === 'done')
			children.push(E('div', { 'class': 'alert-message' }, lj.message || '切替が完了しました。'));

		var rows = [ sbair.row('状態', locked ? 'ロック中' : '解除済み',
			locked ? '他社 SIM は使えません' : null) ];
		// **残り試行回数を必ず出す。** 鍵を間違えるとここが減り、
		// 使い切ると戻せなくなる。件数(capacity)と取り違えないこと。
		(lk.categories || []).forEach(function(c) {
			if (!c.label)
				return;
			rows.push(sbair.row('　' + c.label,
				(c.locked ? 'ロック中' : '解除済み') +
				' — 残り試行 ' + c.remaining + '/' + c.max_retry +
				'、登録 ' + c.entries + '/' + c.capacity + ' 件'));
		});
		children.push(sbair.table(rows));

		children.push(E('div', { 'style': 'margin-top:1em' }, [
			E('button', {
				'class': 'cbi-button ' + (locked ? 'cbi-button-action' : 'cbi-button-neutral'),
				'click': ui.createHandlerFn(self, function() {
					return self.confirmLock(!locked);
				})
			}, locked ? 'ロックを解除する' : 'ロックを掛け直す')
		]));
		children.push(E('div', { 'class': 'cbi-value-description' },
			'AT+ESMLCK を直接打ちます。切替後に SIM を読み直すため電波が一度止まります。'));

		return sbair.section('SIM ロック', children);
	},

	confirmLock: function(on) {
		var self = this;
		var body = [ E('p', {}, on
			? 'SIM ロックを掛け直します。SoftBank 以外の SIM が使えなくなります。'
			: 'SIM ロックを解除します。他社の SIM が使えるようになります。') ];
		body.push(E('ul', {}, [
			E('li', {}, '電波を一度止めます (AT+CFUN=0 → 1)。通信が切れます'),
			E('li', {}, '40〜60 秒かかります'),
			on ? '' : E('li', {}, '鍵が違うと残り試行回数が減ります。使い切ると戻せません')
		]));
		return ui.showModal('SIM ロックの' + (on ? '設定' : '解除'), body.concat([
			E('div', { 'class': 'right' }, [
				E('button', { 'class': 'cbi-button', 'click': ui.hideModal }, 'やめる'),
				' ',
				E('button', {
					'class': 'cbi-button ' + (on ? 'cbi-button-negative' : 'cbi-button-action'),
					'click': ui.createHandlerFn(self, function() {
						return callLockSet(on).then(function(res) {
							ui.hideModal();
							if (res && res.error) {
								ui.addNotification(null, E('p', {}, res.error), 'warning');
								return;
							}
							self.data.lockJob = { state: 'running', step: '起動' };
							poll.add(self.watchLock, 3);
							self.redraw();
						});
					})
				}, on ? '掛け直す' : '解除する')
			])
		]));
	},

	// APN。**ICCID をキーに保存する** ので、SIM を差し替えても
	// そのカードに紐づいたものが自動で当たる。保存先は /etc/config/sbair。
	apnSection: function() {
		var self = this;
		var a = this.data.apn || {};
		var e = a.entry || {};
		var children = [];

		if (!a.iccid) {
			return sbair.section('APN', [ E('p', {}, 'SIM が読めないため設定できません。') ]);
		}

		var f = {};
		var field = function(key, label, value, ph, type) {
			f[key] = E('input', {
				'type': type || 'text',
				'class': 'cbi-input-text',
				'style': 'width:100%;max-width:24em',
				'value': value || '',
				'placeholder': ph || ''
			});
			return E('div', { 'class': 'cbi-value' }, [
				E('label', { 'class': 'cbi-value-title' }, label),
				E('div', { 'class': 'cbi-value-field' }, [ f[key] ])
			]);
		};

		children.push(sbair.table([
			sbair.row('対象の SIM', sbair.mask(a.iccid)),
			sbair.row('現在の WAN', (a.wan && a.wan.apn) ? a.wan.apn : null,
				a.applied ? '適用済み' : (e.apn ? '未適用' : null))
		]));

		children.push(field('label', '名前 (任意)', e.label, '例: au'));
		children.push(field('apn', 'APN', e.apn, '例: au.au-net.ne.jp'));

		var auth = E('select', { 'class': 'cbi-input-select' },
			Object.keys(AUTH).map(function(k) {
				return E('option', { 'value': k,
					'selected': (String(e.auth || '0') === k) ? '' : null }, AUTH[k]);
			}));
		f.auth = auth;
		children.push(E('div', { 'class': 'cbi-value' }, [
			E('label', { 'class': 'cbi-value-title' }, '認証方式'),
			E('div', { 'class': 'cbi-value-field' }, [ auth ])
		]));

		children.push(field('username', 'ユーザ名', e.username));
		children.push(field('password', 'パスワード', e.password, '', 'password'));

		var save = function(apply) {
			return callApnSet(a.iccid, (f.apn.value || '').trim(),
				f.auth.value, (f.username.value || '').trim(),
				f.password.value || '', '', (f.label.value || '').trim()
			).then(function(res) {
				if (res && res.error) {
					ui.addNotification(null, E('p', {}, res.error), 'warning');
					return;
				}
				if (!apply)
					return self.reloadApn('保存しました。');
				return callApnApply().then(function(r) {
					if (r && r.error) {
						ui.addNotification(null, E('p', {}, r.error), 'warning');
						return;
					}
					return self.reloadApn(r.note || '適用しました。');
				});
			});
		};

		children.push(E('div', { 'style': 'margin-top:1em' }, [
			// SIM に聞いて欄を埋める。**入れるだけで保存も適用もしない** —
			// 事業者から降ってきた値がその契約で正しいとは限らない。
			E('button', {
				'class': 'cbi-button cbi-button-neutral',
				'style': 'margin-right:.4em',
				'click': ui.createHandlerFn(self, function() {
					return callApnProbe().then(function(res) {
						if (!res || res.error) {
							ui.addNotification(null,
								E('p', {}, (res && res.error) || 'SIM から読み出せませんでした。'),
								'warning');
							return;
						}
						var g = res.suggestion || {};
						if (g.apn)      f.apn.value = g.apn;
						if (g.username) f.username.value = g.username;
						if (g.password) f.password.value = g.password;
						if (g.auth)     f.auth.value = g.auth;
						ui.addNotification(null,
							E('p', {}, 'SIM から読み出しました: ' + g.apn +
								'。内容を確かめてから保存してください。'), 'info');
					});
				})
			}, 'SIM から読み出す'),
			E('button', {
				'class': 'cbi-button cbi-button-action',
				'style': 'margin-right:.4em',
				'click': ui.createHandlerFn(self, function() { return save(true); })
			}, '保存して適用'),
			E('button', {
				'class': 'cbi-button cbi-button-neutral',
				'style': 'margin-right:.4em',
				'click': ui.createHandlerFn(self, function() { return save(false); })
			}, '保存のみ'),
			e.apn ? E('button', {
				'class': 'cbi-button cbi-button-negative',
				'click': ui.createHandlerFn(self, function() {
					return callApnDelete(a.iccid).then(function() {
						return self.reloadApn('削除しました。');
					});
				})
			}, '削除') : ''
		]));
		children.push(E('div', { 'class': 'cbi-value-description' },
			'保存先は /etc/config/sbair で、ICCID ごとに残ります。' +
			'起動時と SIM の切替後に、その SIM に対応するものが自動で当たります。'));

		// 他の SIM の分も一覧で見せる。差し替えたときに何が入っているか分かる。
		var others = (a.entries || []).filter(function(x) { return x.iccid !== a.iccid; });
		if (others.length) {
			var rows = [ E('tr', { 'class': 'tr table-titles' }, [
				E('th', { 'class': 'th' }, 'ICCID'),
				E('th', { 'class': 'th' }, '名前'),
				E('th', { 'class': 'th' }, 'APN')
			]) ];
			others.forEach(function(x) {
				rows.push(E('tr', { 'class': 'tr' }, [
					E('td', { 'class': 'td left' }, sbair.mask(x.iccid)),
					E('td', { 'class': 'td left' }, x.label || '-'),
					E('td', { 'class': 'td left' }, x.apn)
				]));
			});
			children.push(E('h4', { 'style': 'margin-top:1.5em' }, '他の SIM に保存済み'));
			children.push(sbair.table(rows));
		}

		return sbair.section('APN', children);
	},

	reloadApn: function(msg) {
		var self = this;
		return callApnStatus().then(function(a) {
			self.data.apn = a;
			self.redraw();
			if (msg)
				ui.addNotification(null, E('p', {}, msg), 'info');
		});
	},

	// eSIM のインストール (ES9+)。20〜30 秒かかるので、開始したら
	// esim_download_status を見に行く。
	installer: function(disabled) {
		var self = this;
		var dl = this.data.dl || {};
		var busy = (dl.state === 'running');

		if (busy) {
			return sbair.section('eSIM を追加', [
				E('div', { 'class': 'alert-message' }, [
					E('p', {}, [ E('strong', {}, 'インストール中: '), dl.step || '' ]),
					E('p', {}, 'SM-DP+ との通信が終わるまで 20〜30 秒かかります。')
				])
			]);
		}

		var code = E('input', {
			'type': 'text',
			'class': 'cbi-input-text',
			'style': 'width:100%;max-width:40em',
			'placeholder': 'LPA:1$rsp.example.com$MATCHING-ID'
		});
		var cc = E('input', {
			'type': 'text',
			'class': 'cbi-input-text',
			'style': 'width:100%;max-width:20em',
			'placeholder': '(要求される profile のみ)'
		});

		var children = [];
		if (dl.state === 'error')
			children.push(E('div', { 'class': 'alert-message warning' },
				'前回のインストールに失敗しました (' + (dl.step || '') + '): ' + (dl.message || '')));
		else if (dl.state === 'done')
			children.push(E('div', { 'class': 'alert-message' }, dl.message || 'インストールが完了しました。'));

		children.push(E('div', { 'class': 'cbi-value' }, [
			E('label', { 'class': 'cbi-value-title' }, 'アクティベーションコード'),
			E('div', { 'class': 'cbi-value-field' }, [
				code,
				E('div', { 'class': 'cbi-value-description' },
					'事業者から渡される "LPA:1$..." の文字列。QR コードの中身もこれ。')
			])
		]));
		children.push(E('div', { 'class': 'cbi-value' }, [
			E('label', { 'class': 'cbi-value-title' }, '確認コード'),
			E('div', { 'class': 'cbi-value-field' }, [ cc ])
		]));
		children.push(E('div', {}, [
			E('button', {
				'class': 'cbi-button cbi-button-action',
				'disabled': disabled ? '' : null,
				'click': ui.createHandlerFn(self, function() {
					var v = (code.value || '').trim();
					if (!v) {
						ui.addNotification(null,
							E('p', {}, 'アクティベーションコードを入れてください。'), 'warning');
						return;
					}
					return callDownload(v, (cc.value || '').trim()).then(function(res) {
						if (res && res.error) {
							ui.addNotification(null, E('p', {}, res.error), 'warning');
							return;
						}
						self.data.dl = { state: 'running', step: '起動' };
						poll.add(self.watchDl, 3);
						self.redraw();
					});
				})
			}, 'インストール')
		]));
		children.push(E('div', { 'class': 'cbi-value-description' },
			'⚠ ダウンロードは 1 回限りのことが多く、失敗すると事業者に再発行を' +
			'頼む必要があります。SM-DP+ には IMEI が渡ります。'));

		return sbair.section('eSIM を追加', children);
	},

	profileTable: function(list, disabled) {
		var self = this;
		if (!list || !list.length)
			return E('p', {}, 'profile がありません。');

		var rows = [ E('tr', { 'class': 'tr table-titles' }, [
			E('th', { 'class': 'th' }, 'ICCID'),
			E('th', { 'class': 'th' }, '状態'),
			E('th', { 'class': 'th' }, '事業者'),
			E('th', { 'class': 'th' }, '名前'),
			E('th', { 'class': 'th' }, '')
		]) ];

		list.forEach(function(p) {
			var on = (p.state === 'enabled');
			var btn = function(label, method, style) {
				return E('button', {
					'class': 'cbi-button ' + style,
					'style': 'margin-right:.4em',
					'disabled': disabled ? '' : null,
					'click': ui.createHandlerFn(self, function() {
						return self.confirmProfile(method, p);
					})
				}, label);
			};
			// 名前は付いていれば太字、無ければ profile 名を薄く出す。
			// **どちらを見ているか分かるようにする** — 名前を付けたのに
			// 変わっていないように見えるのが一番困る。
			var label = p.nickname
				? E('strong', {}, p.nickname)
				: E('span', { 'style': 'opacity:.7' }, p.name || '-');

			rows.push(E('tr', { 'class': 'tr' }, [
				E('td', { 'class': 'td left' }, sbair.mask(p.iccid)),
				E('td', { 'class': 'td left' }, stateBadge(p.state)),
				E('td', { 'class': 'td left' }, p.provider || '-'),
				E('td', { 'class': 'td left' }, [
					label, ' ',
					E('button', {
						'class': 'cbi-button cbi-button-neutral',
						'style': 'padding:0 .4em;margin-left:.3em',
						'title': '名前を付ける',
						'disabled': disabled ? '' : null,
						'click': ui.createHandlerFn(self, function() {
							return self.editNickname(p);
						})
					}, '✎')
				]),
				E('td', { 'class': 'td left' }, [
					on ? btn('無効化', 'esim_disable', 'cbi-button-neutral')
					   : btn('有効化', 'esim_enable', 'cbi-button-action'),
					btn('削除', 'esim_delete', 'cbi-button-negative')
				])
			]));
		});
		return sbair.table(rows);
	},

	// 切替は電波を止めるので、必ず確認を挟む。
	confirmSwitch: function(target) {
		var self = this;
		return ui.showModal('SIM マッピングの切替', [
			E('p', {}, sbair.simMappingLabel(target) + ' に切り替えます。'),
			E('ul', {}, [
				E('li', {}, '電波を止めます (AT+CFUN=4)。通信が切れます'),
				E('li', {}, '30〜60 秒かかり、その間 AT は無応答になります'),
				E('li', {}, '切替先に有効な profile が無ければ圏外になります'),
				E('li', {}, '内蔵 eSIM に ISD-R は無いので、そちらでは profile を操作できません')
			]),
			E('div', { 'class': 'right' }, [
				E('button', {
					'class': 'cbi-button',
					'click': ui.hideModal
				}, 'やめる'),
				' ',
				E('button', {
					'class': 'cbi-button cbi-button-action',
					'click': ui.createHandlerFn(self, function() {
						return callSimmapSet(target).then(function(res) {
							ui.hideModal();
							if (res && res.error) {
								ui.addNotification(null, E('p', {}, res.error), 'warning');
								return;
							}
							self.data.sw = { state: 'running', step: '起動', target: target };
							poll.add(self.watch, 3);
							self.redraw();
						});
					})
				}, '切り替える')
			])
		]);
	},

	// profile に名前を付ける (ES10c SetNickname)。
	// **空にすると名前が消える** — SGP.22 がそれを許すので、そのまま渡す。
	editNickname: function(p) {
		var self = this;
		var input = E('input', {
			'type': 'text',
			'class': 'cbi-input-text',
			'style': 'width:100%;max-width:24em',
			'value': p.nickname || '',
			'placeholder': p.name || '',
			'maxlength': '64'
		});
		return ui.showModal('profile の名前', [
			E('p', {}, [
				'事業者側の名前は ',
				E('strong', {}, p.provider || '-'), ' / ',
				E('strong', {}, p.name || '-'),
				'。ここで付ける名前はカードに保存され、この画面以外でも表示されます。'
			]),
			E('div', { 'class': 'cbi-value' }, [
				E('label', { 'class': 'cbi-value-title' }, '名前'),
				E('div', { 'class': 'cbi-value-field' }, [
					input,
					E('div', { 'class': 'cbi-value-description' },
						'空にすると付けた名前を消します。')
				])
			]),
			E('div', { 'class': 'right' }, [
				E('button', { 'class': 'cbi-button', 'click': ui.hideModal }, 'やめる'),
				' ',
				E('button', {
					'class': 'cbi-button cbi-button-action',
					'click': ui.createHandlerFn(self, function() {
						return callNickname(p.iccid, input.value || '').then(function(res) {
							ui.hideModal();
							if (res && res.error) {
								ui.addNotification(null, E('p', {}, res.error), 'warning');
								return;
							}
							return callStatus().then(function(st) {
								self.data.status = st;
								self.redraw();
							});
						});
					})
				}, '保存')
			])
		]);
	},

	confirmProfile: function(method, p) {
		var self = this;
		var label = { esim_enable: '有効化', esim_disable: '無効化', esim_delete: '削除' }[method];
		var body = [
			E('p', {}, [ E('strong', {}, p.nickname || p.name || p.iccid),
			             ' を' + label + 'します。' ])
		];
		if (method === 'esim_delete')
			body.push(E('div', { 'class': 'alert-message warning' },
				'削除は取り消せません。同じ profile を入れ直すには、事業者から ' +
				'activation code を再発行してもらう必要があります。'));
		else
			body.push(E('p', {}, 'カードが再初期化 (REFRESH) されるため、' +
				'反映まで数秒かかります。'));

		return ui.showModal('profile の' + label, body.concat([
			E('div', { 'class': 'right' }, [
				E('button', { 'class': 'cbi-button', 'click': ui.hideModal }, 'やめる'),
				' ',
				E('button', {
					'class': 'cbi-button ' +
						(method === 'esim_delete' ? 'cbi-button-negative' : 'cbi-button-action'),
					'click': ui.createHandlerFn(self, function() {
						return callProfileOp(method, p.iccid).then(function(res) {
							ui.hideModal();
							if (res && res.error) {
								ui.addNotification(null, E('p', {}, res.error), 'warning');
								return;
							}
							// enable/disable の直後はカードが REFRESH 中で
							// AT+CCHO を蹴る。少し待ってから読み直す。
							return new Promise(function(resolve) {
								window.setTimeout(resolve, res && res.refresh_pending ? 6000 : 1000);
							}).then(function() {
								return callStatus().then(function(st) {
									self.data.status = st;
									self.redraw();
								});
							});
						});
					})
				}, label)
			])
		]));
	},

	handleSave: null,
	handleSaveApply: null,
	handleReset: null
});
