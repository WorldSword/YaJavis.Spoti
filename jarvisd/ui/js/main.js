// Каркас: вход, состояние с колонки, опрос, сайдбар, маршруты, запуск.
import { viewProfile } from "./assistant.js";
import { viewCharacters } from "./characters.js";
import { CH, ST, chartsData, navPill, statsData, viewCharts } from "./charts.js";
import { connOpen, viewConnections } from "./connections.js";
import { $, $$, act, api, dirty, esc, ring, setDirty } from "./core.js";
import { homeData, viewHome } from "./home.js";
import { HL, LOGS, homeLogs, logsData, viewLogs } from "./logs.js";
import { drawVolume, musicData, viewPlayer } from "./player.js";
import { viewPlugins } from "./plugins.js";
import { viewSecurity } from "./security.js";
import { viewSpeaker } from "./speaker.js";
import { viewTraining } from "./training.js";
import { wizOpen } from "./wizard.js";

export let S = null;          // состояние с колонки
export let journal = [];
export let page = "home";
export let pollTimer = null;

// ---------------------------------------------------------------- вход
export let gateSetup = false;
export function showGate(setup) {
  gateSetup = !!setup;
  $("#app").classList.add("hidden");
  $("#gate").classList.remove("hidden");
  $("#gate-text").textContent = setup ? "Придумайте пароль для настроек колонки. Он понадобится, чтобы зайти сюда снова." : "Пароль от настроек колонки";
  $("#gate-pass2").classList.toggle("hidden", !setup);
  $("#gate-forgot").classList.toggle("hidden", setup);
  $("#gate-btn").textContent = setup ? "Сохранить пароль" : "Войти";
  $("#gate-pass").autocomplete = setup ? "new-password" : "current-password";
  $("#gate-pass").focus();
  clearInterval(pollTimer);
}

$("#gate-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const p = $("#gate-pass").value;
  $("#gate-err").textContent = "";
  try {
    if (gateSetup) {
      if (p !== $("#gate-pass2").value) throw new Error("Пароли не совпадают");
      await api("POST", "/api/setup", { password: p });
    } else {
      await api("POST", "/api/login", { password: p });
    }
    $("#gate-pass").value = $("#gate-pass2").value = "";
    $("#gate").classList.add("hidden");
    await start();
  } catch (err) { $("#gate").classList.remove("hidden"); $("#gate-err").textContent = err.message; }
});

$("#logout").addEventListener("click", async () => { await api("POST", "/api/logout").catch(() => {}); showGate(false); });
// меню на телефоне: шторка слева с затемнением; закрывается тапом мимо, Escape и переходом на страницу
export const scrim = document.createElement("div");
scrim.className = "scrim";
document.body.append(scrim);
export const setMenu = (open) => { $("#side").classList.toggle("open", open); document.body.classList.toggle("menu-open", open); };
$("#menu").addEventListener("click", () => setMenu(!$("#side").classList.contains("open")));
scrim.addEventListener("click", () => setMenu(false));
document.addEventListener("keydown", (e) => { if (e.key === "Escape" && $("#side").classList.contains("open")) setMenu(false); });

// ---------------------------------------------------------------- данные
export async function reload() {
  S = await api("GET", "/api/state");
  S.live_voices ||= []; S.prompts ||= []; S.profiles ||= []; S.security ||= [];
  S.gemini_models ||= []; S.gemini_voice_groups ||= []; S.chatgpt_voice_groups ||= []; S.grok_voice_groups ||= []; S.chatgpt ||= {}; S.sys ||= {};
  renderSide();
}

export async function start() {
  await reload();
  $("#app").classList.remove("hidden");
  $("#ver").textContent = "v" + S.version;
  route();
  if (!S.settings.onboarded) wizOpen(0);
  clearInterval(pollTimer);
  pollTimer = setInterval(poll, 2000);
  poll();
}

export async function poll() {
  let d;
  try { d = await api("GET", "/api/journal"); } catch { return; }
  if (d.version && S.version && d.version !== S.version) { location.reload(); return; }   // колонку обновили — берём новую панель
  journal = d.items || [];
  S.speaker.mic = d.mic; S.speaker.mic_why = d.mic_why || ""; S.speaker.talking = d.talking; S.speaker.phase = d.phase || ""; S.sys = d.sys || {};
  renderStatus();
  if (d.volume >= 0) S.speaker.volume = d.volume;
  if (page === "home") { homeData(); drawVolume(); if (Date.now() - ST.at > 15000) statsData(); if (Date.now() - HL.at > 5000) homeLogs(); }
  if (page === "player") drawVolume();
  if (page === "charts" && Date.now() - CH.at > 15000) chartsData(false);
  if (page === "logs" && LOGS.auto) logsData(false);
  musicData();
  clearTimeout(poll.fast);
  if (S.speaker.talking && page === "home") poll.fast = setTimeout(poll, 500);
}

// Что сейчас делает колонка: вид кольца, заголовок и пояснение.
export function speakerState() {
  const sp = S.speaker;
  if (sp.talking) return { ring: "talk", title: "Идёт разговор", text: "Говорите — пока разговор открыт, звать по имени не нужно" };
  if (!sp.mic) return { ring: "off", title: "Микрофон не подключён", text: sp.mic_why ? `Колонка не присылает звук: ${sp.mic_why}. Подробности — в «Журнале».` : "Колонка не присылает звук. Проверьте, что она включена и установка завершилась." };
  if (sp.wake_model && S.settings.wake_enabled) return { ring: "idle", title: "Жду слово «Джарвис»", text: "Скажите «Джарвис, …» или нажмите «Поговорить»" };
  return { ring: "mute", title: "Жду кнопку «Поговорить»", text: S.settings.wake_enabled ? "Модель слова «Джарвис» ещё не установлена" : "Отклик на слово «Джарвис» выключен" };
}

