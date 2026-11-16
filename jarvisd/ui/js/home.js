// Главная.
import { KPI, ST, drawKpis, kpiHtml, statsData } from "./charts.js";
import { $, act, api, esc, icon, tip, toast } from "./core.js";
import { FEED, feedFollow, feedReset, feedSync } from "./feed.js";
import { homeLogsSetup } from "./logs.js";
import { S, avatar, journal, poll, setRing, speakerState } from "./main.js";
import { M, drawPlayer, drawVolume, musicData, playerHtml, playerSetup } from "./player.js";

// ---------------------------------------------------------------- главная
export function setupSteps() {
  return [
    { done: !!S.keys.openai || !!S.keys.gemini || !!S.keys.grok || !!S.chatgpt.signed_in, title: "Ключ OpenAI, Gemini, Grok или вход в ChatGPT", text: "Без него Джарвис не говорит и не думает", href: "#connections" },
    { done: S.vless.set && !S.vless.error, title: "VPN (ссылка VLESS)", text: "OpenAI не работает напрямую из России и Беларуси", href: "#connections" },
    { done: !!S.keys.yandex || S.settings.home_via !== "jarvis", title: "Умный дом", text: "Через Алису работает сразу; для режима «Джарвис» нужен токен Яндекса", href: "#speaker" },
    { done: !!S.settings.city, title: "Ваш город", text: "Для погоды и новостей без уточнений", href: "#speaker" },
  ];
}

