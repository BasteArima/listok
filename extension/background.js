// Какие домены не загрузились на странице. Запросы только наблюдаем, блокировать их расширение не может.
// Состояние вкладок лежит в storage.session: браузер останавливает этот скрипт, когда он простаивает.

import { loadSettings, inspectHosts } from "./client.js";

const HANG_MS = 15000; // без ответа дольше — считаем, что не загрузилось (SwitchyOmega ждёт 5 с и ошибается на медленном VPN)
const BADGE_DELAY_MS = 1500;
const SAVE_DELAY_MS = 1000;
const MAX_HOSTS = 300;
const MAX_PENDING = 500;

// Только ошибки сети. Нет ERR_ABORTED (страница отменила запрос сама), ERR_BLOCKED_BY_* (блокировщик рекламы),
// ERR_INTERNET_DISCONNECTED и ERR_NETWORK_CHANGED (дело не в этом домене).
const NETWORK_ERROR = /^net::ERR_(CONNECTION_|TIMED_OUT|NAME_NOT_RESOLVED|NAME_RESOLUTION_FAILED|ADDRESS_UNREACHABLE|SSL_|CERT_|QUIC_|HTTP2_|EMPTY_RESPONSE|TUNNEL_|PROXY_|SOCKS_)/;
const LOCAL_HOST = /^(localhost|\[.*\]|\d+\.\d+\.\d+\.\d+)$|\.(localhost|local|lan|home|internal|arpa|invalid)$/;
const IGNORED_TYPES = new Set(["ping", "csp_report"]);

let tabs = {}; // id вкладки → { main, hosts: { хост: { failed, ok } }, pending: { id запроса: { host, start } } }
const ready = chrome.storage.session.get("tabs").then((s) => {
  tabs = s.tabs || {};
});

let saveTimer;
function save() {
  clearTimeout(saveTimer);
  saveTimer = setTimeout(() => chrome.storage.session.set({ tabs }), SAVE_DELAY_MS);
}

function hostOf(url) {
  try {
    return new URL(url).hostname;
  } catch (_) {
    return "";
  }
}

function hostState(tab, host) {
  if (!tab.hosts[host] && Object.keys(tab.hosts).length < MAX_HOSTS) tab.hosts[host] = { failed: 0, ok: 0 };
  return tab.hosts[host];
}

// snapshot — хосты вкладки. Запрос без ответа дольше HANG_MS считается незагрузившимся, пока ответ не пришёл.
function snapshot(tabId, now = Date.now()) {
  const tab = tabs[tabId];
  if (!tab) return { main: "", hosts: [] };
  const hung = {};
  for (const p of Object.values(tab.pending)) {
    if (now - p.start >= HANG_MS) hung[p.host] = (hung[p.host] || 0) + 1;
  }
  const hosts = Object.entries(tab.hosts).map(([host, s]) => ({ host, failed: s.failed + (hung[host] || 0), ok: s.ok }));
  return { main: tab.main, hosts };
}

// Счётчик на значке: сколько доменов не загрузилось и ещё не добавлено ни в один список.
const badgeTimers = {};
const badgeSeen = {};
function scheduleBadge(tabId, delay = BADGE_DELAY_MS) {
  const due = Date.now() + delay;
  const timer = badgeTimers[tabId];
  if (timer && timer.due <= due) return;
  if (timer) clearTimeout(timer.id);
  const id = setTimeout(() => {
    delete badgeTimers[tabId];
    updateBadge(tabId);
    scheduleHangCheck(tabId);
  }, delay);
  badgeTimers[tabId] = { due, id };
}

// Запрос, который ещё может зависнуть, — проверить снова, когда истечёт его срок.
function scheduleHangCheck(tabId) {
  const tab = tabs[tabId];
  if (!tab) return;
  const now = Date.now();
  const waits = Object.values(tab.pending).map((p) => p.start + HANG_MS - now).filter((ms) => ms > 0);
  if (waits.length) scheduleBadge(tabId, Math.min(...waits) + 500);
}

