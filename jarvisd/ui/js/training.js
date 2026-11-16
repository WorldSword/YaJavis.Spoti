// Обучение слова «Джарвис» и запись фона.
import { $, $$, Motion, act, api, esc, icon, plural, sec, tip, toast, upload, waitRestart } from "./core.js";
import { page } from "./main.js";
import { secHead } from "./security.js";

// ---------------------------------------------------------------- обучение слова
export const KIND = { pos: "Джарвис", false: "Не Джарвис", near: "Не проверено", bg: "Фон" };
export const GUESS = { pos: "колонка решила, что звали", false: "колонка сработала, но решила, что зря", near: "колонка насторожилась, но не сработала" };
export let T = null, skipped = new Set(), clipFilter = "all", clipLimit = 40;

export const clipUrl = (x) => `/api/train/clip?kind=${x.kind}&name=${encodeURIComponent(x.name)}`;
export const clipWhen = (x) => new Date(x.time).toLocaleString("ru-RU", { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });
export const clipProb = (x) => (x.name.match(/_p(\d+)/) || [])[1];
export const clipSec = (x) => Math.max(0, (x.size - 44) / 32000);
export const queue = () => (T?.clips || []).filter((x) => !x.reviewed && x.kind !== "bg" && !skipped.has(x.kind + "/" + x.name))
  .sort((a, b) => ({ near: 0, pos: 1, false: 2 }[a.kind] - { near: 0, pos: 1, false: 2 }[b.kind]) || (new Date(b.time) - new Date(a.time)));

export async function viewTraining() {
  try { T = await api("GET", "/api/train"); } catch (e) { toast(e.message, true); return; }
  T.clips ||= [];
  const used = (T.used_kb / 1024).toFixed(1);
  $("#view").innerHTML = `${secHead("training")}
  ${sec("Запись", "Включите на день-два и пользуйтесь колонкой как обычно: она запишет оклики, «почти оклики» и немного фона. Ложные срабатывания пишутся всегда",
    `<label class="sw"><input type="checkbox" id="t-mode"${T.mode ? " checked" : ""}><span class="tr"></span>Записывать для обучения</label>
    <div class="f"><label>Место на колонке под записи ${tip(T.cloud ? "На колонке лежат только свежие записи. Когда лимит заполнен, самые старые уходят на Google Диск пачкой по 10 — в «Проверке» они по-прежнему есть." : "Записи лежат на колонке, а места там мало. Когда лимит заполнен, сначала удаляются старый фон и «почти оклики». Подключите Google Диск — и старые записи будут уходить туда, а не удаляться.")}<b id="t-lim-v" class="val">${used} из ${T.limit_mb} МБ</b></label><input type="range" id="t-lim" min="1" max="20" value="${T.limit_mb}"><div class="scale"><span>1 МБ</span><span>свободно на колонке ${T.free_mb} МБ</span></div></div>
    <p class="note cloud-note">${T.cloud ? `${icon("cloud")}<span>На Google Диске <b>${T.remote || 0}</b> ${plural(T.remote || 0, "запись", "записи", "записей")} — папка «Jarvis — обучение». Старые уходят туда пачками по 10${T.offloading ? " · <b>отправляю…</b>" : ""}</span>` : `${icon("cloud")}<span>Подключите Google Диск в разделе «Фон комнаты» ниже — тогда старые записи будут уходить туда, а не удаляться</span>`}</p>
    ${T.cloud_error ? `<p class="err">Google Диск: ${esc(T.cloud_error)}</p>` : ""}`)}
  ${sec("Проверка", "Запись играет по кругу — ответьте, было ли в ней слово «Джарвис». Клавиши: 1 — да, 2 — нет, пробел — пауза", `<div id="rv"></div>`)}
  ${sec("Фон комнаты", "Несколько часов звука вашей комнаты — ТВ, разговоры, музыка, — чтобы модель не путала их с «Джарвис». Каждая минута сразу уходит на Google Диск; минуты, где звали Джарвиса, не отправляются", `<div id="bg"><p class="note">…</p></div>`)}
  ${sec("Обучить модель", "Когда записи размечены — скачайте их и обучите модель на компьютере с видеокартой NVIDIA, около 5 минут", trainStepsHtml())}
  ${sec("Все записи", "", `<div id="clips"></div>`)}`;
  drawReview(); drawBg(); drawClips();
  $("#t-mode").addEventListener("change", (e) => api("PUT", "/api/settings", { train_mode: e.target.checked }).then(() => { toast(e.target.checked ? "Запись для обучения включена" : "Запись выключена"); viewTraining(); }).catch((err) => toast(err.message, true)));
  $("#t-lim").addEventListener("input", (e) => ($("#t-lim-v").textContent = `${used} из ${e.target.value} МБ`));
  $("#t-lim").addEventListener("change", (e) => api("PUT", "/api/settings", { train_limit_mb: +e.target.value }).catch((err) => toast(err.message, true)));
  $("#t-model").addEventListener("change", (e) => { const file = e.target.files[0]; if (!file) return; act(null, async () => { await upload("PUT", "/api/train/model", file); await waitRestart("Модель загружена — Джарвис перезапускается"); }); });
  $("#t-restore")?.addEventListener("click", (e) => act(e.target, async () => { await api("POST", "/api/train/model/restore"); await waitRestart("Возвращаю прежнюю модель — Джарвис перезапускается"); }));
}