export function viewHome() {
  const p = S.profiles.find((x) => x.id === S.active) || S.profiles[0];
  const steps = setupSteps(), left = steps.filter((x) => !x.done), doneN = steps.length - left.length;
  $("#view").innerHTML = `<header class="ph ph-row"><div><h1>Главная</h1><p class="sub"><span class="dot" id="home-dot"></span><span id="home-sub"></span></p></div></header>
  <div class="kpis stagger" id="kpis">${KPI.map(kpiHtml).join("")}</div>
  <div class="home">
  <section class="tg">
    <header class="tg-h"><span class="tg-av"><span id="now-ring" class="ringbox"></span>${avatar(p)}</span>
      <div class="tg-who"><b>${esc(p.name)}</b><span id="tg-status" class="tg-st"></span></div>
      <a class="ib lg" href="#p/${esc(p.id)}" aria-label="Настроить ассистента">${icon("tune")}</a></header>
    <div class="tg-body" id="feed"></div>
    <form class="tg-f" id="say-form"><input type="text" id="say" maxlength="600" placeholder="Сообщение" autocomplete="off" aria-label="Сообщение Джарвису">
      <button class="ib lg" id="say-voice" type="button" data-tip="Читать ответ вслух на колонке" aria-label="Читать ответ вслух"></button>
      <button class="rb hidden" id="stop" type="button" data-tip="Оборвать разговор" aria-label="Стоп">${icon("stop", "solid")}</button>
      <button class="rb primary hidden" id="send" type="submit" aria-label="Отправить">${icon("send")}</button>
      <button class="rb primary" id="talk" type="button" data-tip="Колонка сразу начнёт слушать — как будто вы сказали «Джарвис»" aria-label="Поговорить">${icon("mic")}</button></form>
  </section>
  <aside class="rail">
    ${left.length ? `<div class="card"><h2>Осталось настроить <span class="count">${doneN} из ${steps.length}</span></h2>
      <progress class="bar" value="${doneN}" max="${steps.length}"></progress>
      ${steps.map((x) => `<a class="step${x.done ? " done" : ""}" href="${x.href}">${icon(x.done ? "done" : "todo")}<span class="sb"><b>${esc(x.title)}</b><small>${esc(x.text)}</small></span>${x.done ? "" : icon("right")}</a>`).join("")}</div>` : ""}
    <div class="card"><h2>Состояние</h2><dl class="stat">
      <div><dt>Микрофон ${tip("Звук с микрофонов колонки после шумо- и эхоподавления Яндекса.")}</dt><dd id="t-mic"></dd></div>
      <div><dt>Слово «Джарвис» ${tip("Распознаётся прямо на колонке маленькой нейросетью — без интернета и сразу.")}</dt><dd id="t-wake"></dd></div>
      <div><dt>Интернет ${tip("Как колонка ходит в OpenAI: через ваш VPN (VLESS) или напрямую.")}</dt><dd id="t-net"></dd></div>
      <div><dt>Свободно на диске ${tip("На флешке колонки мало места. Если свободно меньше 5 МБ, обновления могут не встать.")}</dt><dd id="t-disk"></dd></div>
    </dl></div>
    <section class="card github-support" id="github-support" aria-label="Поддержать проект">
      <button class="github-close" id="github-dismiss" type="button" aria-label="Больше не показывать просьбу поддержать проект" title="Больше не показывать">${icon("x")}</button>
      <span class="github-mark" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M9 19c-4.3 1.3-4.3-2.2-6-2.6M15 22v-3.9a3.4 3.4 0 0 0-.9-2.6c3-.3 6.2-1.5 6.2-6.9a5.4 5.4 0 0 0-1.5-3.7 5 5 0 0 0-.1-3.7S17.5.8 15 2.6a12.8 12.8 0 0 0-6 0C6.5.8 5.3 1.2 5.3 1.2a5 5 0 0 0-.1 3.7 5.4 5.4 0 0 0-1.5 3.7c0 5.4 3.2 6.6 6.2 6.9a3.4 3.4 0 0 0-.9 2.6V22"/></svg></span>
      <div class="github-copy"><h2>Поддержать YaJavis</h2><p>Нравится проект? Оставьте звезду на GitHub.</p>
        <a class="github-link" href="https://github.com/newfpv/YaJavis" target="_blank" rel="noopener noreferrer">Поставить звезду ${icon("ext")}</a>
      </div>
    </section>
    ${playerHtml()}
    <section class="card home-logs tight" id="hlogs"><div class="card-head"><h2>Логи</h2><a class="meta hl-all" href="#logs">Все логи ${icon("right")}</a></div><div class="logbox" id="hlogbox"></div></section>
  </aside>
  </div>`;
  feedReset($("#feed"));
  ST.at = 0; drawKpis(); statsData();
  playerSetup(); drawVolume(); if (M) drawPlayer(); musicData();
  homeLogsSetup();
  try { if (localStorage.getItem("yajavis.star.dismissed") === "1") $("#github-support")?.remove(); } catch {}
  $("#github-dismiss")?.addEventListener("click", () => {
    $("#github-support")?.remove();
    try { localStorage.setItem("yajavis.star.dismissed", "1"); } catch {}
  });
  $("#talk").addEventListener("click", (e) => act(e.currentTarget, () => api("POST", "/api/talk"), "Слушаю…"));
  $("#stop").addEventListener("click", (e) => act(e.currentTarget, () => api("POST", "/api/stop")));
  // написать текстом: пока в поле что-то есть, вместо микрофона — кнопка отправки
  const say = $("#say"), voiceBtn = $("#say-voice");
  const pref = { get: () => { try { return localStorage.getItem("jarvis.speak") !== "0"; } catch { return true; } }, set: (v) => { try { localStorage.setItem("jarvis.speak", v ? "1" : "0"); } catch { /* не страшно */ } } };
  let speak = pref.get();
  const drawVoice = () => { voiceBtn.innerHTML = icon(speak ? "voice" : "mute"); voiceBtn.classList.toggle("on", speak); voiceBtn.dataset.tip = speak ? "Ответ прозвучит на колонке. Нажмите, чтобы отвечал только текстом" : "Ответ только текстом. Нажмите, чтобы читал вслух на колонке"; };
  drawVoice();
  voiceBtn.addEventListener("click", () => { speak = !speak; pref.set(speak); drawVoice(); });
  const swap = () => { const typing = say.value.trim() !== ""; $("#send").classList.toggle("hidden", !typing); $("#talk").classList.toggle("hidden", typing); };
  say.addEventListener("input", swap);
  $("#say-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    const text = say.value.trim();
    if (!text) return;
    say.value = ""; swap();
    try { await api("POST", "/api/say", { text, speak }); } catch (err) { toast(err.message, true); say.value = text; swap(); return; }
    FEED.stick = true; feedFollow();
    poll(); setTimeout(poll, 700);
  });
  homeData();
}

// Служебные события разговора — в переписке идут строкой по центру, как в мессенджерах.
export const SERVICE = { "умный дом (быстро)": "Выполнил сам", "умный дом → Алиса": "Передал Алисе", "стоп": "Остановлен", "ложное срабатывание": "Показалось, что позвали" };
export const dayLabel = (d) => {
  const day = (x) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const diff = Math.round((day(new Date()) - day(d)) / 864e5);
  return diff === 0 ? "Сегодня" : diff === 1 ? "Вчера" : d.toLocaleDateString("ru-RU", { day: "numeric", month: "long" });
};

export function serviceText(it) {
  const base = SERVICE[it.route];
  if (it.route === "умный дом → Алиса" && it.answer) return it.answer.replace(/^передал/, "Передал");
  return it.answer && it.answer !== "Готово." ? base + " · " + it.answer : base;
}

