import { loadSettings, saveSettings, request, inspectHosts, encodePath } from "./client.js";

const $ = (id) => document.getElementById(id);
const CLOSE_AFTER_MS = 1500;
const MAX_OTHER = 40;

let settings;
let tabId;
let hasLists = false;
let mainTouched = false; // галочку основного сайта поменяли руками: больше не трогаем её сами

function showResult(text, kind) {
  const el = $("result");
  el.textContent = text;
  el.className = "msg " + kind;
  el.hidden = false;
}

function plural(n, one, few, many) {
  const m = n % 100;
  if (m >= 11 && m <= 14) return many;
  if (m % 10 === 1) return one;
  if (m % 10 >= 2 && m % 10 <= 4) return few;
  return many;
}

// currentTab — активная вкладка и её хост. На служебных страницах (настройки, новая вкладка) хост пуст.
async function currentTab() {
  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  let host = "";
  for (const candidate of [tab && tab.url, tab && tab.pendingUrl]) {
    try {
      const u = new URL(candidate);
      if (u.protocol === "http:" || u.protocol === "https:") {
        host = u.hostname.replace(/^\[|\]$/g, "");
        break;
      }
    } catch (_) {}
  }
  return { id: tab && tab.id, host };
}

function fillLists(all, wanted) {
  const select = $("list");
  const editable = all.filter((l) => l.can_edit);
  const current = select.value || wanted;
  select.replaceChildren(...editable.map((l) => new Option(l.title, l.slug)));
  if (editable.some((l) => l.slug === current)) select.value = current;
  hasLists = editable.length > 0;
  updateButton();
}

function checkedRows() {
  return [...document.querySelectorAll(".rows input:checked")];
}

// Кнопка говорит, что будет сделано. Кликнуть впустую нельзя.
function updateButton() {
  const main = $("main-on").checked && $("input").value.trim() !== "";
  const n = checkedRows().length + (main ? 1 : 0);
  const add = $("add");
  add.disabled = n === 0 || !hasLists;
  if (n === 0) add.textContent = $("input").value.trim() ? "Отметьте, что добавить" : "Укажите сайт";
  else if (n === 1) add.textContent = "Добавить";
  else add.textContent = `Добавить ${n} ${plural(n, "домен", "домена", "доменов")}`;
}

// Что получится из ввода и не покрыто ли это уже. Устаревший ответ отбрасывается.
let previewSeq = 0;
async function updatePreview() {
  const seq = ++previewSeq;
  const preview = $("preview");
  const input = $("input").value.trim();
  if (!input) {
    preview.replaceChildren();
    return;
  }
  const q = new URLSearchParams({ q: input });
  if ($("exact").checked) q.set("exact", "1");
  let data;
  try {
    data = await request(settings, "/api/v1/check?" + q);
  } catch (_) {
    return; // Нет связи — об этом скажет сама кнопка, подсказка не важна.
  }
  if (seq !== previewSeq) return;
  const lines = [];
  let covered = false;
  for (const it of data.items) {
    if (it.error) {
      lines.push(line(it.error, "err"));
      continue;
    }
    if (it.value !== input) lines.push(line("Добавится: " + it.value));
    if (it.covered_by) {
      covered = true;
      const titles = [...new Set(it.covered_by.map((c) => "«" + c.list + "»"))].join(", ");
      lines.push(line("Уже есть в " + titles, "warn"));
    }
    for (const w of it.warnings || []) lines.push(line(w, "warn"));
  }
  preview.replaceChildren(...lines);
  if (!mainTouched) {
    $("main-on").checked = !covered;
    updateButton();
  }
}

function line(text, cls) {
  const el = document.createElement("div");
  el.textContent = text;
  if (cls) el.className = cls;
  return el;
}

function row(group, checked, note) {
  const label = document.createElement("label");
  label.className = "row" + (group.coveredBy.length ? " done" : "");
  const box = document.createElement("input");
  box.type = "checkbox";
  box.checked = checked;
  box.value = group.value;
  box.dataset.hosts = group.hosts.join(" ");
  const name = document.createElement("span");
  name.className = "name";
  name.textContent = group.value;
  label.append(box, name);
  if (note) {
    const n = document.createElement("span");
    n.className = "note";
    n.textContent = note;
    label.append(n);
  }
  return label;
}

function failedNote(g) {
  if (g.coveredBy.length) return "уже в «" + g.coveredBy[0] + "»";
  if (g.ok > 0) return "часть загрузилась";
  return `${g.failed} ${plural(g.failed, "запрос", "запроса", "запросов")}`;
}

