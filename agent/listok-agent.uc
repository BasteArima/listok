#!/usr/bin/ucode
// listok-agent — агент listok на роутере с forkop. Описание: docs/router-agent.md.
//
//   listok-agent run                цикл синхронизации всех секций (его запускает procd)
//   listok-agent sync <секция>      одна загрузка фида без применения в forkop (для установщика)
//   listok-agent check <секции…>    есть ли такие секции в forkop (для установщика)
//   listok-agent attach <секции…>   подключить файлы listok к секциям forkop (для установщика)
//   listok-agent status             что настроено и что лежит на роутере
//   listok-agent uninstall          убрать агент и его файлы из forkop
//
// forkop 1.x: файл секции стоит в domain_ip_lists, применение — forkop list_update.
// forkop 2.x: файл — локальный rule set ядра (rule_set_with_subnets), ядро само перечитывает
// его при изменении, а подсети в nft переносит служба forkop. Описание: docs/forkop-integration.md.
//
// Режимы (option mode в /etc/config/listok):
//   wait — POST /agent/v1/wait держит одно соединение на все секции, изменения за секунды;
//   poll — та же проверка без ожидания раз в option interval секунд (для слабых роутеров).
//
// Для проверки без изменений на роутере: LISTOK_UCI_DIR, LISTOK_STATE_DIR, LISTOK_TMP_DIR
// переносят конфиги (и /etc/config/forkop) и файлы в другой каталог, LISTOK_DRY_RUN=1 не вызывает forkop.
'use strict';

import * as fs from 'fs';
import { cursor } from 'uci';

const VERSION = '0.3.1';
const UCI_DIR = getenv('LISTOK_UCI_DIR') || '/etc/config';
const STATE_DIR = getenv('LISTOK_STATE_DIR') || '/etc/listok';
const TMP_DIR = getenv('LISTOK_TMP_DIR') || '/tmp/listok';
const DRY_RUN = getenv('LISTOK_DRY_RUN') == '1';
const DEBUG = getenv('LISTOK_DEBUG') == '1';

const FORKOP = '/usr/bin/forkop';
const FORKOP_PID = '/var/run/forkop_list_update.pid';
const RULESET_DIR = '/tmp/sing-box/rulesets';
const SENTINEL = 'listok-sentinel.invalid';
const LIST_KEYS = [ 'domain_ip_lists', 'rule_set', 'rule_set_with_subnets' ]; // где в forkop бывают пути к спискам
const RELOAD_WAIT_S = 15;     // forkop 2: сколько ждать, пока ядро перечитает файл
const CLASH_PORT = '9090';    // порт clash API, если в конфиге sing-box он не указан
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

function parse(text) {
	try {
		return json(text);
	}
	catch (e) {
		return null;
	}
}

function as_list(v) {
	return type(v) == 'array' ? v : (type(v) == 'string' ? [ v ] : []);
}

function shq(s) {
	return "'" + replace('' + s, "'", "'\\''") + "'";
}

