#!/usr/bin/ucode
// listok-agent — агент listok на роутере с forkop. Описание: docs/router-agent.md.
//
//   listok-agent run            цикл синхронизации всех секций (его запускает procd)
//   listok-agent sync <секция>  одна загрузка фида без forkop list_update (для установщика)
//   listok-agent status         что настроено и что лежит на роутере
//   listok-agent uninstall      убрать агент и его файлы из forkop
//
// Режимы (option mode в /etc/config/listok):
//   wait — POST /agent/v1/wait держит одно соединение на все секции, изменения за секунды;
//   poll — та же проверка без ожидания раз в option interval секунд (для слабых роутеров).
//
// Для проверки без изменений на роутере: LISTOK_UCI_DIR, LISTOK_STATE_DIR, LISTOK_TMP_DIR
// переносят конфиг и файлы в другой каталог, LISTOK_DRY_RUN=1 не вызывает forkop.
'use strict';

import * as fs from 'fs';
import { cursor } from 'uci';

const VERSION = '0.2.0';
const UCI_DIR = getenv('LISTOK_UCI_DIR') || '/etc/config';
const STATE_DIR = getenv('LISTOK_STATE_DIR') || '/etc/listok';
const TMP_DIR = getenv('LISTOK_TMP_DIR') || '/tmp/listok';
const DRY_RUN = getenv('LISTOK_DRY_RUN') == '1';
const DEBUG = getenv('LISTOK_DEBUG') == '1';

const FORKOP = '/usr/bin/forkop';
const FORKOP_PID = '/var/run/forkop_list_update.pid';
const RULESET_DIR = '/tmp/sing-box/rulesets';
const SENTINEL = 'listok-sentinel.invalid';
const WAIT_S = 55;            // wait: сервер держит запрос до стольких секунд (не больше LISTOK_LONGPOLL_MAX)
const POLL_DEFAULT_S = 300;
const BACKOFF_MAX_S = 300;

function log(level, msg) {
	system([ 'logger', '-t', 'listok-agent', '-p', 'daemon.' + level, msg ]);
	if (DEBUG)
		warn(level + ': ' + msg + '\n');
}

function die(msg) {
	log('err', msg);
	warn('listok-agent: ' + msg + '\n');
	exit(1);
}

function shq(s) {
	return "'" + replace('' + s, "'", "'\\''") + "'";
}

// run — запуск команды с захватом stdout. Аргументы экранируются, shell не интерпретирует их.
function run(args) {
	let p = fs.popen(join(' ', map(args, shq)) + ' 2>/dev/null', 'r');
	if (!p)
		return '';
	let out = p.read('all') || '';
	p.close();
	return out;
}

function config() {
	let c = cursor(UCI_DIR);
	let mode = c.get('listok', 'agent', 'mode') || 'wait';
	let cfg = {
		server: c.get('listok', 'agent', 'server'),
		server_ip: c.get('listok', 'agent', 'server_ip') || '',
		proxy: c.get('listok', 'agent', 'proxy') || '',
		token: c.get('listok', 'agent', 'token'),
		mode: mode == 'poll' ? 'poll' : 'wait',
		interval: max(int(c.get('listok', 'agent', 'interval') || POLL_DEFAULT_S) || POLL_DEFAULT_S, 60),
		feeds: {},
		uci_names: {}
	};
	c.foreach('listok', 'feed', function(s) {
		if (s.section && s.token) {
			cfg.feeds[s.section] = s.token;
			cfg.uci_names[s.section] = s['.name'];
		}
	});
	if (!cfg.server || !cfg.token)
		die('в /etc/config/listok нет server или token');
	return cfg;
}

// curl_base — общие параметры curl: таймаут соединения, --resolve на адрес сервера в LAN (D-028), прокси.
function curl_base(cfg) {
	let args = [ 'curl', '-sS', '--connect-timeout', '10' ];
	if (cfg.server_ip) {
		let m = match(cfg.server, /^(https?):\/\/([^\/:]+)(:([0-9]+))?/);
		if (m) {
			let port = m[4] || (m[1] == 'https' ? '443' : '80');
			let ip = index(cfg.server_ip, ':') >= 0 ? '[' + cfg.server_ip + ']' : cfg.server_ip;
			push(args, '--resolve', m[2] + ':' + port + ':' + ip);
		}
	}
	if (cfg.proxy)
		push(args, '-x', cfg.proxy);
	return args;
}