export function trainStepsHtml() {
  const c = T.counts || {};
  return `<div class="row"><div class="chips"><span class="chip k-pos">Джарвис <b id="c-pos">${c.pos || 0}</b></span><span class="chip k-false">Не Джарвис <b id="c-false">${c.false || 0}</b></span><span class="chip">Фон <b>${c.bg || 0}</b></span></div>
      <span class="spacer"></span>${T.clips.length ? `<a class="btn primary" href="/api/train/download" download data-tip="Все записи — и с колонки, и с Google Диска — плюс текущая модель и скрипт finetune.py: распакуйте и запустите «python finetune.py»">Скачать записи</a>` : `<button class="btn" disabled>Скачать записи</button>`}</div>
    <ol class="steps">
      <li><b>Скачайте записи</b> — в архиве они уже разложены по вашим ответам. Фон с Google Диска (папка «Jarvis — фон комнаты») положите в <code>kws/bg_long</code>.</li>
      <li><b>Обучите</b> в папке проекта: <code>python kws/from_speaker.py jarvis-train-….zip</code> — около 3 минут на видеокарте, модель появится в <code>kws/out_next/wake.bin</code>.</li>
      <li><b>Загрузите</b> её сюда — колонка проверит файл и перезапустится. Стало хуже — верните прежнюю.</li></ol>
    <div class="row"><label class="btn" for="t-model">Загрузить wake.bin</label><input type="file" id="t-model" accept=".bin" class="hidden">${T.prev_model ? `<button class="btn ghost" id="t-restore">Вернуть прежнюю модель</button>` : ""}</div>`;
}

// ---- проверка: плеер, который всё время играет текущую запись по кругу
export const RV = { audio: null, started: false, peaks: [], key: "" };
export function rvAudio() {
  if (!RV.audio) {
    RV.audio = new Audio(); RV.audio.loop = true; RV.audio.preload = "auto";
    RV.audio.addEventListener("play", () => { RV.started = true; $("#rv-toggle") && ($("#rv-toggle").innerHTML = icon("pause", "solid")); Motion.add(rvAnim); });
    RV.audio.addEventListener("pause", () => { $("#rv-toggle") && ($("#rv-toggle").innerHTML = icon("play", "solid")); });
  }
  return RV.audio;
}
export const rvAnim = {
  render() {
    const a = RV.audio, wave = $("#rv-wave");
    if (!a || !wave) return;
    const d = a.duration || 0, p = d ? a.currentTime / d : 0;
    const bars = wave.children, lit = Math.round(p * bars.length);
    for (let i = 0; i < bars.length; i++) bars[i].classList.toggle("on", i < lit);
    $("#rv-time").textContent = `${(a.currentTime || 0).toFixed(1)} / ${d ? d.toFixed(1) : "…"} с`;
  },
  frame() {},
  done() { return !RV.audio || RV.audio.paused || !$("#rv-wave"); },
};

