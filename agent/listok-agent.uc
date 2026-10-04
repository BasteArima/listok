#!/usr/bin/ucode
// listok-agent — агент listok на роутере с forkop. Описание: docs/router-agent.md.
//
//   listok-agent run <секция>   цикл синхронизации одной секции (его запускает procd)
//   listok-agent sync <секция>  одна загрузка фида без forkop list_update (для установщика)
//   listok-agent status         что настроено и что лежит на роутере
//   listok-agent uninstall      убрать агент и его файлы из forkop
//
// Для проверки без изменений на роутере: LISTOK_UCI_DIR, LISTOK_STATE_DIR, LISTOK_TMP_DIR
// переносят конфиг и файлы в другой каталог, LISTOK_DRY_RUN=1 не вызывает forkop.
'use strict';

import * as fs from 'fs';
import { cursor } from 'uci';

const VERSION = '0.1.0';
const UCI_DIR = getenv('LISTOK_UCI_DIR') || '/etc/config';
const STATE_DIR = getenv('LISTOK_STATE_DIR') || '/etc/listok';
const TMP_DIR = getenv('LISTOK_TMP_DIR') || '/tmp/listok';
const DRY_RUN = getenv('LISTOK_DRY_RUN') == '1';
const DEBUG = getenv('LISTOK_DEBUG') == '1';