// fetch — скачать фид секции. Возвращает {code, etag, body}; code 0 — сеть недоступна.
function fetch(cfg, section) {
	fs.mkdir(TMP_DIR);
	let body = TMP_DIR + '/' + section + '.body', hdr = TMP_DIR + '/' + section + '.hdr';
	fs.unlink(body);
	fs.unlink(hdr);
	let args = curl_base(cfg);
	push(args, '-o', body, '-D', hdr, '-w', '%{http_code}', '-m', '60',
		cfg.server + '/f/' + cfg.feeds[section] + '.lst');
	let code = int(trim(run(args))) || 0;
	let etag = '';
	for (let line in split(fs.readfile(hdr) || '', '\n')) {
		let m = match(trim(line), /^etag:[ \t]*(.+)$/i);
		if (m)
			etag = trim(m[1]);
	}
	return { code: code, etag: etag, body: body };
}

// validate — фид похож на настоящий: первой строкой сторожевая запись, дальше домены и подсети.
// Пустой или испорченный файл в forkop не попадёт (forkop-integration.md, П-6).
function validate(path) {
	let text = fs.readfile(path);
	if (!text)
		return 'пустой ответ';
	let lines = split(trim(text), '\n');
	if (lines[0] != SENTINEL)
		return 'нет сторожевой записи в начале фида';
	for (let i = 1; i < length(lines); i++) {
		let l = lines[i];
		if (!match(l, /^[a-z0-9_-]+(\.[a-z0-9_-]+)+$/) &&
		    !match(l, /^[0-9]{1,3}(\.[0-9]{1,3}){3}\/[0-9]{1,2}$/) &&
		    !match(l, /^[0-9a-f:]+\/[0-9]{1,3}$/))
			return 'строка ' + (i + 1) + ' не похожа на домен или подсеть';
	}
	return null;
}

function list_path(section) {
	return STATE_DIR + '/' + section + '.lst';
}

function etag_of(section) {
	return trim(fs.readfile(STATE_DIR + '/' + section + '.etag') || '');
}

// store — положить фид на место атомарно. На флеш пишем только при реальном изменении.
function store(section, path, etag) {
	let text = fs.readfile(path);
	let target = list_path(section);
	fs.mkdir(STATE_DIR);
	let changed = fs.readfile(target) != text;
	if (changed) {
		fs.writefile(target + '.new', text);
		fs.rename(target + '.new', target);
	}
	if (etag_of(section) != etag)
		fs.writefile(STATE_DIR + '/' + section + '.etag', etag + '\n');
	return changed;
}

function forkop_busy() {
	let pid = trim(fs.readfile(FORKOP_PID) || '');
	return pid != '' && fs.access('/proc/' + pid);
}

// apply — один forkop list_update на все изменившиеся секции (он и так обновляет все разом).
// Код выхода ничего не говорит (D-029): успех секции — её rule-set пересобран после вызова.
// Возвращает {секция: {ok, error}}.
function apply(sections) {
	let res = {};
	if (DRY_RUN) {
		for (let s in sections)
			res[s] = { ok: true };
		return res;
	}
	let pending = sections;
	for (let attempt = 1; attempt <= 3 && length(pending); attempt++) {
		for (let i = 0; i < 100 && forkop_busy(); i++)
			sleep(3000);
		let t0 = time();
		system([ FORKOP, 'list_update' ], 600000);
		let left = [];
		for (let s in pending) {
			let ruleset = RULESET_DIR + '/' + s + '-lists-ruleset.json';
			let st = fs.stat(ruleset);
			if (st && st.mtime >= t0 && index(fs.readfile(ruleset) || '', SENTINEL) >= 0)
				res[s] = { ok: true };
			else
				push(left, s);
		}
		pending = left;
		if (length(pending))
			sleep(10000);
	}
	for (let s in pending)
		res[s] = { ok: false, error: 'forkop list_update не пересобрал rule-set секции ' + s };
	return res;
}

// post — JSON-запрос к /agent/v1. Возвращает {code, body}; code 0 — сеть недоступна.
function post(cfg, path, payload, timeout) {
	fs.mkdir(TMP_DIR);
	let file = TMP_DIR + '/post.json', out = TMP_DIR + '/post.out';
	fs.writefile(file, sprintf('%J', payload));
	fs.unlink(out);
	let args = curl_base(cfg);
	push(args, '-m', '' + (timeout || 15), '-o', out, '-w', '%{http_code}',
		'-H', 'Authorization: Bearer ' + cfg.token, '-H', 'Content-Type: application/json',
		'--data-binary', '@' + file, cfg.server + path);
	let code = int(trim(run(args))) || 0;
	let body = fs.readfile(out) || '';
	fs.unlink(file);
	return { code: code, body: body };
}

