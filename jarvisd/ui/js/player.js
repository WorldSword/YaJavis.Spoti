// Плеер: карточка на главной и отдельная страница.
import { $, $$, Motion, act, api, copyText, esc, icon, jelly, toast } from "./core.js";
import { S } from "./main.js";

// ---------------------------------------------------------------- плеер на главной (Glass player, Kokonut UI «Now Playing», MIT)
// Пластинка с обложкой видео: разгоняется и останавливается по инерции (τ 0,22 / 0,2 с), 33⅓ об/мин — 200°/с;
// её можно крутить пальцем — это перемотка. Тонарм опускается за 500 мс, эквалайзер дышит в такт,
// цифры времени перекатываются (220 мс). Карточка всегда одной высоты.
export let M = null, mAt = 0;
export const NP = { angle: 0, omega: 0, env: 0, held: false, scrub: null, lastA: 0, lastT: 0, samples: [], seekHeld: null };
export const CRUISE = 200, SPIN_UP = 0.22, SPIN_DOWN = 0.2, COAST = 0.45;
export const clockStr = (s) => { s = Math.max(0, Math.floor(s || 0)); return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`; };
export const mPlaying = () => !!M?.playing && !M.paused;
export const mPos = () => !M ? 0 : NP.scrub != null ? NP.scrub : NP.seekHeld != null ? NP.seekHeld : mPlaying() ? Math.min(M.dur || Infinity, M.pos + (Date.now() - mAt) / 1000) : M.pos;
export const hash = (n) => { const s = Math.sin(n * 127.1) * 43758.5453; return s - Math.floor(s); };
export const noise = (x) => { const i = Math.floor(x), f = x - i, u = f * f * (3 - 2 * f); return hash(i) * (1 - u) + hash(i + 1) * u; };

export const ARM = `<svg class="np-arm" viewBox="0 0 40 110" aria-hidden="true"><circle class="a1" cx="31" cy="11" r="8"/><circle class="a2" cx="31" cy="11" r="3"/><path class="a3" d="M31 11 L31 70 L18 92"/><rect class="a4" height="12" rx="1.5" transform="rotate(32 16 96)" width="7" x="12.5" y="90"/></svg>`;

export function playerHtml() {
  return `<section class="card np" id="np" data-state="idle">
    <div class="mu-top">
      <a class="cover" href="#player" id="np-cover" aria-label="Открыть плеер">${icon("music")}<img id="np-art" alt=""></a>
      <div class="np-info"><div class="np-src" id="np-kicker"></div><div class="np-title" id="np-title">Тишина</div><div class="np-artist" id="np-artist">Включите что-нибудь с YouTube или попросите Алису</div></div>
    </div>
    <div class="progress np-play-ui"><span class="roll" id="np-cur"></span><div id="np-seek"></div><span id="np-dur"></span></div>
    <div class="controls np-play-ui">
      <button class="cbtn" type="button" data-m="stop" aria-label="Выключить">${icon("stop", "solid")}</button>
      <button class="cbtn" type="button" data-m="prev" id="np-prev" aria-label="Предыдущий">${icon("prev", "solid")}</button>
      <button class="cbtn big" type="button" data-m="toggle" id="np-toggle" aria-label="Пауза">${icon("play", "solid")}</button>
      <button class="cbtn" type="button" data-m="next" id="np-next" aria-label="Следующий">${icon("skip", "solid")}</button>
      <button class="cbtn" type="button" data-m="copy" aria-label="Скопировать ссылку">${icon("link")}</button>
    </div>
    <form class="np-form" id="np-form"><input type="text" id="np-q" placeholder="Название или ссылка на YouTube" aria-label="Что включить" autocomplete="off"><button class="cbtn big" type="submit" aria-label="Включить">${icon("play", "solid")}</button></form>
  </section>
  ${volCardHtml()}`;
}

// громкость колонки — отдельная карточка (на главной и на странице «Плеер»)
export function volCardHtml() {
  return `<section class="card vol-card" id="np-vol">
    <div class="card-head"><h2>Громкость колонки</h2><span class="meta">Алиса, музыка и Джарвис</span></div>
    <div class="vol-main">
      <div class="vol-num"><b class="vol-big" id="vol-big">–</b><small>из 10</small></div>
      <div class="vol-ctl">
        <button class="cbtn" type="button" data-vol="-1" aria-label="Тише">${icon("minus")}</button>
        <div class="vol-sl"><div id="vol"></div><div class="vol-ticks" aria-hidden="true">${Array.from({ length: 11 }, (_, i) => `<i data-n="${i}"></i>`).join("")}</div></div>
        <button class="cbtn" type="button" data-vol="1" aria-label="Громче">${icon("plus")}</button>
      </div>
    </div>
  </section>`;
}

export async function musicData() {
  try { M = await api("GET", "/api/music"); mAt = Date.now(); } catch { return; }
  drawPlayer(); drawPlayerPage();
  $("#nav-eq")?.classList.toggle("hidden", !mPlaying());
}

// обложка: YouTube — крупная (если есть), иначе средняя; Яндекс Музыка — как пришла
export function setArt(img, art, t) {
  if (img.dataset.src === art) return;
  img.dataset.src = art; img.classList.remove("ok");
  if (!art) { img.removeAttribute("src"); return; }
  img.onload = () => { if (img.naturalWidth <= 120 && img.src.includes("maxres")) { img.src = artOf(t, false); return; } img.classList.add("ok"); };
  img.onerror = () => { if (img.src.includes("maxres")) img.src = artOf(t, false); };
  img.src = art;
}

export function drawPlayer() {
  const np = $("#np");
  if (!np || !M) return;
  const t = M.track || {}, on = M.playing || M.paused, ya = extSrc(), nm = srcName();
  np.dataset.state = M.loading && !on ? "loading" : !on ? "idle" : M.paused ? "paused" : "playing";
  setArt($("#np-art"), on ? artOf(t, true) : "", t);
  $("#np-kicker").innerHTML = on ? `<span class="np-eq" aria-hidden="true"><i></i><i></i><i></i></span>${nm}${M.paused ? " · пауза" : ""}` : M.loading ? "ищу…" : "Плеер";
  swapText($("#np-title"), on ? t.Title || "…" : M.loading ? "Ищу и включаю…" : "Тишина");
  swapText($("#np-artist"), on ? t.Author || nm : "Включите что-нибудь с YouTube, скажите «Джарвис, включи … в Spotify» или попросите Алису");
  $("#np-toggle").innerHTML = icon(M.paused ? "play" : "pause", "solid");
  $("#np-toggle").setAttribute("aria-label", M.paused ? "Продолжить" : "Пауза");
  $("#np-prev").disabled = !ya && (M.history || []).length < 2;
  $("#np-next").disabled = !ya && !(M.next || []).length;
  $("#np-dur").textContent = M.dur ? clockStr(M.dur) : "";
  Motion.add(npAnim);
}

// управление музыкой — общее для главной и страницы «Плеер»
export function musicAct(a, b) {
  if (!M) return;
  if (b) { b.classList.remove("k-prev", "k-next", "k-pop"); void b.offsetWidth; b.classList.add(a === "prev" ? "k-prev" : a === "next" ? "k-next" : "k-pop"); }
  if (a === "copy") { const l = musicLink(); if (l) copyText(l).then(() => toast("Ссылка скопирована"), () => toast("Не получилось скопировать", true)); return; }
  if (a === "radio") { music({ action: "radio", on: !M.radio }, b).then((ok) => ok && toast(M.radio ? "Радио: после очереди — похожее" : "Радио выключено: после очереди тишина")); return; }
  if (a === "prev" && !extSrc()) { const h = (M.history || [])[1]; if (h) music({ action: "id", query: h.ID }, b); return; }
  music({ action: a === "toggle" ? (M.paused ? "resume" : "pause") : a }, b);
}

// перекатывающиеся цифры: сменившаяся уезжает вверх, новая въезжает снизу (назад — наоборот)
export function rollTo(el, text, dir) {
  if (el.dataset.v === text) return;
  const old = el.dataset.v || "";
  el.dataset.v = text;
  if (Motion.reduce || old.length !== text.length) { el.innerHTML = [...text].map((ch) => `<span class="rd"><span>${ch}</span></span>`).join(""); return; }
  [...text].forEach((ch, i) => {
    const slot = el.children[i];
    if (!slot || old[i] === ch) return;
    slot.innerHTML = `<span class="out ${dir > 0 ? "up" : "down"}">${old[i]}</span><span class="in ${dir > 0 ? "up" : "down"}">${ch}</span>`;
  });
}

export const npAnim = {
  lastSec: -1,
  t: 0,
  frame(dt) { this.t += dt; },
  render() {
    const p = mPos(), t = Motion.reduce ? 0.37 : this.t, env = mPlaying() ? 1 : 0;
    $$(".np-eq i").forEach((bar, i) => {
      const lvl = 0.18 + 0.82 * (0.6 * noise(t * 4.3 + i * 17.3) + 0.4 * noise(t * 9.1 + i * 31.7 + 100)) * env;
      bar.style.transform = `scaleY(${lvl.toFixed(3)})`;
    });
    if (seekJ && M && M.dur && !seekJ.dragging) { seekJ.range(0, M.dur); seekJ.set(p); }
    const sec = Math.floor(p), cur = $("#np-cur") || $("#pl-cur");
    if (cur && (sec !== this.lastSec || cur !== this.lastEl)) { rollTo(cur, M && (M.playing || M.paused) ? clockStr(p) : "", sec >= this.lastSec ? 1 : -1); this.lastSec = sec; this.lastEl = cur; }
  },
  done() { return (!$("#np") && !$("#plh")) || !mPlaying(); },
};

export function playerSetup() {
  const np = $("#np");
  np.addEventListener("click", (e) => {
    const b = e.target.closest("[data-m]");
    if (!b || b.disabled) return;
    musicAct(b.dataset.m, b);
  });
  $("#np-form").addEventListener("submit", (e) => {
    e.preventDefault();
    const q = $("#np-q").value.trim();
    if (!q) return;
    $("#np-q").value = "";
    M = { ...(M || {}), loading: true }; drawPlayer();
    music({ action: "play", query: q });
  });
  makeSliders($("#np-seek"), $("#vol"));
}

// перемотка и громкость — «желейные» ползунки (на главной и в плеере)
export function makeSliders(seekEl, volEl) {
  seekJ = jelly(seekEl, { label: "Перемотка", min: 0, max: Math.max(1, M?.dur || 1), value: 0, format: clockStr, keyStep: 5,
    disabled: () => !M?.dur,
    onInput: (v) => { NP.seekHeld = v; Motion.add(npAnim); },
    onChange: (v) => { NP.seekHeld = null; if (!M?.dur) return; M.pos = v; mAt = Date.now(); Motion.add(npAnim); music({ action: "seek", sec: Math.round(v) }); } });
  volJ = jelly(volEl, { label: "Громкость колонки", min: 0, max: 10, step: 1, value: S.speaker.volume > 0 ? S.speaker.volume : 5, format: (v) => String(v),
    onInput: (v) => { volDrag = true; volShow(v); },
    onChange: (v) => setVolume(v) });
  const card = volEl.closest(".vol-card");
  card?.addEventListener("click", (e) => {
    const b = e.target.closest("[data-vol]");
    if (!b) return;
    const cur = volWant ?? (S.speaker.volume >= 0 ? S.speaker.volume : 5);
    const v = Motion.clamp(cur + Number(b.dataset.vol), 0, 10);
    b.classList.remove("k-pop"); void b.offsetWidth; b.classList.add("k-pop");
    if (v === cur) return;
    volJ.set(v, true); volShow(v); setVolume(v);
  });
  if (S.speaker.volume >= 0) volShow(S.speaker.volume);
}

// число и деления под ползунком; отправка с задержкой — чтобы частые «+» не долбили колонку
export let volWant = null, volTimer = 0;
export function volShow(v) {
  const big = $("#vol-big"); if (big) { big.textContent = v; big.dataset.v = v; }
  $$(".vol-ticks i").forEach((t) => t.classList.toggle("on", Number(t.dataset.n) <= v));
  const c = $("#np-vol"); if (c) c.dataset.lvl = v === 0 ? "mute" : v <= 3 ? "low" : v <= 7 ? "mid" : "high";
}
export function setVolume(v) {
  volDrag = true; volWant = v; volShow(v);
  clearTimeout(volTimer);
  volTimer = setTimeout(() => {
    api("POST", "/api/speaker/volume", { level: v }).then((r) => { S.speaker.volume = r.volume; })
      .catch((e) => toast(e.message, true)).finally(() => { if (volWant === v) { volWant = null; volDrag = false; } });
  }, 250);
}

export async function music(body, btn) {
  let ok = false;
  await act(btn, async () => { M = await api("POST", "/api/music", body); mAt = Date.now(); ok = true; });
  drawPlayer(); drawPlayerPage();
  $("#nav-eq")?.classList.toggle("hidden", !mPlaying());
  return ok;
}

// громкость колонки (её слышно у Алисы, музыки и Джарвиса); неизвестна — ползунок прячем
export let volDrag = false, seekJ = null, volJ = null;
export function drawVolume() {
  const box = $("#np-vol");
  if (!box || !volJ || volDrag) return;
  const v = S.speaker.volume;
  box.classList.toggle("hidden", v == null || v < 0);
  if (v != null && v >= 0) { volJ.set(v, true); volShow(v); }
}

// ---------------------------------------------------------------- плеер — отдельная страница
export const extSrc = () => M?.source === "yandex" || M?.source === "spotify"; // очередью управляет внешний плеер
export const srcName = () => M?.source === "yandex" ? "Яндекс Музыка" : M?.source === "spotify" ? "Spotify" : "YouTube";
export const artOf = (t, big) => !t?.ID ? "" : extSrc() ? (M.cover || "") : `https://i.ytimg.com/vi/${t.ID}/${big ? "maxresdefault" : "mqdefault"}.jpg`;
export const musicLink = () => {
  const id = M?.track?.ID;
  if (!id) return "";
  if (M.source === "spotify") { const m = /^spotify:(track|episode):(\w+)$/.exec(id); return M.link || (m ? `https://open.spotify.com/${m[1]}/${m[2]}` : ""); }
  return M.link || `https://youtu.be/${id}`;
};

export function viewPlayer() {
  $("#view").innerHTML = `<header class="ph"><h1>Плеер</h1><p class="sub">Всё, что играет на колонке: Spotify, YouTube Джарвиса и Яндекс Музыка Алисы</p></header>
  <section class="card pl-hero" id="plh" data-state="idle"><img class="pl-glow" id="pl-glow" alt="">
    <div class="pl-top">
      <div class="pl-cover" id="pl-cover">${icon("music")}<img id="pl-img" alt=""></div>
      <div class="pl-meta">
        <div class="pl-kick"><span class="np-eq" aria-hidden="true"><i></i><i></i><i></i></span><span id="pl-kick">Тишина</span></div>
        <h2 class="pl-title mq-line" id="pl-title"></h2>
        <div class="pl-artist" id="pl-artist"></div>
        <div class="pl-chips" id="pl-chips"></div>
      </div>
    </div>
    <div class="pl-bottom">
      <div class="progress pl-seek"><span class="roll" id="pl-cur"></span><div id="pl-seek"></div><span id="pl-dur"></span></div>
      <div class="pl-bar">
        <div class="pl-l">
          <button class="cbtn" type="button" data-p="radio" id="pl-radio" data-tip="Радио: когда очередь кончится, играть похожее" aria-label="Радио">${icon("radio")}</button>
          <button class="cbtn" type="button" data-p="copy" id="pl-copy" data-tip="Скопировать ссылку" aria-label="Скопировать ссылку">${icon("link")}</button>
          <a class="cbtn" id="pl-open" target="_blank" rel="noopener" data-tip="Открыть на YouTube" aria-label="Открыть в браузере">${icon("ext")}</a>
        </div>
        <div class="pl-controls">
          <button class="cbtn" type="button" data-p="stop" data-tip="Выключить" aria-label="Выключить">${icon("stop", "solid")}</button>
          <button class="cbtn" type="button" data-p="prev" id="pl-prev" aria-label="Предыдущий">${icon("prev", "solid")}</button>
          <button class="cbtn big" type="button" data-p="toggle" id="pl-toggle" aria-label="Пауза">${icon("play", "solid")}</button>
          <button class="cbtn" type="button" data-p="next" id="pl-next" aria-label="Следующий">${icon("skip", "solid")}</button>
        </div>
        <div class="pl-vol" id="np-vol" title="Громкость колонки">${icon("speaker")}<div id="vol"></div><b class="vol-big" id="vol-big">–</b></div>
      </div>
    </div>
  </section>
  <div class="cols stagger">
    <section class="card"><div class="card-head"><h2>Очередь</h2><span class="meta" id="pl-qmeta"></span></div>
      <form class="q-add" id="pl-form"><label class="q-field">${icon("search")}<input type="text" id="pl-q" placeholder="Название или ссылка на YouTube" autocomplete="off" aria-label="Что включить"></label>
        <button class="cbtn" type="submit" data-a="play" data-tip="Включить сейчас" aria-label="Включить сейчас">${icon("play", "solid")}</button>
        <button class="cbtn" type="submit" data-a="queue" data-tip="В очередь" aria-label="В очередь">${icon("plus")}</button></form>
      <div class="qlist" id="pl-queue"></div></section>
    <section class="card"><div class="card-head"><h2>Недавно играло</h2><span class="meta">YouTube · нажмите, чтобы включить</span></div><div class="qlist" id="pl-hist"></div></section>
  </div>`;
  $("#plh").addEventListener("click", (e) => {
    const b = e.target.closest("[data-p]");
    if (!b || b.disabled) return;
    musicAct(b.dataset.p, b);
  });
  $("#pl-form").addEventListener("submit", (e) => {
    e.preventDefault();
    const q = $("#pl-q").value.trim(), a = e.submitter?.dataset.a || "play";
    if (!q) return;
    $("#pl-q").value = "";
    if (a === "play") { M = { ...(M || {}), loading: true }; drawPlayerPage(); }
    music({ action: a, query: q }).then((ok) => ok && a === "queue" && toast("Добавлено в очередь"));
  });
  const onRow = (e) => {
    const spb = e.target.closest("[data-sp]");
    if (spb) { music({ action: "spotify", query: spb.dataset.sp }); return; }
    const rm = e.target.closest("[data-un]");
    if (rm) { e.stopPropagation(); music({ action: "unqueue", index: +rm.dataset.un }); return; }
    const r = e.target.closest(".qrow[data-id]");
    if (!r || r.classList.contains("cur")) return;
    if (r.dataset.qi != null) music({ action: "unqueue", index: +r.dataset.qi }).then(() => music({ action: "id", query: r.dataset.id }));
    else music({ action: "id", query: r.dataset.id });
  };
  $("#pl-queue").addEventListener("click", onRow);
  $("#pl-hist").addEventListener("click", onRow);
  makeSliders($("#pl-seek"), $("#vol"));
  drawVolume();
  if (M) drawPlayerPage();
  musicData();
}

export function swapText(el, text) {
  if (el.title === text && el.firstChild) return;
  el.title = text;
  if (el.classList.contains("mq-line")) {   // не влезает — бежит туда и обратно (как в yapanel)
    el.innerHTML = `<span class="mq">${esc(text)}</span>`;
    el.classList.remove("run");
    requestAnimationFrame(() => {
      const over = el.scrollWidth - el.clientWidth;
      if (over > 4) { el.style.setProperty("--mq", `${-over - 24}px`); el.style.setProperty("--mqt", `${Math.max(6, over / 28)}s`); el.classList.add("run"); }
    });
  } else el.textContent = text;
  el.classList.remove("swap"); void el.offsetWidth; el.classList.add("swap");
}

export function qrowHtml(t, { badge = "", cur = false, qi = null, art = "", ro = false } = {}) {
  const img = art || (t.ID && !/^\d+$/.test(t.ID) ? `https://i.ytimg.com/vi/${esc(t.ID)}/mqdefault.jpg` : "");
  return `<div class="qrow${cur || ro ? " cur" : ""}"${ro ? "" : ` data-id="${esc(t.ID)}"`}${qi != null ? ` data-qi="${qi}"` : ""}${cur || ro ? "" : ` role="button" tabindex="0"`}>
    ${img ? `<img src="${esc(img)}" alt="" loading="lazy">` : `<span class="qph"></span>`}<span class="tt"><b>${esc(t.Title || "…")}</b><span>${esc(t.Author || "")}</span></span>${badge}
    ${qi != null ? `<button class="ib" type="button" data-un="${qi}" aria-label="Убрать из очереди" data-tip="Убрать">${icon("x")}</button>` : ""}</div>`;
}

export function drawPlayerPage() {
  const h = $("#plh");
  if (!h || !M) return;
  const t = M.track || {}, on = M.playing || M.paused, ya = extSrc(), sp = M.source === "spotify", nm = srcName();
  h.dataset.state = M.loading && !on ? "loading" : !on ? "idle" : M.paused ? "paused" : "playing";
  const art = on ? artOf(t, true) : "", glow = $("#pl-glow");
  setArt($("#pl-img"), art, t);
  const g = art.replace("maxresdefault", "mqdefault");
  if (glow.dataset.src !== g) { glow.dataset.src = g; if (g) glow.src = g; else glow.removeAttribute("src"); }
  swapText($("#pl-title"), on ? t.Title || "…" : M.loading ? "Ищу и включаю…" : "Ничего не играет");
  swapText($("#pl-artist"), on ? t.Author || nm : "Скажите «Джарвис, включи … в Spotify», «включи с ютуба …» или попросите Алису");
  $("#pl-kick").textContent = on ? `${nm}${M.paused ? " · пауза" : ""}` : M.loading ? "YouTube · загрузка" : "Тишина";
  const chips = on ? [sp ? "через Soloist" : ya ? "через Алису" : "через Джарвиса", M.album && `Альбом «${M.album}»`, !ya && M.radio && "радио"].filter(Boolean) : [];
  const link = on ? musicLink() : "", open = $("#pl-open");
  if (link) open.href = link; else open.removeAttribute("href");
  open.classList.toggle("hidden", !link);
  open.dataset.tip = sp ? "Открыть в Spotify" : ya ? "Открыть в Яндекс Музыке" : "Открыть на YouTube";
  const ch = chips.map((c) => `<span class="chip">${esc(c)}</span>`).join("");
  if ($("#pl-chips").dataset.h !== ch) { $("#pl-chips").dataset.h = ch; $("#pl-chips").innerHTML = ch; }
  $("#pl-dur").textContent = M.dur ? clockStr(M.dur) : "";
  $("#pl-toggle").innerHTML = icon(M.paused || !on ? "play" : "pause", "solid");
  $("#pl-toggle").disabled = !on;
  $("#pl-prev").disabled = !on || (!ya && (M.history || []).length < 2);
  $("#pl-next").disabled = !on || (!ya && !(M.next || []).length);
  $("#pl-radio").classList.toggle("on", !!M.radio);
  $("#pl-radio").classList.toggle("hidden", ya);
  $("#pl-copy").classList.toggle("hidden", !link);
  let qh;
  const spBtns = `<div class="row sp-quick"><button class="btn" type="button" data-sp="liked">♥ Любимые</button><button class="btn" type="button" data-sp="radio"${on ? "" : " disabled"}>Радио по треку</button><button class="btn" type="button" data-sp="dj">DJ</button></div>`;
  if (sp) {
    const next = M.next || [];
    qh = (on ? qrowHtml(t, { cur: true, badge: `<span class="np-eq q-eq" aria-hidden="true"><i></i><i></i><i></i></span>`, art: M.cover }) : "") +
      next.map((x, i) => qrowHtml(x, { ro: true, art: x.Art, badge: `<span class="badge">${i + 1}</span>` })).join("") +
      (on && !next.length ? `<p class="empty">Дальше очередь Spotify пока пуста.</p>` : "") + spBtns;
  } else if (ya) qh = on ? qrowHtml(t, { cur: true, badge: `<span class="np-eq q-eq" aria-hidden="true"><i></i><i></i><i></i></span>`, art: M.cover }) + `<p class="empty">Очередью Яндекс Музыки управляет Алиса: «Алиса, следующий трек», «Алиса, включи похожее».</p>` : "";
  else {
    const next = M.next || [], nq = M.queue || 0;
    qh = (on ? qrowHtml(t, { cur: true, badge: `<span class="np-eq q-eq" aria-hidden="true"><i></i><i></i><i></i></span>` }) : "") +
      next.map((x, i) => qrowHtml(x, { qi: i < nq ? i : null, badge: i < nq ? `<span class="badge">${i + 1}</span>` : `<span class="badge">${M.radio ? "похожее" : "дальше"}</span>` })).join("");
  }
  if (!qh) qh = `<p class="empty">Очередь пуста. Найдите трек выше и нажмите «+» или скажите «Джарвис, добавь в очередь …»</p>`;
  if (!sp && S?.spotify?.installed) qh += spBtns;
  if ($("#pl-queue").dataset.h !== qh) { $("#pl-queue").dataset.h = qh; $("#pl-queue").innerHTML = qh; }
  $("#pl-qmeta").textContent = sp ? `Spotify · дальше ${(M.next || []).length}` : ya ? nm : `${M.queue || 0} в очереди${M.radio ? " · дальше похожее" : ""}`;
  const hist = (M.history || []).filter((x) => !(on && !ya && x.ID === t.ID)).slice(0, 12);
  const hh = hist.length ? hist.map((x) => qrowHtml(x)).join("") : `<p class="empty">Здесь появится то, что включали с YouTube</p>`;
  if ($("#pl-hist").dataset.h !== hh) { $("#pl-hist").dataset.h = hh; $("#pl-hist").innerHTML = hh; }
  Motion.add(npAnim);
}