export async function rvWave(url) {
  const wave = $("#rv-wave");
  try {
    const buf = await (await fetch(url, { credentials: "same-origin" })).arrayBuffer();
    const ctx = rvWave.ctx ||= new (window.AudioContext || window.webkitAudioContext)();
    const data = (await ctx.decodeAudioData(buf)).getChannelData(0);
    const n = 72, step = Math.floor(data.length / n), peaks = [];
    for (let i = 0; i < n; i++) { let m = 0; for (let j = i * step; j < (i + 1) * step; j++) m = Math.max(m, Math.abs(data[j] || 0)); peaks.push(m); }
    const top = Math.max(0.05, ...peaks);
    if (!wave || $("#rv-wave") !== wave) return;
    [...wave.children].forEach((b, i) => b.style.setProperty("--h", `${Math.max(6, (peaks[i] / top) * 100).toFixed(0)}%`));
  } catch { /* волна — украшение */ }
}

export function drawReview() {
  const q = queue(), x = q[0], box = $("#rv");
  if (!box) return;
  if (!x) {
    RV.audio?.pause();
    box.innerHTML = `<div class="rv-done">${icon("done")}<div><b>Всё проверено</b><small>${T.mode ? "Новые записи появятся здесь сами — заглядывайте раз в день-два." : "Включите запись вверху — колонка начнёт собирать оклики для проверки."}</small></div></div>`;
    return;
  }
  const p = clipProb(x), key = x.kind + "/" + x.name;
  box.innerHTML = `<div class="rvp">
    <div class="rvp-head"><span class="chip k-${x.kind}">${esc(GUESS[x.kind])}${p ? ` · ${p}%` : ""}</span><span class="spacer"></span><span class="muted">${esc(clipWhen(x))} · осталось ${q.length}</span></div>
    <div class="rvp-player">
      <button class="np-play" id="rv-toggle" type="button" aria-label="Пауза">${icon(RV.started ? "pause" : "play", "solid")}</button>
      <div class="rvp-wave" id="rv-wave" title="Нажмите — сыграть с этого места">${"<i></i>".repeat(72)}</div>
      <span class="rvp-time" id="rv-time">0.0 / … с</span>
    </div>
    <div class="rvp-q"><b>Было слово «Джарвис»?</b></div>
    <div class="rv-ans"><button class="btn primary" data-a="yes">Да, было <kbd>1</kbd></button><button class="btn" data-a="no">Нет <kbd>2</kbd></button></div>
    <div class="rv-alt"><button class="btn sm ghost" data-a="skip">Пропустить</button><button class="btn sm ghost danger" data-a="del">Не уверен — удалить</button></div>
  </div>`;
  const a = rvAudio();
  if (RV.key !== key) { RV.key = key; a.src = clipUrl(x); }
  // следующую запись (если она на Google Диске) колонка докачивает заранее — ответ «да/нет» без паузы
  const nx = q[1];
  if (nx?.remote && RV.pre !== nx.name) { RV.pre = nx.name; fetch(clipUrl(nx), { credentials: "same-origin" }).catch(() => {}); }
  rvWave(clipUrl(x));
  if (RV.started) a.play().catch(() => {});
  rvAnim.render();
  box.onclick = (e) => {
    const b = e.target.closest("[data-a]");
    if (b) return answer(b.dataset.a, b);
    if (e.target.closest("#rv-toggle")) return a.paused ? a.play().catch(() => {}) : a.pause();
    const w = e.target.closest("#rv-wave");
    if (w && a.duration) { const r = w.getBoundingClientRect(); a.currentTime = ((e.clientX - r.left) / r.width) * a.duration; a.play().catch(() => {}); }
  };
}

