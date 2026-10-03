// Показ ошибок htmx-запросов. Отдельный файл, а не inline: CSP разрешает только свои скрипты.
(function () {
  "use strict";
  let timer;
  function toast(text) {
    const el = document.getElementById("toast");
    if (!el) return;
    el.textContent = text;
    el.hidden = false;
    clearTimeout(timer);
    timer = setTimeout(function () { el.hidden = true; }, 5000);
  }
  document.addEventListener("htmx:responseError", function (e) {
    const xhr = e.detail.xhr;
    const messages = { 401: "Сессия истекла, войдите заново", 403: "Нет прав на это действие", 404: "Не найдено" };
    toast(messages[xhr.status] || "Ошибка сервера (" + xhr.status + ")");
    if (xhr.status === 401) window.location.href = "/login?next=" + encodeURIComponent(location.pathname);
  });
  document.addEventListener("htmx:sendError", function () {
    toast("Нет связи с сервером");
  });
})();
