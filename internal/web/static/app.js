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
  // Кнопки «Копировать»: data-copy = id поля с текстом.
  document.addEventListener("click", function (e) {
    const btn = e.target.closest("[data-copy]");
    if (!btn) return;
    const field = document.getElementById(btn.dataset.copy);
    if (!field) return;
    const done = function () {
      const old = btn.textContent;
      btn.textContent = "Скопировано";
      setTimeout(function () { btn.textContent = old; }, 1500);
    };
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(field.value).then(done, function () { field.select(); });
    } else {
      field.select();
      try { document.execCommand("copy"); done(); } catch (_) {}
    }
  });
  document.addEventListener("htmx:sendError", function () {
    toast("Нет связи с сервером");
  });

  // Массовый выбор записей на странице списка: режим с галочками и панель действий #bulk-bar.
  function rowBoxes() {
    return document.querySelectorAll("#rows .sel input[type=checkbox]");
  }
  function updateSelection() {
    const bar = document.getElementById("bulk-bar");
    if (!bar) return;
    const boxes = Array.from(rowBoxes());
    const n = boxes.filter(function (b) { return b.checked; }).length;
    bar.querySelector("[data-select-count]").textContent = n;
    bar.querySelectorAll("[data-bulk]").forEach(function (b) { b.disabled = n === 0; });
    const all = document.querySelector("[data-select-all]");
    if (all) {
      all.checked = n > 0 && n === boxes.length;
      all.indeterminate = n > 0 && n < boxes.length;
    }
  }
  function setSelecting(on) {
    const table = document.getElementById("entries-table");
    const bar = document.getElementById("bulk-bar");
    if (!table || !bar) return;
    table.classList.toggle("selecting", on);
    bar.hidden = !on;
    document.querySelectorAll("[data-select-mode][aria-pressed]").forEach(function (b) {
      b.setAttribute("aria-pressed", String(on));
    });
    if (!on) rowBoxes().forEach(function (b) { b.checked = false; });
    updateSelection();
  }
  document.addEventListener("click", function (e) {
    if (e.target.closest("[data-select-mode]")) {
      const table = document.getElementById("entries-table");
      setSelecting(!(table && table.classList.contains("selecting")));
      return;
    }
    // Подтверждение только для опасного действия; отмена гасит отправку формы.
    const danger = e.target.closest("[data-confirm-tpl]");
    if (danger) {
      const n = document.querySelectorAll("#rows .sel input:checked").length;
      if (!window.confirm(danger.dataset.confirmTpl.replace("{n}", n))) e.preventDefault();
    }
  });
  document.addEventListener("change", function (e) {
    if (e.target.matches("[data-select-all]")) {
      rowBoxes().forEach(function (b) { b.checked = e.target.checked; });
      updateSelection();
    } else if (e.target.closest("#rows .sel")) {
      updateSelection();
    }
  });
  // Таблицу перерисовали (поиск, действие, правка строки): пересчитать выбранное. Дёшево, поэтому на любой swap.
  document.addEventListener("htmx:afterSettle", updateSelection);
})();