async function updateBadge(tabId) {
  const { main, hosts } = snapshot(tabId);
  const failed = hosts.filter((h) => h.failed > 0 && h.host !== main).map((h) => h.host).sort();
  const seen = failed.join(" ");
  if (badgeSeen[tabId] === seen) return;
  badgeSeen[tabId] = seen;
  let count = 0;
  if (failed.length) {
    const settings = await loadSettings();
    if (!settings.token || settings.urls.length === 0) return;
    try {
      const groups = await inspectHosts(settings, main ? [main, ...failed] : failed);
      for (const g of groups.values()) {
        if (!g.hosts.includes(main) && g.coveredBy.length === 0) count++;
      }
    } catch (_) {
      count = failed.length; // Сервис не отвечает: покрытие неизвестно, показываем всё.
    }
  }
  await chrome.action.setBadgeBackgroundColor({ tabId, color: "#b7791f" }).catch(() => {});
  await chrome.action.setBadgeText({ tabId, text: count ? String(count) : "" }).catch(() => {});
}

function onRequest(d) {
  if (d.tabId < 0 || IGNORED_TYPES.has(d.type)) return;
  const host = hostOf(d.url);
  if (d.type === "main_frame") {
    tabs[d.tabId] = { main: host, hosts: {}, pending: {} };
    delete badgeSeen[d.tabId];
  }
  const tab = tabs[d.tabId];
  if (!tab || !host || LOCAL_HOST.test(host) || !hostState(tab, host)) return;
  const ids = Object.keys(tab.pending);
  if (ids.length >= MAX_PENDING) delete tab.pending[ids[0]];
  tab.pending[d.requestId] = { host, start: d.timeStamp };
  save();
  scheduleBadge(d.tabId, HANG_MS + 500);
}

function onAnswered(d) {
  const tab = tabs[d.tabId];
  const p = tab && tab.pending[d.requestId];
  if (!p) return;
  delete tab.pending[d.requestId];
  if (tab.hosts[p.host]) tab.hosts[p.host].ok++;
  save();
  scheduleBadge(d.tabId);
}

function onError(d) {
  const tab = tabs[d.tabId];
  if (!tab) return;
  const p = tab.pending[d.requestId];
  delete tab.pending[d.requestId];
  const host = hostOf(d.url);
  const givenUp = d.error === "net::ERR_ABORTED" && p && d.timeStamp - p.start >= HANG_MS;
  if ((NETWORK_ERROR.test(d.error) || givenUp) && tab.hosts[host]) tab.hosts[host].failed++;
  save();
  scheduleBadge(d.tabId);
}

const filter = { urls: ["http://*/*", "https://*/*"] };
const whenReady = (fn) => (d) => {
  ready.then(() => fn(d));
};
chrome.webRequest.onBeforeRequest.addListener(whenReady(onRequest), filter);
chrome.webRequest.onHeadersReceived.addListener(whenReady(onAnswered), filter);
chrome.webRequest.onCompleted.addListener(whenReady(onAnswered), filter);
chrome.webRequest.onErrorOccurred.addListener(whenReady(onError), filter);

chrome.tabs.onRemoved.addListener((tabId) => {
  ready.then(() => {
    delete tabs[tabId];
    delete badgeSeen[tabId];
    save();
  });
});

chrome.runtime.onMessage.addListener((msg, _sender, reply) => {
  ready.then(() => {
    if (msg.type === "tabInfo") {
      reply(snapshot(msg.tabId));
      return;
    }
    if (msg.type === "forget") {
      // Домены добавлены: на этой странице они больше не считаются проблемой.
      const tab = tabs[msg.tabId];
      if (tab) {
        for (const host of msg.hosts) {
          if (tab.hosts[host]) tab.hosts[host].failed = 0;
        }
        for (const [id, p] of Object.entries(tab.pending)) {
          if (msg.hosts.includes(p.host)) delete tab.pending[id];
        }
        save();
        updateBadge(msg.tabId);
      }
      reply(true);
    }
  });
  return true;
});