function installed_version(name) {
	for (let line in split(run([ 'opkg', 'list-installed' ]), '\n')) {
		let m = match(line, /^([^ ]+) - (.+)$/);
		if (m && (m[1] == name || index(m[1], name + '-') == 0))
			return m[2];
	}
	return '';
}

// hello — версии и режим на сервер. Если ссылку фида перевыпустили, сервер вернёт новый токен:
// агент записывает его в /etc/config/listok и продолжает без переустановки.
function hello(cfg) {
	let r = post(cfg, '/agent/v1/hello', {
		agent_version: VERSION,
		forkop_version: installed_version('forkop'),
		singbox_version: installed_version('sing-box'),
		mode: cfg.mode,
		interval_s: cfg.mode == 'poll' ? cfg.interval : 0,
		sections: keys(cfg.feeds)
	});
	if (r.code == 401)
		log('err', 'hello: токен агента не принят (агент отвязан на сервере?) — переустановите агент');
	if (r.code != 200) {
		log('warning', 'hello: сервер ответил ' + r.code);
		return r.code;
	}
	let resp = json(r.body);
	let c = null;
	for (let f in resp?.feeds || []) {
		if (!cfg.feeds[f.section]) {
			log('warning', 'на сервере есть фид секции ' + f.section + ', которой нет в /etc/config/listok — переустановите агент');
			continue;
		}
		if (f.token && f.token != cfg.feeds[f.section]) {
			c = c || cursor(UCI_DIR);
			c.set('listok', cfg.uci_names[f.section], 'token', f.token);
			cfg.feeds[f.section] = f.token;
			log('info', 'ссылку фида секции ' + f.section + ' перевыпустили, новый токен записан');
		}
	}
	if (c)
		c.commit('listok');
	return 200;
}

function report(cfg, section, etag, res) {
	let r = post(cfg, '/agent/v1/applied', { section: section, etag: etag, ok: res.ok, error: res.error || '' });
	if (r.code != 204)
		log('warning', 'отчёт о применении секции ' + section + ': сервер ответил ' + r.code);
}

// sync_changed — скачать изменившиеся секции, проверить, положить; одним list_update применить.
// Возвращает false при ошибке сети или сервера (тогда цикл уходит в паузу).
function sync_changed(cfg, sections) {
	let applied = [], etags = {}, ok = true;
	for (let s in sections) {
		let r = fetch(cfg, s);
		if (r.code == 404) {
			// Ссылку могли перевыпустить: hello подтянет новый токен, пробуем ещё раз.
			let old = cfg.feeds[s];
			if (hello(cfg) == 200 && cfg.feeds[s] != old)
				r = fetch(cfg, s);
		}
		if (r.code == 404) {
			log('warning', 'фид секции ' + s + ' не найден (404)');
			continue;
		}
		if (r.code != 200) {
			log('warning', 'фид секции ' + s + ': сервер ответил ' + r.code);
			ok = false;
			continue;
		}
		let bad = validate(r.body);
		if (bad) {
			log('err', 'фид секции ' + s + ' отклонён: ' + bad);
			ok = false;
			continue;
		}
		etags[s] = r.etag;
		if (store(s, r.body, r.etag))
			push(applied, s);
		else
			report(cfg, s, r.etag, { ok: true });
	}
	if (length(applied)) {
		log('info', 'новые версии фидов: ' + join(', ', applied) + ', применяю');
		let res = apply(applied);
		for (let s in applied) {
			log(res[s].ok ? 'info' : 'err', res[s].ok ? 'применено ' + s + ' ' + etags[s] : res[s].error);
			report(cfg, s, etags[s], res[s]);
		}
	}
	return ok;
}