// Кольцо перерисовываем, только когда состояние сменилось, — иначе анимация дёргается на каждом опросе.
export function setRing(box, state, cls) {
  if (!box || box.dataset.state === state) return;
  box.dataset.state = state;
  box.innerHTML = ring(state, cls);
}

export const ORB_TEXT = { idle: "Колонка на связи — ждёт «Джарвис»", listening: "Слушает", thinking: "Думает", speaking: "Говорит", off: "Нет звука с колонки", mute: "На связи, но слово «Джарвис» выключено или нет модели" };
export function renderStatus() {
  const st = speakerState(), ph = S.speaker.phase;
  const o = st.ring === "off" ? "off" : S.speaker.talking ? (ph || "listening") : st.ring === "mute" ? "mute" : "idle";
  const orb = $("#orb");
  if (orb.dataset.s !== o) { orb.dataset.s = o; orb.setAttribute("aria-label", ORB_TEXT[o]); orb.title = ORB_TEXT[o]; }
}

// ---------------------------------------------------------------- сайдбар
export const avatar = (p, big = false) => `<span class="av c${p.color || 1}${big ? " big" : ""}">${esc((p.name || "?").trim()[0] || "?")}</span>`;
export const voiceName = (id) => (S.live_voices.find((v) => v.id === id)?.name) || id || "cedar";
export const engineName = (p) => ({ gemini: "Gemini Live", chatgpt: "GPT-Live по подписке", grok: "Grok Voice" })[p.engine] || "GPT-Live";

export function renderSide() {
  $("#upd-dot")?.classList.toggle("hidden", !S.update_available);
  $("#profiles").innerHTML = S.profiles.map((p) => `<div class="prof${page === "p:" + p.id ? " on" : ""}" draggable="true" data-id="${esc(p.id)}">
    ${avatar(p)}<div class="meta"><div class="n">${esc(p.name)}</div><div class="s">${engineName(p)} · ${esc(voiceName(p.voice))}</div></div>
    ${p.id === S.active ? `<span class="act" title="Сейчас отвечает"></span>` : ""}</div>`).join("");
  $$("#profiles .prof").forEach((el) => {
    el.addEventListener("click", () => go("#p/" + el.dataset.id));
    el.addEventListener("dragstart", (e) => { el.classList.add("drag"); e.dataTransfer.setData("text/plain", el.dataset.id); });
    el.addEventListener("dragend", () => el.classList.remove("drag"));
    el.addEventListener("dragover", (e) => e.preventDefault());
    el.addEventListener("drop", async (e) => {
      e.preventDefault();
      const from = e.dataTransfer.getData("text/plain"), to = el.dataset.id;
      if (from === to) return;
      const ids = S.profiles.map((p) => p.id).filter((id) => id !== from);
      ids.splice(ids.indexOf(to), 0, from);
      await act(null, async () => { await api("POST", "/api/profiles/order", { ids }); await reload(); });
    });
  });
  $("#nav-train")?.classList.toggle("hidden", !S.settings.dev_mode);
  $$(".nav a").forEach((a) => a.classList.toggle("on", a.dataset.page === page));
  navPill();
}

$("#add-profile").addEventListener("click", () => go("#p/new"));

// ---------------------------------------------------------------- маршруты
export function go(hash) { location.hash = hash; }
window.addEventListener("hashchange", () => {
  if (!S) return;   // ещё не вошли — страницы рисовать не из чего
  if (dirty && !confirm("Есть несохранённые изменения. Уйти без сохранения?")) { history.back(); return; }
  route();
});
window.addEventListener("beforeunload", (e) => { if (dirty) e.preventDefault(); });

export function route() {
  setDirty(false);
  $("#side").classList.remove("open");
  document.body.classList.remove("menu-open");
  const h = location.hash.slice(1) || "home";
  const v0 = $("#view"); v0.oninput = v0.onchange = v0.onclick = null;   // обработчики прошлой страницы
  if (h.startsWith("p/")) { page = "p:" + h.slice(2); viewProfile(h.slice(2)); }
  else if (h.startsWith("connections/")) { page = "connections"; viewConnections(); connOpen(h.slice(12)); }
  else {
    page = ["home", "player", "charts", "logs", "characters", "connections", "plugins", "speaker", "security", "training"].includes(h) ? h : "home";
    if (page === "training" && !S.settings.dev_mode) page = "security";
    ({ home: viewHome, player: viewPlayer, charts: viewCharts, logs: viewLogs, characters: viewCharacters, connections: viewConnections, plugins: viewPlugins, speaker: viewSpeaker, training: viewTraining, security: viewSecurity })[page]();
  }
  renderSide();
  window.scrollTo(0, 0);
  const v = $("#view");          // одно появление на переход между страницами
  v.classList.remove("enter"); void v.offsetWidth; v.classList.add("enter");
}

// ---------------------------------------------------------------- запуск
start().catch(() => {});
