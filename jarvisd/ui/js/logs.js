// Логи.
import { $, $$, api, copyText, esc, icon, toast } from "./core.js";
import { page } from "./main.js";

// ---------------------------------------------------------------- логи
export const LOGS = { src: "jarvis", q: "", auto: true, at: 0, html: "" };
export function viewLogs() {
  $("#view").innerHTML = `<header class="ph ph-row"><div><h1>Логи</h1><p class="sub">Что делали Джарвис и Алиса — последние 400 строк. Тексты разговоров пишутся только в журнал Джарвиса</p></div>
    <div class="actions"><div class="seg" id="lg-src"><button type="button" data-s="jarvis" class="${LOGS.src === "jarvis" ? "on" : ""}">Джарвис</button><button type="button" data-s="alice" class="${LOGS.src === "alice" ? "on" : ""}">Алиса</button><button type="button" data-s="media" class="${LOGS.src === "media" ? "on" : ""}">Звук и музыка</button></div></div></header>
  <section class="card logs-card">
    <div class="logs-bar"><label class="search">${icon("search")}<input type="search" id="lg-q" placeholder="Найти в логе" value="${esc(LOGS.q)}" autocomplete="off"></label>
      <label class="sw"><input type="checkbox" id="lg-auto"${LOGS.auto ? " checked" : ""}><span class="tr"></span>Обновлять сами</label>
      <button class="btn sm" id="lg-copy">${icon("link")}Скопировать</button></div>
    <div class="logbox" id="logbox"><p class="empty">Загружаю…</p></div>
  </section>`;
  LOGS.html = "";
  $$("#lg-src [data-s]").forEach((b) => b.addEventListener("click", () => { LOGS.src = b.dataset.s; $$("#lg-src button").forEach((x) => x.classList.toggle("on", x === b)); LOGS.html = ""; logsData(true); }));
  let qt = 0;
  $("#lg-q").addEventListener("input", (e) => { clearTimeout(qt); qt = setTimeout(() => { LOGS.q = e.target.value; LOGS.html = ""; logsData(true); }, 250); });
  $("#lg-auto").addEventListener("change", (e) => { LOGS.auto = e.target.checked; });
  $("#lg-copy").addEventListener("click", () => copyText($("#logbox").innerText).then(() => toast("Лог скопирован"), () => toast("Не получилось скопировать", true)));
  logsData(true);
}

export const logLine = (l) => {
  const m = /^(\d{4}\/\d\d\/\d\d |\[[\d:]+\] \[)?(\S*\s?\d\d:\d\d:\d\d(?:\.\d+)?\]?)?(.*)$/.exec(l) || [];
  const bad = /ошибк|error|fail|не получилось|panic|не удалось|не открылся/i.test(l), warn = /warn|предупрежд|повтор|ложное/i.test(l);
  return `<div class="ll${bad ? " bad" : warn ? " warn" : ""}"><span class="lt">${esc((m[1] || "") + (m[2] || ""))}</span>${esc(m[3] ?? l)}</div>`;
};

// логи на главной: карточка занимает остаток правой колонки; места меньше ~170 px — её не видно и лог не грузим
export const HL = { at: 0, html: "", on: false };
export function homeLogsSetup() {
  const c = $("#hlogs");
  if (!c) return;
  HL.html = ""; HL.on = false;
  new ResizeObserver(() => {
    const on = c.isConnected && getComputedStyle(c).display !== "none" && c.clientHeight >= 170;
    c.classList.toggle("tight", !on);
    if (on && !HL.on) { HL.on = true; homeLogs(); } else HL.on = on;
  }).observe(c);
}
export async function homeLogs() {
  HL.at = Date.now();
  const box = $("#hlogbox");
  if (!box || !HL.on) return;
  const n = Math.max(10, Math.ceil(box.clientHeight / 19) + 4);
  let d;
  try { d = await api("GET", `/api/logs?src=jarvis&n=${n}`); } catch { return; }
  const html = (d.lines || []).map(logLineShort).join("") || `<p class="empty">${esc(d.error || "Пока пусто")}</p>`;
  if (html === HL.html || !box.isConnected) return;
  HL.html = html;
  box.innerHTML = html;
  box.scrollTop = box.scrollHeight;
}

// короткая строка для главной: время — только минуты и секунды
export const logLineShort = (l) => {
  const m = /^\d{4}\/\d\d\/\d\d \d\d:(\d\d:\d\d)(?:\.\d+)? (.*)$/.exec(l);
  if (!m) return logLine(l);
  const bad = /ошибк|error|fail|не получилось|panic|не удалось|не открылся/i.test(m[2]), warn = /warn|предупрежд|повтор|ложное/i.test(m[2]);
  return `<div class="ll${bad ? " bad" : warn ? " warn" : ""}"><span class="lt">${m[1]}</span>${esc(m[2])}</div>`;
};

export async function logsData(force) {
  if (page !== "logs") return;
  LOGS.at = Date.now();
  let d;
  try { d = await api("GET", `/api/logs?src=${LOGS.src}&n=400&q=${encodeURIComponent(LOGS.q)}`); } catch { return; }
  const box = $("#logbox");
  if (!box) return;
  const html = (d.lines || []).map(logLine).join("") || `<p class="empty">${esc(d.error || (LOGS.q ? "Ничего не нашлось" : "Пока пусто"))}</p>`;
  if (html === LOGS.html && !force) return;
  const pinned = box.scrollHeight - box.scrollTop - box.clientHeight < 60 || force;
  LOGS.html = html;
  box.innerHTML = html;
  if (pinned) box.scrollTop = box.scrollHeight;
}
