// Плитки на главной и графики.
import { $, $$, Motion, api, tip } from "./core.js";
import { S, page } from "./main.js";

// ---------------------------------------------------------------- плитки на главной (как в yapanel) и графики
export const fmtN = (v, dec = 0) => (v == null || isNaN(v) ? "—" : Number(v).toLocaleString("ru-RU", { minimumFractionDigits: dec, maximumFractionDigits: dec }).replace("-", "−"));
export const KPI = [
  { id: "temp", label: "Температура" },
  { id: "mem", label: "Память", tip: "Сколько оперативной памяти колонки занято. Джарвис занимает около 25 МБ." },
  { id: "cpu", label: "Процессор" },
  { id: "wifi", label: "Wi-Fi", tip: "Уровень сигнала: от −30 до −60 дБм — отлично, до −70 — нормально, ниже — связь может рваться, и Джарвис будет отвечать с задержкой." },
];
export const kpiHtml = (k) => `<div class="kpi" id="k-${k.id}"><span class="label">${k.label}${k.tip ? " " + tip(k.tip) : ""}</span><div class="value"><b>—</b><span></span></div><div class="kbar"><i></i></div><span class="meta"></span>
  <svg class="spark" viewBox="0 0 200 46" aria-hidden="true"><path class="a"/><path class="l"/></svg></div>`;
export let ST = { pts: [], at: 0 };

export async function statsData() {
  ST.at = Date.now();
  try { ST.pts = (await api("GET", "/api/stats?range=900")).points || []; } catch { return; }
  drawKpis();
}

// число «набегает» к новому значению (при первом показе — от нуля)
export function countTo(el, v, dec = 0) {
  if (v == null) { el.textContent = "—"; delete el.dataset.v; return; }
  const first = el.dataset.v == null, from = first ? 0 : +el.dataset.v;
  if (from === v) { if (first) { el.dataset.v = v; el.textContent = fmtN(v, dec); } return; }
  el.dataset.v = v;
  if (Motion.reduce) { el.textContent = fmtN(v, dec); return; }
  const t0 = performance.now(), d = 800;
  const step = (t) => {
    if (+el.dataset.v !== v) return;
    const k = Math.min(1, (t - t0) / d), e = 1 - Math.pow(1 - k, 3);
    el.textContent = fmtN(from + (v - from) * e, dec);
    if (k < 1) requestAnimationFrame(step);
  };
  requestAnimationFrame(step);
}

export function sparkPaths(vals, w = 100, h = 40) {
  const v = vals.filter((x) => x != null && !isNaN(x));
  if (v.length < 2) return null;
  let lo = Math.min(...v), hi = Math.max(...v);
  if (hi - lo < 1e-6) { hi += 1; lo -= 1; }
  const top = h * 0.3;
  const pts = v.map((y, i) => [(i / (v.length - 1)) * w, top + (1 - (y - lo) / (hi - lo)) * (h - top - 2)]);
  const l = pts.map((p, i) => (i ? "L" : "M") + p[0].toFixed(2) + " " + p[1].toFixed(2)).join("");
  return { l, a: l + `L${w} ${h}L0 ${h}Z` };
}

export function setKpi(id, val, unit, frac, meta, series, dec = 0, level = "") {
  const box = $("#k-" + id);
  if (!box) return;
  countTo($(".value b", box), val, dec);
  $(".value span", box).textContent = val == null ? "" : unit;
  const bar = $(".kbar", box);
  bar.className = "kbar" + (level ? " " + level : "");
  $(".kbar i", box).style.setProperty("width", `${Math.round(Motion.clamp(frac || 0, 0, 1) * 100)}%`);
  $(".meta", box).textContent = meta || "";
  const svg = $(".spark", box), w = Math.round(svg.clientWidth) || 200, h = 46, p = sparkPaths(series, w, h);
  if (!p) return;
  svg.setAttribute("viewBox", `0 0 ${w} ${h}`);
  $("path.l", svg).setAttribute("d", p.l);
  $("path.a", svg).setAttribute("d", p.a);
  if (!svg.dataset.drawn) { svg.dataset.drawn = "1"; drawIn($("path.l", svg), $("path.a", svg), 1300); }
}