// Домены страницы: сверху не загрузившиеся, в свёрнутом блоке остальные. Отмечены только те, что точно не загрузились
// и ещё не добавлены: тогда значок и Enter делают нужное без лишних щелчков.
async function loadResources() {
  if (tabId === undefined) return;
  const info = await chrome.runtime.sendMessage({ type: "tabInfo", tabId }).catch(() => null);
  if (!info || info.hosts.length === 0) return;
  const stats = new Map(info.hosts.map((h) => [h.host, h]));
  const groups = await inspectHosts(settings, info.hosts.map((h) => h.host));
  const failed = [];
  const other = [];
  for (const g of groups.values()) {
    if (g.hosts.includes(info.main)) continue; // Это основной сайт, он в поле сверху.
    g.failed = g.hosts.reduce((sum, h) => sum + stats.get(h).failed, 0);
    g.ok = g.hosts.reduce((sum, h) => sum + stats.get(h).ok, 0);
    if (g.failed > 0) failed.push(g);
    else if (g.coveredBy.length === 0) other.push(g);
  }
  failed.sort((a, b) => b.failed - a.failed);
  other.sort((a, b) => b.ok - a.ok);

  $("failed-rows").replaceChildren(...failed.map((g) => row(g, g.coveredBy.length === 0 && g.ok === 0, failedNote(g))));
  $("failed-count").textContent = failed.length;
  $("failed").hidden = failed.length === 0;
  const shown = other.slice(0, MAX_OTHER);
  $("other-rows").replaceChildren(...shown.map((g) => row(g, false, "")));
  $("other-count").textContent = other.length;
  $("other").hidden = other.length === 0;
  updateButton();
}

function debounce(fn, ms) {
  let timer;
  return (...args) => {
    clearTimeout(timer);
    timer = setTimeout(() => fn(...args), ms);
  };
}

async function add() {
  const slug = $("list").value;
  const main = $("main-on").checked ? $("input").value.trim() : "";
  const rows = checkedRows();
  const input = [main, ...rows.map((b) => b.value)].filter(Boolean).join("\n");
  if (!input || !slug) return;
  $("add").disabled = true;
  $("result").hidden = true;
  try {
    const data = await request(settings, encodePath(slug), {
      input,
      comment: $("comment").value,
      exact_host: $("exact").checked,
    });
    await saveSettings({ lastList: slug });
    const statuses = data.items.map((i) => i.status);
    const failed = statuses.includes("error");
    if (tabId !== undefined && !failed) {
      const hosts = rows.flatMap((b) => b.dataset.hosts.split(" "));
      await chrome.runtime.sendMessage({ type: "forget", tabId, hosts }).catch(() => {});
    }
    if (statuses.includes("added") && !failed) {
      showResult(data.message, "ok");
      setTimeout(() => window.close(), CLOSE_AFTER_MS);
      return;
    }
    showResult(data.message, failed ? "err" : "warn");
  } catch (e) {
    showResult(e.message, "err");
  }
  updateButton();
}

async function main() {
  const openSettings = (e) => {
    e.preventDefault();
    chrome.runtime.openOptionsPage();
  };
  $("settings").addEventListener("click", openSettings);
  $("open-settings").addEventListener("click", openSettings);

  settings = await loadSettings();
  if (!settings.token || settings.urls.length === 0) {
    $("setup").hidden = false;
    return;
  }

  const tab = await currentTab();
  tabId = tab.id;
  $("input").value = tab.host;
  $("form").hidden = false;
  // Списки из прошлого раза сразу, свежие — как придут: окошко не ждёт сеть.
  fillLists(settings.lists, settings.lastList);
  (tab.host ? $("add") : $("input")).focus();

  const refresh = debounce(updatePreview, 300);
  $("input").addEventListener("input", () => {
    updateButton();
    refresh();
  });
  $("main-on").addEventListener("change", () => {
    mainTouched = true;
    updateButton();
  });
  $("exact").addEventListener("change", updatePreview);
  $("form").addEventListener("change", updateButton);
  $("form").addEventListener("submit", (e) => {
    e.preventDefault();
    add();
  });

  const keepFocus = async (task) => {
    await task;
    if (document.activeElement === document.body && !$("add").disabled) $("add").focus();
  };
  keepFocus(updatePreview());
  keepFocus(loadResources().catch(() => {}));
  try {
    const data = await request(settings, "/api/v1/lists");
    fillLists(data.lists, settings.lastList);
    await saveSettings({ lists: data.lists });
    if (!hasLists) showResult("Нет списка, в который можно добавлять", "warn");
  } catch (e) {
    showResult(e.status === 401 ? "Токен не подходит: создайте новый и вставьте его в настройках" : e.message, "err");
  }
}

main();
