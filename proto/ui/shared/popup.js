// Prototype behaviour for the static popup mockup: every control gives visible
// feedback, nothing connects anywhere. The theme can be forced from the "..."
// menu, so light/dark can be checked without touching system settings.
// Each click is reported to the host (log file) so a check can see that the
// page received it.
(function () {
  "use strict";
  function report(msg) {
    try {
      if (window.__TAURI_INTERNALS__) {
        window.__TAURI_INTERNALS__.invoke("ui_log", { msg: msg });
      } else {
        fetch("/__log?m=" + encodeURIComponent(msg), { cache: "no-store" });
      }
    } catch (e) { /* no host bridge */ }
  }
  window.__zatvorReport = report;
  var timer;
  function toast(text) {
    var t = document.getElementById("toast");
    t.textContent = text;
    t.classList.add("show");
    clearTimeout(timer);
    timer = setTimeout(function () { t.classList.remove("show"); }, 1800);
  }
  function setTheme(v) {
    if (v === "system") document.documentElement.removeAttribute("data-theme");
    else document.documentElement.setAttribute("data-theme", v);
    document.querySelectorAll("[data-theme-set]").forEach(function (b) {
      b.setAttribute("aria-checked", String(b.dataset.themeSet === v));
    });
  }
  var paused = false;
  var actions = {
    power: function (b) {
      var on = b.getAttribute("aria-pressed") !== "true";
      b.setAttribute("aria-pressed", String(on));
      b.setAttribute("aria-label", on ? "Туннель включён" : "Туннель выключен");
      document.getElementById("root").classList.toggle("off", !on);
      document.querySelectorAll(".ring").forEach(function (c) {
        c.style.stroke = on ? "var(--ok)" : "var(--muted)";
      });
      toast(on ? "Защита включена (макет)" : "Защита выключена (макет)");
    },
    pause: function (b) {
      paused = !paused;
      b.textContent = paused ? "Продолжить" : "Пауза";
      toast(paused ? "Пауза на 15 минут (макет)" : "Защита продолжена (макет)");
    },
    check: function () { toast("Проверка: всё в порядке (макет)"); },
    panel: function () { toast("Панель появится на этапе 1"); },
    menu: function () { document.getElementById("menu").classList.toggle("open"); }
  };
  document.addEventListener("click", function (e) {
    var b = e.target.closest("button");
    var menu = document.getElementById("menu");
    if (!b) { menu.classList.remove("open"); return; }
    if (b.dataset.themeSet) {
      setTheme(b.dataset.themeSet);
      menu.classList.remove("open");
      report("click theme=" + b.dataset.themeSet);
      return;
    }
    var a = b.dataset.action;
    if (a !== "menu") menu.classList.remove("open");
    if (actions[a]) actions[a](b);
    report("click " + a + (e.isTrusted ? " trusted" : " synthetic"));
  });
  // Inline style attributes carry most of the layout; a CSP that drops
  // 'unsafe-inline' leaves the page unstyled (serif font, no flex).
  var root = document.getElementById("root");
  var cs = root ? getComputedStyle(root) : null;
  var styled = !!cs && cs.display === "flex" && cs.fontFamily.indexOf("Onest") >= 0;
  report("page loaded, colour scheme " +
    (matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light") +
    ", styles=" + (styled ? "ok" : "missing"));
})();