export function drawKpis() {
  if (!$("#kpis")) return;
  const sys = S.sys || {}, last = ST.pts[ST.pts.length - 1] || {};
  const used = (sys.mem_total_mb || 0) - (sys.mem_avail_mb || 0);
  setKpi("temp", sys.temp_c ? Math.round(sys.temp_c) : null, "°C", (sys.temp_c || 0) / 85, "процессор", ST.pts.map((p) => p.temp || null), 0, sys.temp_c > 75 ? "bad" : sys.temp_c > 65 ? "warn" : "");
  setKpi("mem", sys.mem_total_mb ? used : null, sys.mem_total_mb ? `из ${sys.mem_total_mb} МБ` : "", used / (sys.mem_total_mb || 1), sys.mem_total_mb ? `свободно ${sys.mem_avail_mb} МБ` : "", ST.pts.map((p) => p.mem));
  const cpu = ST.pts.length > 1 ? Math.round(last.cpu) : null;
  const me = ST.pts.length > 1 ? Math.round(last.me || 0) : null;
  setKpi("cpu", cpu, "%", (cpu || 0) / 100, me != null ? `из них Джарвис — ${me}%` : "все ядра колонки", ST.pts.slice(1).map((p) => p.cpu), 0, cpu > 85 ? "warn" : "");
  const w = sys.wifi_dbm || null, q = w ? Motion.clamp((w + 90) / 60, 0, 1) : 0;
  setKpi("wifi", w, "дБм", q, !w ? "" : w >= -60 ? "сигнал отличный" : w >= -70 ? "сигнал нормальный" : "сигнал слабый — ответы могут задерживаться", ST.pts.map((p) => p.wifi || null), 0, w && w < -70 ? "warn" : "");
}

export const CHARTS = [
  { k: "cpu", t: "Процессор", u: "%", sub: "загрузка всех ядер", fix: [0, 100] },
  { k: "temp", t: "Температура", u: "°C", sub: "процессор", dec: 1 },
  { k: "mem", t: "Память занята", u: "МБ", sub: () => (S.sys.mem_total_mb ? `из ${S.sys.mem_total_mb} МБ` : "") },
  { k: "wifi", t: "Сигнал Wi-Fi", u: "дБм", sub: "ближе к нулю — лучше" },
  { k: "disk", t: "Свободно на /data", u: "МБ", sub: "меньше 5 МБ — обновления могут не встать" },
  { k: "me", t: "Сам Джарвис", u: "%", sub: "сколько процессора колонки занимает Джарвис", fix: [0, 100] },
];
export const CH = { range: 3600, pts: [], at: 0 };

export function viewCharts() {
  $("#view").innerHTML = `<header class="ph ph-row"><div><h1>Графики</h1><p class="sub">Раз в 15 секунд за последние сутки. История живёт в памяти колонки и начинается заново после перезапуска Джарвиса</p></div>
    <div class="seg" id="ch-range">${[[900, "15 мин"], [3600, "1 ч"], [21600, "6 ч"], [86400, "24 ч"]].map(([s, n]) => `<button type="button" data-r="${s}" class="${s === CH.range ? "on" : ""}">${n}</button>`).join("")}</div></header>
  <div class="charts stagger">${CHARTS.map((c) => `<section class="card"><div class="chart-h"><div><h2>${c.t}</h2><small id="cs-${c.k}"></small></div><div class="value"><b id="cv-${c.k}">—</b><span>${c.u}</span></div></div><div class="chart" id="ch-${c.k}"></div></section>`).join("")}</div>`;
  $$("#ch-range [data-r]").forEach((b) => b.addEventListener("click", () => {
    CH.range = +b.dataset.r;
    $$("#ch-range button").forEach((x) => x.classList.toggle("on", x === b));
    chartsData(true);
  }));
  chartsData(true);
}

export async function chartsData(fresh) {
  if (page !== "charts") return;
  CH.at = Date.now();
  try { CH.pts = (await api("GET", "/api/stats?range=" + CH.range)).points || []; } catch { return; }
  for (const c of CHARTS) drawChart(c, fresh);
}

