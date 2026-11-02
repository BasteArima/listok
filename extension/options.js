import { loadSettings, saveSettings, normalizeURL, parseConnection, requestOne } from "./client.js";

const $ = (id) => document.getElementById(id);

function addCheck(text, kind) {
  const el = document.createElement("div");
  el.className = "msg " + kind;
  el.textContent = text;
  $("checks").append(el);
}

function originPattern(url) {
  const u = new URL(url);
  return u.protocol + "//" + u.hostname + "/*";
}

function isPlainRemote(url) {
  const u = new URL(url);
  return u.protocol === "http:" && u.hostname !== "localhost" && u.hostname !== "127.0.0.1";
}

async function checkAddress(url, token) {
  try {
    const data = await requestOne(url, token, "/api/v1/lists");
    const editable = data.lists.filter((l) => l.can_edit).length;
    addCheck(`${url}: работает, пользователь ${data.user}, списков для добавления: ${editable}`, "ok");
    return data.lists;
  } catch (e) {
    addCheck(`${url}: ${e.status === 401 ? "токен не подходит или отозван" : e.message}`, "err");
    return null;
  }
}

function save(e) {
  e.preventDefault();
  $("checks").replaceChildren();
  let urls;
  try {
    urls = [$("url").value, $("fallback").value].filter((v) => v.trim()).map(normalizeURL);
  } catch (_) {
    addCheck("Адрес указан неверно. Пример: https://listok.example.com", "err");
    return;
  }
  const token = $("token").value.trim();
  // Разрешение спрашивается только сразу по нажатию, поэтому до первого await.
  const granted = chrome.permissions.request({ origins: urls.map(originPattern) }).catch(() => false);
  apply(urls, token, granted);
}

async function apply(urls, token, granted) {
  await saveSettings({ urls, token, active: urls[0] });
  $("url").value = urls[0];
  $("fallback").value = urls[1] || "";
  $("connection").value = "";
  if (!(await granted)) {
    addCheck("Браузер не дал доступ к адресу. По https это обычно не мешает, а локальный адрес может не заработать.", "warn");
  }
  for (const url of urls) {
    if (isPlainRemote(url)) addCheck(`${url}: без https токен идёт по сети открытым текстом.`, "warn");
  }
  let lists = null;
  for (const url of urls) {
    lists = (await checkAddress(url, token)) || lists;
  }
  if (lists) await saveSettings({ lists });
}

async function main() {
  const s = await loadSettings();
  $("url").value = s.urls[0] || "";
  $("fallback").value = s.urls[1] || "";
  $("token").value = s.token;

  $("connection").addEventListener("input", () => {
    const parsed = parseConnection($("connection").value);
    if (parsed) {
      $("url").value = parsed.url;
      $("token").value = parsed.token;
    }
  });
  $("form").addEventListener("submit", save);
}

main();
