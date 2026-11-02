// Настройки расширения и запросы к listok. Адресов может быть два: запасной пробуется, когда основной не отвечает.

const TIMEOUT_MS = 4000;

export class ApiError extends Error {
  constructor(message, status) {
    super(message);
    this.status = status;
  }
}

export async function loadSettings() {
  const s = await chrome.storage.local.get(["urls", "token", "active", "lastList", "lists"]);
  return { urls: s.urls || [], token: s.token || "", active: s.active || "", lastList: s.lastList || "", lists: s.lists || [] };
}

export function saveSettings(values) {
  return chrome.storage.local.set(values);
}

// normalizeURL — адрес сервиса без хвостового слеша. Неверный адрес — исключение.
export function normalizeURL(text) {
  const raw = text.trim();
  const u = new URL(raw.includes("://") ? raw : "https://" + raw);
  if (u.protocol !== "http:" && u.protocol !== "https:") throw new Error("нужен адрес http или https");
  return u.origin + u.pathname.replace(/\/+$/, "");
}

// parseConnection разбирает строку подключения с вкладки «Подключения»: адрес#токен.
export function parseConnection(text) {
  const raw = text.trim();
  const i = raw.lastIndexOf("#");
  if (i <= 0 || i === raw.length - 1) return null;
  try {
    return { url: normalizeURL(raw.slice(0, i)), token: raw.slice(i + 1) };
  } catch (_) {
    return null;
  }
}

// requestOne — запрос к одному адресу. Исключение ApiError со status 0 — адрес не отвечает или это не listok,
// другой status — ответ самого listok (токен не подошёл, нет прав): другой адрес тут не поможет.
export async function requestOne(url, token, path, body) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), TIMEOUT_MS);
  let res;
  let data;
  try {
    res = await fetch(url + path, {
      method: body ? "POST" : "GET",
      headers: { Authorization: "Bearer " + token, ...(body ? { "Content-Type": "application/json" } : {}) },
      body: body ? JSON.stringify(body) : undefined,
      credentials: "omit",
      cache: "no-store",
      signal: controller.signal,
    });
    data = await res.json();
  } catch (_) {
    throw new ApiError("нет связи", 0);
  } finally {
    clearTimeout(timer);
  }
  if (res.ok) return data;
  if (res.status >= 500 || !data.message) throw new ApiError("сервер не отвечает как listok", 0);
  throw new ApiError(data.message, res.status);
}

// request пробует адреса по очереди, начиная с сработавшего в прошлый раз, и запоминает рабочий.
// Повторить добавление на другом адресе безопасно: запись просто окажется «уже есть».
export async function request(settings, path, body) {
  const order = [...settings.urls].sort((a, b) => (b === settings.active) - (a === settings.active));
  for (const url of order) {
    try {
      const data = await requestOne(url, settings.token, path, body);
      if (url !== settings.active) {
        settings.active = url;
        await saveSettings({ active: url });
      }
      return data;
    } catch (e) {
      if (e.status !== 0) throw e;
    }
  }
  throw new ApiError("Нет связи с listok: " + settings.urls.join(", "), 0);
}

export function encodePath(slug) {
  return "/api/v1/lists/" + encodeURIComponent(slug) + "/entries";
}