export async function answer(a, btn) {
  const x = queue()[0];
  if (!x) return;
  RV.started = true;   // ответили — дальше записи сами играют по кругу
  if (a === "skip") { skipped.add(x.kind + "/" + x.name); drawReview(); return; }
  await act(btn, async () => {
    await api("POST", "/api/train/move", { kind: x.kind, name: x.name, to: a === "yes" ? "pos" : a === "no" ? "false" : "delete", reviewed: true });
    T = await api("GET", "/api/train"); T.clips ||= [];
  });
  const c = T.counts || {};
  if ($("#c-pos")) { $("#c-pos").textContent = c.pos || 0; $("#c-false").textContent = c.false || 0; }
  drawReview(); drawClips();
}

document.addEventListener("keydown", (e) => {
  if (page !== "training" || !$("#rv-toggle") || e.target.closest("input, textarea, select")) return;
  if (e.key === "1") answer("yes");
  else if (e.key === "2") answer("no");
  else if (e.key === " ") { e.preventDefault(); const a = rvAudio(); a.paused ? a.play().catch(() => {}) : a.pause(); }
});

// ---- все записи: аккуратный список, свой маленький плеер на строку
export const LIST = { audio: null, key: "" };
export function drawClips() {
  const box = $("#clips");
  if (!box) return;
  const c = T.counts || {}, all = T.clips;
  const shown = all.filter((x) => clipFilter === "all" || (clipFilter === "todo" ? !x.reviewed && x.kind !== "bg" : x.kind === clipFilter));
  const tabs = [["all", "Все", all.length], ["pos", "Джарвис", c.pos || 0], ["false", "Не Джарвис", c.false || 0], ["bg", "Фон", c.bg || 0], ["todo", "Не проверено", all.filter((x) => !x.reviewed && x.kind !== "bg").length]];
  box.innerHTML = `<div class="row"><div class="seg" id="cl-f">${tabs.map(([k, n, cnt]) => `<button type="button" data-f="${k}" class="${clipFilter === k ? "on" : ""}">${n} <small>${cnt}</small></button>`).join("")}</div>
      <span class="spacer"></span>${all.length ? `<button class="btn sm ghost danger" id="t-clear">Удалить все</button>` : ""}</div>
    <div class="clips">${shown.slice(0, clipLimit).map((x) => {
      const k = x.kind + "/" + x.name, playing = LIST.key === k && LIST.audio && !LIST.audio.paused;
      return `<div class="clip${playing ? " on" : ""}" data-key="${esc(k)}">
        <button class="ib clip-play" data-play="${esc(k)}" aria-label="Прослушать">${icon(playing ? "pause" : "play", "solid")}</button>
        <span class="clip-meta"><b>${esc(clipWhen(x))}${x.remote ? ` <span class="on-cloud" data-tip="На Google Диске — колонка докачает при прослушивании">${icon("cloud")}</span>` : ""}</b><small>${clipSec(x).toFixed(1)} с${clipProb(x) ? ` · ${clipProb(x)}%` : ""}${x.reviewed ? " · проверено" : ""}</small></span>
        <div class="seg sm">${x.kind === "bg" ? `<button type="button" class="on" disabled>Фон</button>` : `<button type="button" data-mv="pos" data-k="${x.kind}" data-n="${esc(x.name)}" class="${x.kind === "pos" ? "on" : ""}">Джарвис</button><button type="button" data-mv="false" data-k="${x.kind}" data-n="${esc(x.name)}" class="${x.kind === "false" ? "on" : ""}">Не Джарвис</button>`}</div>
        <button class="ib clip-del" data-mv="delete" data-k="${x.kind}" data-n="${esc(x.name)}" aria-label="Удалить">${icon("x")}</button>
      </div>`; }).join("") || `<p class="note">Здесь пока пусто</p>`}</div>
    ${shown.length > clipLimit ? `<button class="btn sm ghost" id="cl-more">Показать ещё ${Math.min(40, shown.length - clipLimit)} из ${shown.length - clipLimit}</button>` : ""}`;
  box.onclick = async (e) => {
    const f = e.target.closest("[data-f]"), mv = e.target.closest("[data-mv]"), pl = e.target.closest("[data-play]");
    if (f) { clipFilter = f.dataset.f; clipLimit = 40; drawClips(); return; }
    if (e.target.closest("#cl-more")) { clipLimit += 40; drawClips(); return; }
    if (e.target.closest("#t-clear")) { if (confirm("Удалить все записи для обучения? Сначала лучше скачать архив.")) act(null, async () => { await api("DELETE", "/api/train"); viewTraining(); }, "Записи удалены"); return; }
    if (pl) {
      const a = LIST.audio ||= new Audio();
      if (LIST.key === pl.dataset.play && !a.paused) a.pause();
      else { const [kind, ...n] = pl.dataset.play.split("/"); if (LIST.key !== pl.dataset.play) a.src = clipUrl({ kind, name: n.join("/") }); LIST.key = pl.dataset.play; a.play().catch(() => {}); }
      a.onended = a.onpause = a.onplay = () => drawClips();
      return;
    }
    if (mv && !mv.classList.contains("on")) await act(mv, async () => {
      await api("POST", "/api/train/move", { kind: mv.dataset.k, name: mv.dataset.n, to: mv.dataset.mv, reviewed: true });
      T = await api("GET", "/api/train"); T.clips ||= []; drawClips(); drawReview();
    });
  };
}

