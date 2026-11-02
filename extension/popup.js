import { loadSettings, saveSettings, request, encodePath } from "./client.js";

const $ = (id) => document.getElementById(id);
const CLOSE_AFTER_MS = 1500;

function showResult(text, kind) {
  const el = $("result");
  el.textContent = text;
  el.className = "msg " + kind;
  el.hidden = false;
}

// currentHost — хост активной вкладки. На служебных страницах (настройки, новая вкладка) пусто.
async function currentHost() {
  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  for (const candidate of [tab && tab.url, tab && tab.pendingUrl]) {
    try {
      const u = new URL(candidate);
      if (u.protocol === "http:" || u.protocol === "https:") return u.hostname.replace(/^\[|\]$/g, "");
    } catch (_) {}
  }
  return "";
}

function fillLists(all, wanted) {
  const select = $("list");
  const editable = all.filter((l) => l.can_edit);
  const current = select.value || wanted;
  select.replaceChildren(...editable.map((l) => new Option(l.title, l.slug)));
  if (editable.some((l) => l.slug === current)) select.value = current;
  $("add").disabled = editable.length === 0;
  return editable;
}

// Что получится из ввода и не покрыто ли это уже. Устаревший ответ отбрасывается.
let previewSeq = 0;
async function updatePreview(settings) {
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
  for (const it of data.items) {
    if (it.error) {
      lines.push(span(it.error, "err"));
      continue;
    }
    if (it.value !== input) lines.push(span("Добавится: " + it.value));
    if (it.covered_by) {
      const titles = [...new Set(it.covered_by.map((c) => "«" + c.list + "»"))].join(", ");
      lines.push(span("Уже есть в " + titles, "warn"));
    }
    for (const w of it.warnings || []) lines.push(span(w, "warn"));
  }
  preview.replaceChildren(...lines);
}

function span(text, cls) {
  const el = document.createElement("div");
  el.textContent = text;
  if (cls) el.className = cls;
  return el;
}

function debounce(fn, ms) {
  let timer;
  return (...args) => {
    clearTimeout(timer);
    timer = setTimeout(() => fn(...args), ms);
  };
}

async function add(settings) {
  const slug = $("list").value;
  const input = $("input").value.trim();
  if (!input) {
    showResult("Укажите сайт", "err");
    $("input").focus();
    return;
  }
  if (!slug) {
    showResult("Нет списка, в который можно добавлять", "err");
    return;
  }
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
    if (statuses.includes("added")) {
      showResult(data.message, "ok");
      setTimeout(() => window.close(), CLOSE_AFTER_MS);
      return;
    }
    showResult(data.message, statuses.includes("error") ? "err" : "warn");
  } catch (e) {
    showResult(e.message, "err");
  }
  $("add").disabled = false;
}

async function main() {
  const openSettings = (e) => {
    e.preventDefault();
    chrome.runtime.openOptionsPage();
  };
  $("settings").addEventListener("click", openSettings);
  $("open-settings").addEventListener("click", openSettings);

  const settings = await loadSettings();
  if (!settings.token || settings.urls.length === 0) {
    $("setup").hidden = false;
    return;
  }

  const host = await currentHost();
  $("input").value = host;
  $("form").hidden = false;
  // Списки из прошлого раза сразу, свежие — как придут: окошко не ждёт сеть.
  fillLists(settings.lists, settings.lastList);
  (host ? $("add") : $("input")).focus();

  const refresh = debounce(() => updatePreview(settings), 300);
  $("input").addEventListener("input", refresh);
  $("exact").addEventListener("change", () => updatePreview(settings));
  $("form").addEventListener("submit", (e) => {
    e.preventDefault();
    add(settings);
  });

  updatePreview(settings);
  try {
    const data = await request(settings, "/api/v1/lists");
    fillLists(data.lists, settings.lastList);
    await saveSettings({ lists: data.lists });
    if (!data.lists.some((l) => l.can_edit)) showResult("Нет списка, в который можно добавлять", "warn");
  } catch (e) {
    showResult(e.status === 401 ? "Токен не подходит: создайте новый и вставьте его в настройках" : e.message, "err");
  }
}

main();