function cmd_run() {
	let cfg = config();
	if (!length(keys(cfg.feeds)))
		die('в /etc/config/listok нет ни одного фида');
	let backoff = 5, next_hello = 0;
	log('info', 'агент ' + VERSION + ' запущен: секции ' + join(', ', sort(keys(cfg.feeds))) +
		(cfg.mode == 'poll' ? ', проверка раз в ' + cfg.interval + ' с' : ', мгновенный режим'));
	while (true) {
		if (time() >= next_hello) {
			let code = hello(cfg);
			next_hello = time() + (code == 200 ? 3600 : 60);
		}
		let etags = {};
		for (let s in keys(cfg.feeds))
			etags[s] = etag_of(s);
		let wait = cfg.mode == 'poll' ? 0 : WAIT_S;
		let t0 = time();
		let r = post(cfg, '/agent/v1/wait', { feeds: etags, wait: wait }, wait + 25);
		let resp = r.code == 200 ? json(r.body) : null;
		if (!resp) {
			if (r.code == 401) {
				log('err', 'токен агента не принят — агент отвязан на сервере, жду час');
				sleep(3600 * 1000);
				continue;
			}
			log('warning', 'нет связи с сервером (код ' + r.code + '), повтор через ' + backoff + ' с');
			sleep(backoff * 1000);
			backoff = min(backoff * 2, BACKOFF_MAX_S);
			continue;
		}
		if (length(resp.gone || [])) {
			log('warning', 'фиды секций ' + join(', ', resp.gone) + ' недоступны (удалены, выключены или перевыпущены) — спрашиваю сервер');
			if (hello(cfg) == 200)
				next_hello = time() + 3600;
		}
		if (length(resp.changed || []) && !sync_changed(cfg, resp.changed)) {
			sleep(backoff * 1000);
			backoff = min(backoff * 2, BACKOFF_MAX_S);
			continue;
		}
		backoff = 5;
		if (cfg.mode == 'poll')
			sleep(cfg.interval * 1000);
		else if (length(resp.gone || []))
			sleep(BACKOFF_MAX_S * 1000); // фид пропал: не долбить сервер, пока его не вернут
		else if (time() - t0 < 5 && !length(resp.changed || []))
			sleep(30000); // сервер не держит long-poll: не долбить его
	}
}

function cmd_sync(section) {
	let cfg = config();
	if (!cfg.feeds[section])
		die('нет фида секции ' + section + ' в /etc/config/listok');
	let r = fetch(cfg, section);
	if (r.code != 200)
		die('фид секции ' + section + ': сервер ответил ' + r.code);
	let bad = validate(r.body);
	if (bad)
		die('фид секции ' + section + ' отклонён: ' + bad);
	store(section, r.body, r.etag);
	print('секция ' + section + ': ' + (length(split(trim(fs.readfile(list_path(section))), '\n')) - 1) + ' записей\n');
}

function cmd_status() {
	let cfg = config();
	print('агент ' + VERSION + ', сервер ' + cfg.server + (cfg.server_ip ? ' (через ' + cfg.server_ip + ')' : '') +
		(cfg.mode == 'poll' ? ', проверка раз в ' + cfg.interval + ' с' : ', мгновенный режим') + '\n');
	for (let section in sort(keys(cfg.feeds))) {
		let text = fs.readfile(list_path(section));
		let n = text ? length(split(trim(text), '\n')) - 1 : 0;
		print(sprintf('  %-12s %s записей, версия %s\n', section, text ? '' + n : 'нет файла,', etag_of(section) || '—'));
	}
}

// cmd_uninstall — убрать локальные пути listok из forkop и удалить агент. forkop перезапустится.
function cmd_uninstall() {
	let c = cursor();
	c.foreach('forkop', 'section', function(s) {
		let lists = s.domain_ip_lists;
		if (type(lists) == 'string')
			lists = [ lists ];
		if (type(lists) != 'array')
			return;
		let keep = filter(lists, (v) => index(v, STATE_DIR + '/') != 0);
		if (length(keep) == length(lists))
			return;
		if (length(keep))
			c.set('forkop', s['.name'], 'domain_ip_lists', keep);
		else
			c.delete('forkop', s['.name'], 'domain_ip_lists');
	});
	c.commit('forkop');
	// procd запускает агент как ucode, по имени процесса его не найти — останавливаем через init.
	system([ '/etc/init.d/listok-agent', 'stop' ]);
	system([ '/etc/init.d/listok-agent', 'disable' ]);
	system([ '/etc/init.d/forkop', 'reload' ]);
	for (let f in [ '/etc/config/listok', '/etc/init.d/listok-agent', '/usr/bin/listok-agent' ])
		fs.unlink(f);
	system([ 'rm', '-rf', STATE_DIR, TMP_DIR ]);
	print('агент удалён, списки listok убраны из forkop\n');
}

let cmd = ARGV[0];
if (cmd == 'run')
	cmd_run();
else if (cmd == 'sync' && ARGV[1])
	cmd_sync(ARGV[1]);
else if (cmd == 'status')
	cmd_status();
else if (cmd == 'uninstall')
	cmd_uninstall();
else if (cmd == 'version')
	print(VERSION + '\n');
else {
	warn('использование: listok-agent run | sync <секция> | status | uninstall | version\n');
	exit(2);
}