export function drawChart(c, fresh) {
  const box = $("#ch-" + c.k);
  if (!box) return;
  const pts = CH.pts.filter((p, i) => p[c.k] != null && !((c.k === "wifi" || c.k === "temp") && !p[c.k]) && !((c.k === "cpu" || c.k === "me") && i === 0));
  const last = pts[pts.length - 1];
  const val = $("#cv-" + c.k);
  if (last) countTo(val, +last[c.k].toFixed(c.dec || 0), c.dec || 0); else val.textContent = "—";
  $("#cs-" + c.k).textContent = typeof c.sub === "function" ? c.sub() : c.sub;
  if (pts.length < 2) { box.innerHTML = `<p class="empty">Данные копятся — первые точки появятся через минуту</p>`; return; }
  const W = Math.max(240, box.clientWidth), H = 150, L = 40, B = 20, T = 6, R = 6;
  const vs = pts.map((p) => p[c.k]);
  let lo = Math.min(...vs), hi = Math.max(...vs);
  if (c.fix) [lo, hi] = c.fix;
  else { const pad = (hi - lo) * 0.2 || Math.max(1, Math.abs(hi) * 0.05); lo -= pad; hi += pad; }
  const t0 = pts[0].t, t1 = Math.max(pts[pts.length - 1].t, t0 + 1);
  const X = (t) => L + ((t - t0) / (t1 - t0)) * (W - L - R), Y = (v) => T + (1 - (v - lo) / (hi - lo)) * (H - T - B);
  const line = pts.map((p, i) => (i ? "L" : "M") + X(p.t).toFixed(1) + " " + Y(p[c.k]).toFixed(1)).join("");
  const area = line + `L${X(t1).toFixed(1)} ${H - B}L${L} ${H - B}Z`;
  const dec = hi - lo < 5 ? 1 : 0;
  const yt = [lo, (lo + hi) / 2, hi].map((v) => `<line x1="${L}" x2="${W - R}" y1="${Y(v).toFixed(1)}" y2="${Y(v).toFixed(1)}"/>`).join("");
  const yl = [lo, (lo + hi) / 2, hi].map((v) => `<text class="ax" x="${L - 8}" y="${(Y(v) + 3.5).toFixed(1)}" text-anchor="end">${fmtN(v, dec)}</text>`).join("");
  const hhmm = (t) => new Date(t * 1000).toLocaleTimeString("ru-RU", { hour: "2-digit", minute: "2-digit" });
  const xl = [0.12, 0.38, 0.64, 0.9].map((f) => { const t = t0 + (t1 - t0) * f; return `<text class="ax" x="${X(t).toFixed(1)}" y="${H - 4}" text-anchor="middle">${hhmm(t)}</text>`; }).join("");
  box.innerHTML = `<svg viewBox="0 0 ${W} ${H}" aria-hidden="true"><g class="grid">${yt}</g>${yl}${xl}<path class="a" d="${area}"/><path class="l" d="${line}"/>
    <line class="cross" y1="${T}" y2="${H - B}" x1="-10" x2="-10"/><circle class="pt" r="4" cx="-10" cy="-10"/></svg><div class="ctip"></div>`;
  if (fresh) drawIn($("path.l", box), $("path.a", box));
  const svg = $("svg", box), cross = $(".cross", box), pt = $(".pt", box), tipEl = $(".ctip", box);
  box.onpointermove = (e) => {
    const r = svg.getBoundingClientRect(), x = ((e.clientX - r.left) / r.width) * W;
    const t = t0 + ((x - L) / (W - L - R)) * (t1 - t0);
    let best = pts[0];
    for (const p of pts) if (Math.abs(p.t - t) < Math.abs(best.t - t)) best = p;
    const bx = X(best.t), by = Y(best[c.k]);
    cross.setAttribute("x1", bx); cross.setAttribute("x2", bx); pt.setAttribute("cx", bx); pt.setAttribute("cy", by);
    tipEl.textContent = `${fmtN(best[c.k], c.dec || 0)} ${c.u} · ${hhmm(best.t)}`;
    tipEl.style.setProperty("left", `${(bx / W) * 100}%`);
  };
}
export let chartsResize = 0;
window.addEventListener("resize", () => { clearTimeout(chartsResize); chartsResize = setTimeout(() => { if (page === "charts") for (const c of CHARTS) drawChart(c, false); if (page === "home") drawKpis(); }, 150); });

// край карточки светится там, где указатель
document.addEventListener("pointermove", (e) => {
  const c = e.target.closest?.(".card, .sec, .np, .kpi");
  if (!c) return;
  const r = c.getBoundingClientRect();
  c.style.setProperty("--mx", `${e.clientX - r.left}px`);
  c.style.setProperty("--my", `${e.clientY - r.top}px`);
}, { passive: true });

// плашка активного пункта меню переезжает на пружине
export function navPill() {
  const nav = $("#nav");
  if (!nav) return;
  let pill = $(".nav-pill", nav);
  if (!pill) { pill = document.createElement("span"); pill.className = "nav-pill instant"; nav.prepend(pill); nav.classList.add("has-pill"); }
  const t = $(".nav > a.on, .prof.on", nav);
  if (!t || !t.offsetHeight) { pill.style.setProperty("opacity", "0"); return; }
  pill.style.setProperty("transform", `translateY(${t.offsetTop}px)`);
  pill.style.setProperty("height", `${t.offsetHeight}px`);
  pill.style.setProperty("opacity", "1");
  requestAnimationFrame(() => requestAnimationFrame(() => pill.classList.remove("instant")));
}

// ---------------------------------------------------------------- линия графика «прорисовывается» (длина пути считается в пикселях)
export function drawIn(line, area, ms = 1100) {
  if (Motion.reduce || !line.getTotalLength) return;
  const len = line.getTotalLength();
  line.style.setProperty("transition", "none");
  line.style.setProperty("stroke-dasharray", `${len} ${len}`);
  line.style.setProperty("stroke-dashoffset", String(len));
  if (area) { area.style.setProperty("transition", "none"); area.style.setProperty("opacity", "0"); }
  line.getBoundingClientRect();
  requestAnimationFrame(() => {
    line.style.setProperty("transition", `stroke-dashoffset ${ms}ms cubic-bezier(.22,1,.36,1)`);
    line.style.setProperty("stroke-dashoffset", "0");
    if (area) { area.style.setProperty("transition", `opacity ${ms * 0.7}ms ease ${ms * 0.35}ms`); area.style.setProperty("opacity", "1"); }
  });
  setTimeout(() => { if (line.isConnected) { line.style.removeProperty("stroke-dasharray"); line.style.removeProperty("stroke-dashoffset"); } }, ms + 120);
}