// run — запуск команды с захватом stdout. Аргументы экранируются, shell не интерпретирует их.
function run(args) {
	let p = fs.popen(join(' ', map(args, shq)) + ' 2>/dev/null </dev/null', 'r');
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

// server_addr — хост, порт и адрес соединения сервера (с учётом server_ip).
function server_addr(cfg) {
	let m = match(cfg.server, /^(https?):\/\/([^\/:]+)(:([0-9]+))?/);
	if (!m)
		return null;
	return { https: m[1] == 'https', host: m[2], port: m[4] || (m[1] == 'https' ? '443' : '80'),
		connect: cfg.server_ip || m[2] };
}

// detect_ca — корень, которым подписан сертификат сервера, из /etc/ssl/certs (D-034).
// С полным хранилищем (~150 корней) curl тратит на разбор ~0,3 с процессора на каждый запрос,
// с одним нужным корнем — вчетверо меньше; проверка сертификата та же. Нет openssl или корня — ''
// (curl возьмёт полное хранилище).
function detect_ca(cfg) {
	let a = server_addr(cfg);
	if (!a || !a.https || !fs.access('/usr/bin/openssl'))
		return '';
	let connect = index(a.connect, ':') >= 0 ? '[' + a.connect + ']' : a.connect;
	let chain = run([ 'openssl', 's_client', '-connect', connect + ':' + a.port, '-servername', a.host, '-showcerts' ]);
	fs.mkdir(TMP_DIR);
	let tmp = TMP_DIR + '/chain.pem';
	for (let pem in match(chain, /-----BEGIN CERTIFICATE-----[^-]+-----END CERTIFICATE-----/g) || []) {
		fs.writefile(tmp, pem[0] + '\n');
		let h = trim(run([ 'openssl', 'x509', '-in', tmp, '-noout', '-issuer_hash' ]));
		for (let i = 0; h != '' && i < 4; i++) {
			let f = '/etc/ssl/certs/' + h + '.' + i;
			if (fs.access(f)) {
				fs.unlink(tmp);
				return f;
			}
		}
	}
	fs.unlink(tmp);
	return '';
}

// curl_base — общие параметры curl: таймаут соединения, --resolve на адрес сервера в LAN (D-028),
// прокси, один корень вместо всего хранилища (D-034).
function curl_base(cfg) {
	let args = [ 'curl', '-sS', '--connect-timeout', '10' ];
	if (cfg.ca)
		push(args, '--cacert', cfg.ca);
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

// forkop_major — основная версия forkop по settings.config_version (у 1.x её нет).
function forkop_major() {
	let v = cursor(UCI_DIR).get('forkop', 'settings', 'config_version') || '';
	return int(split(v, '.')[0]) || 1;
}

// forkop_section — UCI-имя секции forkop по её названию. В forkop 2 секции, созданные в LuCI,
// анонимные (cfg…), а колонка «Name» — это option label; в 1.x название и есть имя секции.
function forkop_section(c, name) {
	let found = null;
	c.foreach('forkop', 'section', function(s) {
		if (found == null && s.label == name)
			found = s['.name'];
	});
	if (found == null && c.get('forkop', name) == 'section')
		found = name;
	return found;
}

// list_key — опция секции, откуда forkop берёт локальный файл со списком.
function list_key(c, sec, major) {
	if (major < 2)
		return 'domain_ip_lists';
	// Как миграция forkop 2.0.0 переносит domain_ip_lists: DNS-секция берёт из набора только домены.
	return c.get('forkop', sec, 'action') == 'dns' ? 'rule_set' : 'rule_set_with_subnets';
}

// singbox_conf — конфиг sing-box, который сгенерировал forkop 2: в нём теги rule set и clash API.
function singbox_conf() {
	let path = cursor(UCI_DIR).get('forkop', 'settings', 'config_path') || '/etc/sing-box/config.json';
	return parse(fs.readfile(path) || '');
}

// clash_api — адрес и секрет clash API ядра; 0.0.0.0 и :: — это локальный адрес.
function clash_api(conf) {
	let ctl = conf?.experimental?.clash_api;
	if (!ctl)
		return null;
	let addr = ctl.external_controller || '', host = '127.0.0.1', port = CLASH_PORT;
	let i = rindex(addr, ':');
	if (i >= 0) {
		host = substr(addr, 0, i);
		port = substr(addr, i + 1) || CLASH_PORT;
	}
	if (host == '' || host == '0.0.0.0' || host == '::' || host == '[::]')
		host = '127.0.0.1';
	return { url: 'http://' + host + ':' + port, secret: ctl.secret || '' };
}

// ruleset_tag — тег, под которым forkop подключил файл секции к ядру; null — не подключил.
function ruleset_tag(conf, section) {
	for (let r in conf?.route?.rule_set || [])
		if (r.type == 'local' && r.path == list_path(section))
			return r.tag;
	return null;
}

// revision — ревизия rule set в ядре: растёт при каждом перечитывании файла. null — ядро не ответило.
function revision(api, tag) {
	let args = [ 'curl', '-sS', '-m', '5', '--noproxy', '*' ];
	if (api.secret)
		push(args, '-H', 'Authorization: Bearer ' + api.secret);
	push(args, api.url + '/providers/rules/' + tag);
	let r = parse(run(args));
	return type(r?.revision) == 'int' ? r.revision : null;
}

// probe — для forkop 2: тег rule set каждой секции и его ревизия сейчас (до записи файла).
function probe(sections) {
	let conf = singbox_conf(), api = clash_api(conf), out = {};
	for (let s in sections) {
		let tag = ruleset_tag(conf, s);
		out[s] = { tag: tag, rev: tag && api ? revision(api, tag) : null };
	}
	return { api: api, sections: out };
}

function not_attached(s) {
	return 'в конфиге sing-box нет rule set с ' + list_path(s) + ' — forkop не подключил файл секции ' + s +
		', нужен его перезапуск или переустановка агента';
}

const NO_API = 'clash API ядра forkop не отвечает — не проверить, подхвачен ли список';

function forkop_busy() {
	let pid = trim(fs.readfile(FORKOP_PID) || '');
	return pid != '' && fs.access('/proc/' + pid);
}

// apply_core — forkop 2: ядро перечитывает файл само, как только он сменился, а подсети в nft
// переносит служба forkop по событию ядра (D-036). Успех — ревизия rule set сменилась.
function apply_core(sections, before) {
	let res = {}, pending = [];
	for (let s in sections) {
		let p = before.sections[s];
		if (!p.tag)
			res[s] = { ok: false, error: not_attached(s) };
		else if (p.rev == null)
			res[s] = { ok: false, error: NO_API };
		else
			push(pending, s);
	}
	for (let i = 0; i < RELOAD_WAIT_S * 2 && length(pending); i++) {
		sleep(500);
		pending = filter(pending, function(s) {
			let p = before.sections[s], r = revision(before.api, p.tag);
			if (r == null || r == p.rev)
				return true;
			res[s] = { ok: true };
			return false;
		});
	}
	for (let s in pending)
		res[s] = { ok: false, error: 'ядро не перечитало файл секции ' + s + ' за ' + RELOAD_WAIT_S + ' с — смотрите logread -e sing-box' };
	return res;
}

// apply_list_update — forkop 1.x: один forkop list_update на все изменившиеся секции (он и так
// обновляет все разом). Код выхода ничего не говорит (D-029): успех секции — её rule-set пересобран.
function apply_list_update(sections) {
	let res = {};
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

// apply — применить изменившиеся секции. before — probe до записи файлов (forkop 2) или null.
// Возвращает {секция: {ok, error}}.
function apply(sections, before) {
	let res = {};
	if (DRY_RUN) {
		for (let s in sections)
			res[s] = { ok: true };
		return res;
	}
	return before ? apply_core(sections, before) : apply_list_update(sections);
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
		forkop_version: installed_version('forkop') || cursor(UCI_DIR).get('forkop', 'settings', 'config_version') || '',
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

// sync_changed — скачать изменившиеся секции, проверить, положить и применить все разом.
// Возвращает false при ошибке сети или сервера (тогда цикл уходит в паузу).
function sync_changed(cfg, sections) {
	let applied = [], etags = {}, ok = true;
	// forkop 2: ревизии до записи, чтобы потом увидеть, что ядро перечитало файл.
	let before = !DRY_RUN && forkop_major() >= 2 ? probe(sections) : null;
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
		let res = apply(applied, before);
		for (let s in applied) {
			log(res[s].ok ? 'info' : 'err', res[s].ok ? 'применено ' + s + ' ' + etags[s] : res[s].error);
			report(cfg, s, etags[s], res[s]);
		}
	}
	return ok;
}

// pin_ca — найти корень сервера и убедиться, что с ним запрос проходит; иначе полное хранилище.
function pin_ca(cfg) {
	cfg.ca = detect_ca(cfg);
	if (!cfg.ca)
		return;
	let args = curl_base(cfg);
	push(args, '-m', '15', '-o', '/dev/null', '-w', '%{http_code}', cfg.server + '/healthz');
	if (trim(run(args)) != '200') {
		log('info', 'проверка с корнем ' + cfg.ca + ' не прошла, использую полное хранилище сертификатов');
		cfg.ca = '';
	}
}

// loaded — подхвачен ли сейчас forkop'ом файл каждой секции: {секция: {ok, error}}.
function loaded(sections) {
	let res = {};
	if (forkop_major() >= 2) {
		// Сразу после установки ядро может ещё стартовать: ждём clash API до полуминуты.
		let p = probe(sections);
		for (let i = 0; i < 15 && length(filter(sections, (s) => p.sections[s].tag && p.sections[s].rev == null)); i++) {
			sleep(2000);
			p = probe(sections);
		}
		for (let s in sections) {
			let x = p.sections[s];
			res[s] = !x.tag ? { ok: false, error: not_attached(s) } : (x.rev == null ? { ok: false, error: NO_API } : { ok: true });
		}
		return res;
	}
	for (let s in sections) {
		let ruleset = fs.readfile(RULESET_DIR + '/' + s + '-lists-ruleset.json') || '';
		res[s] = index(ruleset, SENTINEL) >= 0 ? { ok: true } :
			{ ok: false, error: 'в rule-set секции ' + s + ' нет списка listok — forkop не подхватил файл, нужен его перезапуск' };
	}
	return res;
}

// report_state — при старте доложить, что сейчас применено: файл есть и forkop его подхватил.
// Так статус на сервере виден сразу после установки или перезагрузки, а не после первой правки.
function report_state(cfg) {
	let ready = filter(keys(cfg.feeds), (s) => etag_of(s) && fs.access(list_path(s)));
	let state = DRY_RUN ? null : loaded(ready);
	for (let s in ready)
		report(cfg, s, etag_of(s), state ? state[s] : { ok: true });
}

function cmd_run() {
	let cfg = config();
	if (!length(keys(cfg.feeds)))
		die('в /etc/config/listok нет ни одного фида');
	pin_ca(cfg);
	let backoff = 5, next_hello = 0, reported = false;
	log('info', 'агент ' + VERSION + ' запущен: секции ' + join(', ', sort(keys(cfg.feeds))) +
		(cfg.mode == 'poll' ? ', проверка раз в ' + cfg.interval + ' с' : ', мгновенный режим') +
		(cfg.ca ? ', корень ' + cfg.ca : ''));
	while (true) {
		if (time() >= next_hello) {
			let code = hello(cfg);
			next_hello = time() + (code == 200 ? 3600 : 60);
			if (code == 200 && !reported) {
				report_state(cfg);
				reported = true;
			}
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
			if (r.code == 0 && cfg.ca)
				pin_ca(cfg); // сертификат сервера могли перевыпустить другим корнем
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

// cmd_check — есть ли секции в forkop. Ничего не меняет: установщик зовёт его до любых правок.
function cmd_check(sections) {
	let c = cursor(UCI_DIR), bad = filter(sections, (s) => !forkop_section(c, s));
	for (let s in bad)
		warn('listok: ОШИБКА: в forkop нет секции ' + s + ' (ищу по названию в LuCI и по имени в /etc/config/forkop)' +
			' — создайте её или исправьте фид в listok\n');
	exit(length(bad) ? 1 : 0);
}

// cmd_attach — подключить файлы listok к секциям forkop. Путь к файлу остаётся ровно в одной опции
// секции: в LuCI forkop 2 набор лежит либо в rule_set, либо в rule_set_with_subnets, и путь,
// добавленный руками без флажка подсетей, иначе оказался бы в обеих. Ссылки на фиды этого сервера
// (если их вставляли вручную) убираются. Если настройки изменились — единственный перезапуск forkop;
// при переустановке с теми же секциями forkop не трогается.
function cmd_attach(sections) {
	let cfg = config();
	let c = cursor(UCI_DIR), major = forkop_major(), changed = false;
	for (let name in sections) {
		let sec = forkop_section(c, name);
		if (!sec)
			die('в forkop нет секции ' + name);
		let target = list_key(c, sec, major), path = list_path(name);
		for (let key in LIST_KEYS) {
			let cur = as_list(c.get('forkop', sec, key));
			let want = filter(cur, function(v) {
				if (index(v, cfg.server + '/f/') == 0) {
					print('listok: секция ' + name + ': ссылка на фид заменена локальным файлом\n');
					return false;
				}
				return v != path;
			});
			if (key == target)
				push(want, path);
			if (join('\n', cur) == join('\n', want))
				continue;
			if (length(want))
				c.set('forkop', sec, key, want);
			else
				c.delete('forkop', sec, key);
			changed = true;
		}
	}
	if (!changed) {
		print('listok: настройки forkop уже верные, перезапуск не нужен\n');
		return;
	}
	c.commit('forkop');
	print('listok: перезапуск forkop (единственный: дальше списки обновляются без перезапуска sing-box)\n');
	if (!DRY_RUN)
		system([ '/etc/init.d/forkop', 'reload' ]);
}

function cmd_status() {
	let cfg = config();
	print('агент ' + VERSION + ', сервер ' + cfg.server + (cfg.server_ip ? ' (через ' + cfg.server_ip + ')' : '') +
		(cfg.mode == 'poll' ? ', проверка раз в ' + cfg.interval + ' с' : ', мгновенный режим') +
		(forkop_major() >= 2 ? ', forkop 2: списки перечитывает ядро' : ', forkop 1: списки применяет forkop list_update') + '\n');
	for (let section in sort(keys(cfg.feeds))) {
		let text = fs.readfile(list_path(section));
		let n = text ? length(split(trim(text), '\n')) - 1 : 0;
		print(sprintf('  %-12s %s записей, версия %s\n', section, text ? '' + n : 'нет файла,', etag_of(section) || '—'));
	}
}

// cmd_uninstall — убрать локальные пути listok из forkop и удалить агент. forkop перезапустится.
function cmd_uninstall() {
	let c = cursor(UCI_DIR);
	c.foreach('forkop', 'section', function(s) {
		for (let key in LIST_KEYS) {
			let lists = as_list(s[key]);
			let keep = filter(lists, (v) => index(v, STATE_DIR + '/') != 0);
			if (length(keep) == length(lists))
				continue;
			if (length(keep))
				c.set('forkop', s['.name'], key, keep);
			else
				c.delete('forkop', s['.name'], key);
		}
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
else if (cmd == 'check' && length(ARGV) > 1)
	cmd_check(slice(ARGV, 1));
else if (cmd == 'attach' && length(ARGV) > 1)
	cmd_attach(slice(ARGV, 1));
else if (cmd == 'status')
	cmd_status();
else if (cmd == 'uninstall')
	cmd_uninstall();
else if (cmd == 'version')
	print(VERSION + '\n');
else {
	warn('использование: listok-agent run | sync <секция> | check <секции…> | attach <секции…> | status | uninstall | version\n');
	exit(2);
}