// ---------------------------------------------------------------- фон комнаты на Google Диск — в два клика
export let BG = null, bgServer = false, bgHours = 4;
export const hm = (min) => min >= 60 ? `${Math.floor(min / 60)} ч ${min % 60} мин` : `${min} мин`;

export async function drawBg() {
  const box = $("#bg");
  if (!box) return;
  try { BG = await api("GET", "/api/bgrec"); } catch (e) { box.innerHTML = `<p class="err">${esc(e.message)}</p>`; return; }
  const g = BG.gdrive || {}, server = bgServer || BG.to === "server";
  const ready = server ? !!BG.url : g.signed_in;
  let target;
  if (server) {
    target = `<div class="grid g2"><input type="text" id="bg-url" value="${esc(BG.url)}" placeholder="https://webdav.yandex.ru/jarvis-bg или http://192.168.1.10:8765/"><div class="row nw"><input type="text" id="bg-user" value="${esc(BG.user)}" placeholder="Логин" autocomplete="off"><input type="password" id="bg-pass" placeholder="${BG.pass_set ? "пароль сохранён" : "Пароль"}" autocomplete="new-password"></div></div>
      <div class="row"><button class="btn sm" id="bg-save">Сохранить</button><small class="muted">Яндекс Диск — адрес webdav.yandex.ru/папка и пароль приложения; компьютер — <code>python kws/bg_receiver.py</code></small><span class="spacer"></span><button class="btn sm ghost" id="bg-to-drive">Лучше Google Диск</button></div>`;
  } else if (g.signed_in) {
    target = `<div class="row"><span class="st ok">Google Диск подключён — папка «Jarvis — фон комнаты»</span><span class="spacer"></span><button class="btn sm ghost danger" id="gd-out">Отключить</button></div>`;
  } else if (g.code) {
    target = `<div class="gcode"><span class="gstep">1</span><a class="btn primary" href="${esc(g.url)}" target="_blank" rel="noopener">Открыть ${esc(g.url.replace("https://www.", "").replace("https://", ""))}</a>
      <span class="gstep">2</span><span>Введите код <b class="code-big">${esc(g.code)}</b> и разрешите доступ</span></div>
      <small class="muted">Колонка увидит только свою папку на Диске. Эта страница сама поймёт, что вы вошли.</small>`;
  } else if (!g.client_set) {
    target = `<p class="note">Google Диск не настроен в этой сборке. Можно использовать свой сервер или установить официальный релиз YaJavis с подключением Google Диска.</p>
      <div class="row"><button class="btn sm ghost" id="bg-to-server">Отправлять на свой сервер</button></div>`;
  } else {
    target = `${g.error ? `<p class="err">${esc(g.error)}</p>` : ""}<div class="row"><button class="btn primary" id="gd-in">Подключить Google Диск</button><small class="muted">откроется страница Google — войдёте и разрешите доступ</small><span class="spacer"></span><button class="btn sm ghost" id="bg-to-server">или свой сервер</button></div>`;
  }
  box.innerHTML = `${target}
    <div class="bg-run${ready ? "" : " off"}">${BG.running
      ? `<span class="rec-dot"></span><span><b>Пишу фон</b> · осталось ${hm(BG.left_min)} · отправлено ${BG.sent} мин (${BG.sent_mb.toFixed(0)} МБ)${BG.dropped ? ` · пропущено ${BG.dropped}` : ""}</span><span class="spacer"></span><button class="btn sm" id="bg-stop">Остановить</button>`
      : `<div class="seg" id="bg-hours">${[1, 2, 4, 8, 12].map((h) => `<button type="button" data-h="${h}" class="${h === bgHours ? "on" : ""}">${h} ч</button>`).join("")}</div><span class="spacer"></span><button class="btn primary" id="bg-start"${ready ? "" : " disabled"}>Записать ${bgHours} ч фона</button>`}</div>
    ${BG.error ? `<p class="err">${esc(BG.error)}</p>` : ""}`;
  const to = (t) => api("PUT", "/api/bgrec", { to: t }).then(drawBg).catch((e) => toast(e.message, true));
  $("#bg-to-server")?.addEventListener("click", () => { bgServer = true; to("server"); });
  $("#bg-to-drive")?.addEventListener("click", () => { bgServer = false; to("gdrive"); });
  $$("#bg-hours [data-h]").forEach((b) => b.addEventListener("click", () => { bgHours = +b.dataset.h; $$("#bg-hours button").forEach((x) => x.classList.toggle("on", x === b)); $("#bg-start").textContent = `Записать ${bgHours} ч фона`; }));
  $("#gd-in")?.addEventListener("click", (e) => act(e.target, async () => { await api("POST", "/api/gdrive/login"); drawBg(); }));
  $("#gd-out")?.addEventListener("click", (e) => { if (confirm("Отключить Google Диск?")) act(e.target, async () => { await api("DELETE", "/api/gdrive/login"); drawBg(); }); });
  $("#bg-save")?.addEventListener("click", (e) => act(e.target, async () => { const body = { to: "server", url: $("#bg-url").value, user: $("#bg-user").value }; if ($("#bg-pass").value) body.pass = $("#bg-pass").value; await api("PUT", "/api/bgrec", body); drawBg(); }, "Сохранено"));
  $("#bg-start")?.addEventListener("click", (e) => act(e.target, async () => { await api("POST", "/api/bgrec/start", { hours: bgHours }); drawBg(); }, "Запись фона началась"));
  $("#bg-stop")?.addEventListener("click", (e) => act(e.target, async () => { await api("POST", "/api/bgrec/stop"); drawBg(); }));
  clearTimeout(drawBg.t);
  if (BG.running || g.code) drawBg.t = setTimeout(() => { if (page === "training") drawBg(); }, g.code ? 3000 : 15000);
}