const FORKOP = '/usr/bin/forkop';
const FORKOP_PID = '/var/run/forkop_list_update.pid';
const RULESET_DIR = '/tmp/sing-box/rulesets';
const SENTINEL = 'listok-sentinel.invalid';
const WAIT_S = 55;            // long-poll: сервер держит запрос до стольких секунд
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
	let cfg = {
		server: c.get('listok', 'agent', 'server'),
		server_ip: c.get('listok', 'agent', 'server_ip') || '',
		proxy: c.get('listok', 'agent', 'proxy') || '',
		token: c.get('listok', 'agent', 'token'),
		feeds: {}
	};
	c.foreach('listok', 'feed', function(s) {
		if (s.section && s.token)
			cfg.feeds[s.section] = s.token;
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

// fetch — запрос фида. Возвращает {code, etag, body}; code 0 — сеть недоступна.
function fetch(cfg, section, etag, wait) {
	fs.mkdir(TMP_DIR);
	let body = TMP_DIR + '/' + section + '.body', hdr = TMP_DIR + '/' + section + '.hdr';
	fs.unlink(body);
	fs.unlink(hdr);
	let args = curl_base(cfg);
	push(args, '-o', body, '-D', hdr, '-w', '%{http_code}', '-m', '' + (wait + 25));
	if (etag)
		push(args, '-H', 'If-None-Match: ' + etag);
	push(args, cfg.server + '/f/' + cfg.feeds[section] + '.lst' + (wait > 0 ? '?wait=' + wait : ''));
	let code = int(trim(run(args))) || 0;
	let newEtag = '';
	for (let line in split(fs.readfile(hdr) || '', '\n')) {
		let m = match(trim(line), /^etag:[ \t]*(.+)$/i);
		if (m)
			newEtag = trim(m[1]);
	}
	return { code: code, etag: newEtag, body: body };
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

// store — положить фид на место атомарно. На флеш пишем только при реальном изменении.
function store(section, path, etag) {
	let text = fs.readfile(path);
	let target = list_path(section);
	fs.mkdir(STATE_DIR);
	if (fs.readfile(target) == text)
		return false;
	fs.writefile(target + '.new', text);
	fs.rename(target + '.new', target);
	fs.writefile(STATE_DIR + '/' + section + '.etag', etag + '\n');
	return true;
}

function forkop_busy() {
	let pid = trim(fs.readfile(FORKOP_PID) || '');
	return pid != '' && fs.access('/proc/' + pid);
}

// apply — forkop list_update. Код выхода ничего не говорит: при чужом обновлении forkop
// пишет «уже идёт» и выходит с 0. Поэтому успех — rule-set секции пересобран после нашего вызова.
function apply(section) {
	if (DRY_RUN)
		return { ok: true };
	let ruleset = RULESET_DIR + '/' + section + '-lists-ruleset.json';
	for (let attempt = 1; attempt <= 3; attempt++) {
		for (let i = 0; i < 100 && forkop_busy(); i++)
			sleep(3000);
		let t0 = time();
		system([ FORKOP, 'list_update' ], 600000);
		let st = fs.stat(ruleset);
		if (st && st.mtime >= t0 && index(fs.readfile(ruleset) || '', SENTINEL) >= 0)
			return { ok: true };
		sleep(10000);
	}
	return { ok: false, error: 'forkop list_update не пересобрал rule-set секции ' + section };
}

function post(cfg, path, payload) {
	fs.mkdir(TMP_DIR);
	let file = TMP_DIR + '/post.json';
	fs.writefile(file, sprintf('%J', payload));
	let args = curl_base(cfg);
	push(args, '-m', '15', '-o', TMP_DIR + '/post.out', '-w', '%{http_code}',
		'-H', 'Authorization: Bearer ' + cfg.token, '-H', 'Content-Type: application/json',
		'--data-binary', '@' + file, cfg.server + path);
	let code = int(trim(run(args))) || 0;
	let out = fs.readfile(TMP_DIR + '/post.out') || '';
	fs.unlink(file);
	return { code: code, body: out };
}

function installed_version(name) {
	for (let line in split(run([ 'opkg', 'list-installed' ]), '\n')) {
		let m = match(line, /^([^ ]+) - (.+)$/);
		if (m && (m[1] == name || index(m[1], name + '-') == 0))
			return m[2];
	}
	return '';
}

function hello(cfg) {
	let r = post(cfg, '/agent/v1/hello', {
		agent_version: VERSION,
		forkop_version: installed_version('forkop'),
		singbox_version: installed_version('sing-box'),
		sections: keys(cfg.feeds)
	});
	if (r.code != 200) {
		log('warning', 'hello: сервер ответил ' + r.code);
		return;
	}
	let resp = json(r.body);
	for (let f in resp?.feeds || [])
		if (!cfg.feeds[f.section])
			log('warning', 'на сервере есть фид секции ' + f.section + ', которой нет в /etc/config/listok — переустановите агент');
}

function report(cfg, section, etag, res) {
	let r = post(cfg, '/agent/v1/applied', { section: section, etag: etag, ok: res.ok, error: res.error || '' });
	if (r.code != 204)
		log('warning', 'отчёт о применении: сервер ответил ' + r.code);
}

function cmd_run(section) {
	let cfg = config();
	if (!cfg.feeds[section])
		die('нет фида секции ' + section + ' в /etc/config/listok');
	let etag = trim(fs.readfile(STATE_DIR + '/' + section + '.etag') || '');
	let backoff = 5, next_hello = 0;
	log('info', 'агент ' + VERSION + ' запущен для секции ' + section);
	while (true) {
		if (time() >= next_hello) {
			hello(cfg);
			next_hello = time() + 3600;
		}
		let t0 = time();
		let r = fetch(cfg, section, etag, WAIT_S);
		if (r.code == 304) {
			backoff = 5;
			if (time() - t0 < 5)
				sleep(30000); // сервер не держит long-poll: не долбим его
			continue;
		}
		if (r.code == 200) {
			let bad = validate(r.body);
			if (bad) {
				log('err', 'фид секции ' + section + ' отклонён: ' + bad);
				sleep(backoff * 1000);
				backoff = min(backoff * 2, BACKOFF_MAX_S);
				continue;
			}
			let changed = store(section, r.body, r.etag);
			etag = r.etag;
			let res = { ok: true };
			if (changed) {
				log('info', 'новая версия фида секции ' + section + ' ' + etag + ', применяю');
				res = apply(section);
				log(res.ok ? 'info' : 'err', res.ok ? 'применено ' + etag : res.error);
			}
			report(cfg, section, etag, res);
			backoff = 5;
			continue;
		}
		if (r.code == 404) {
			log('warning', 'фид секции ' + section + ' не найден (404): ссылку перевыпустили или фид удалён');
			sleep(BACKOFF_MAX_S * 1000);
			continue;
		}
		log('warning', 'нет связи с сервером (код ' + r.code + '), повтор через ' + backoff + ' с');
		sleep(backoff * 1000);
		backoff = min(backoff * 2, BACKOFF_MAX_S);
	}
}

function cmd_sync(section) {
	let cfg = config();
	if (!cfg.feeds[section])
		die('нет фида секции ' + section + ' в /etc/config/listok');
	let r = fetch(cfg, section, '', 0);
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
	print('агент ' + VERSION + ', сервер ' + cfg.server + (cfg.server_ip ? ' (через ' + cfg.server_ip + ')' : '') + '\n');
	for (let section in sort(keys(cfg.feeds))) {
		let text = fs.readfile(list_path(section));
		let n = text ? length(split(trim(text), '\n')) - 1 : 0;
		print(sprintf('  %-12s %s записей, версия %s\n', section, text ? '' + n : 'нет файла,',
			trim(fs.readfile(STATE_DIR + '/' + section + '.etag') || '—')));
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
if (cmd == 'run' && ARGV[1])
	cmd_run(ARGV[1]);
else if (cmd == 'sync' && ARGV[1])
	cmd_sync(ARGV[1]);
else if (cmd == 'status')
	cmd_status();
else if (cmd == 'uninstall')
	cmd_uninstall();
else if (cmd == 'version')
	print(VERSION + '\n');
else {
	warn('использование: listok-agent run|sync <секция> | status | uninstall | version\n');
	exit(2);
}