export function homeData() {
  const feed = $("#feed");
  if (!feed) return;
  const st = speakerState();
  setRing($("#now-ring"), st.ring, "av");
  const status = $("#tg-status");
  status.textContent = S.speaker.talking ? "говорит и слушает…" : st.title.replace(/^Жду/, "ждёт").replace(/^Микрофон/, "микрофон");
  status.className = "tg-st" + (S.speaker.talking ? " live" : st.ring === "off" ? " warn" : "");
  $("#say").placeholder = S.speaker.talking ? "Идёт разговор — можно и написать" : "Сообщение";
  const sys = S.sys || {};
  drawKpis();
  const up = sys.uptime_s || 0, d = Math.floor(up / 86400), h = Math.floor((up % 86400) / 3600), m = Math.floor((up % 3600) / 60);
  const hd = "dot " + (S.speaker.mic ? "on" : "off");
  if ($("#home-dot").className !== hd) $("#home-dot").className = hd;
  $("#home-sub").textContent = `${S.speaker.model || "Яндекс Станция"} · Джарвис ${S.version}${up ? ` · колонка работает ${d ? `${d} д ${h} ч` : h ? `${h} ч ${m} мин` : `${m} мин`}` : ""}`;
  const disk = $("#t-disk");
  disk.textContent = sys.disk_total_mb ? `${sys.disk_free_mb} из ${sys.disk_total_mb} МБ` : "—";
  disk.className = "v " + (sys.disk_total_mb && sys.disk_free_mb < 5 ? "warn" : "ok");
  $("#stop").classList.toggle("hidden", !S.speaker.talking);
  const set = (id, text, cls) => { const el = $(id); el.textContent = text; el.className = "v " + cls; };
  set("#t-mic", S.speaker.mic ? "на связи" : "нет звука", S.speaker.mic ? "ok" : "bad");
  set("#t-wake", !S.settings.wake_enabled ? "выключено" : S.speaker.wake_model ? "слушаю" : "нет модели", !S.settings.wake_enabled ? "warn" : S.speaker.wake_model ? "ok" : "warn");
  set("#t-net", S.vless.error ? "ошибка VPN" : S.vless.set ? "через VPN" : "напрямую", S.vless.error ? "bad" : S.vless.set ? "ok" : "warn");

  const items = journal.filter((it) => it.route !== "ложное срабатывание" || it.text).slice(0, 40).reverse();   // старые сверху
  const time = (ts) => new Date(ts * 1000).toLocaleTimeString("ru-RU", { hour: "2-digit", minute: "2-digit" });
  const rows = [];
  let day = "";
  for (const it of items) {
    const d = dayLabel(new Date(it.ts * 1000));
    if (d !== day) { day = d; rows.push({ key: "d:" + d, cls: "svc day", html: d }); }
    const service = SERVICE[it.route], tm = time(it.ts);
    const answered = !!(it.answer || it.error || service);
    if (it.text) rows.push({ key: "me:" + it.ts, cls: "b me", html: `<span class="t">${esc(it.text)}</span><span class="m">${tm}${icon(answered ? "read" : "sent")}</span>` });
    else if (it.live && !answered) rows.push({ key: "me:" + it.ts, cls: "b me live", html: `<span class="wave">${"<i></i>".repeat(7)}</span><span class="t">Слушаю</span>`, label: "Говорите" });
    if (service) rows.push({ key: "svc:" + it.ts, cls: "svc", html: esc(serviceText(it)) });
    else if (it.answer) rows.push({ key: "him:" + it.ts, cls: "b him ans", answer: it.answer,
      meta: `${typeof it.audio_s === "number" && it.audio_s > 0 ? `ответ через ${it.audio_s.toFixed(1)} с · ` : ""}${tm}` });
    else if (it.live && !answered && it.text) {
      if (S.speaker.phase === "speaking") rows.push({ key: "him:" + it.ts, cls: "b him speak", html: `<span class="eqw">${"<i></i>".repeat(5)}</span><span class="t">Отвечает</span>`, label: "Джарвис отвечает" });
      else rows.push({ key: "him:" + it.ts, cls: "b him think", html: `<span class="think-t">Думаю</span>`, label: "Джарвис думает" });
    }
    if (it.error) rows.push({ key: "err:" + it.ts, cls: "b him err", html: `<span class="t">${esc(it.error)}</span><span class="m">${tm}</span>` });
  }
  if (!rows.length) rows.push({ key: "empty", cls: "tg-empty", html: `<b>Здесь будет переписка</b><span>Скажите «Джарвис, какая погода?» — вопрос и ответ появятся тут</span>` });
  feedSync(feed, rows);
}
