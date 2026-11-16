// Колонка.
import { $, $$, api, esc, icon, jelly, sec, tip, toast } from "./core.js";
import { ledLabBind, ledLabHtml } from "./leds.js";
import { S, reload } from "./main.js";

// ---------------------------------------------------------------- колонка
export const TZ = [["Europe/Kaliningrad", "Калининград (UTC+2)"], ["Europe/Moscow", "Москва (UTC+3)"], ["Europe/Minsk", "Минск (UTC+3)"],
  ["Europe/Kyiv", "Киев"], ["Europe/Samara", "Самара (UTC+4)"], ["Asia/Yekaterinburg", "Екатеринбург (UTC+5)"],
  ["Asia/Almaty", "Алматы (UTC+5)"], ["Asia/Omsk", "Омск (UTC+6)"], ["Asia/Novosibirsk", "Новосибирск (UTC+7)"],
  ["Asia/Krasnoyarsk", "Красноярск (UTC+7)"], ["Asia/Irkutsk", "Иркутск (UTC+8)"], ["Asia/Yakutsk", "Якутск (UTC+9)"],
  ["Asia/Vladivostok", "Владивосток (UTC+10)"], ["Asia/Magadan", "Магадан (UTC+11)"], ["Asia/Kamchatka", "Камчатка (UTC+12)"],
  ["Europe/Berlin", "Берлин"], ["Europe/London", "Лондон"], ["UTC", "UTC"]];
export const HOME_USE = [
  ["alice", "Алиса", "Джарвис передаёт команду штатной Алисе — она умеет всё, что подключено к Яндексу", () => true],
  ["yandexmusic", "Яндекс Музыка", "«Включи музыку», «поставь песню…» Джарвис отдаёт Алисе — играет из Яндекс Музыки. Выключено — всё играет с YouTube", () => true],
  ["smartlife", "Smart Life", "Напрямую по домашней сети — быстрее всего", () => S.smartlife?.signed_in],
  ["xiaomi", "Xiaomi Home", "Через облако Xiaomi: пылесос, увлажнитель, лампы Mi Home", () => S.xiaomi?.signed_in],
  ["yandex", "Яндекс Умный дом", "Сам Джарвис через API Яндекса, без Алисы — нужен токен", () => !!S.keys?.yandex],
  ["homeassistant", "Home Assistant", "Ваш сервер Home Assistant", () => S.home_assistant?.configured],
];
export function homeUseRows() {
  const use = S.settings.home_use || {};
  return HOME_USE.map(([id, name, about, ready]) => { const on = ready(); return `<label class="sw hu${on ? "" : " off"}"><input type="checkbox" data-home="${id}"${on && use[id] !== false ? " checked" : ""}${on ? "" : " disabled"}><span class="tr"></span>
    <span class="hu-t"><b>${name}</b><small>${about}${on ? "" : ` · <a href="#connections">сначала подключите</a> — тогда галочка включится сама`}</small></span></label>`; }).join("");
}
export function viewSpeaker() {
  const s = S.settings, via = s.home_via === "jarvis" ? "jarvis" : "alice";
  $("#view").innerHTML = `<header class="ph"><h1>Колонка</h1><p class="sub">Изменения сохраняются сразу</p></header>
  ${sec("Умный дом и музыка", "Через что Джарвис может управлять домом и откуда включать музыку. Можно оставить несколько: быстрые команды сначала пробуют Smart Life по домашней сети, потом Алису, а сложные просьбы модель решает сама теми способами, что здесь включены",
    `<div class="home-use">${homeUseRows()}</div>`)}
  ${sec("Где вы", "Комната колонки — для команд без комнаты («выключи свет»). Город и пояс — для погоды, новостей и времени: часы колонки идут по Гринвичу",
    `<div class="f"><label for="s-room">Комната, где стоит колонка ${tip("«Выключи свет», «включи кондиционер» без названия комнаты — Джарвис сделает это здесь, как Алиса.")}</label>
      <input type="text" id="s-room" list="s-rooms" value="${esc(s.speaker_room || "")}" placeholder="Например, Зал" autocomplete="off">
      <datalist id="s-rooms">${(S.rooms || []).map((r) => `<option value="${esc(r)}">`).join("")}</datalist></div>
    <div class="grid g2"><div class="f"><label for="s-city">Город</label><input type="text" id="s-city" value="${esc(s.city || "")}" placeholder="Например, Москва"></div>
      <div class="f"><label for="s-tz">Часовой пояс</label><select id="s-tz">${TZ.map(([id, n]) => `<option value="${id}"${id === (s.timezone || "UTC") ? " selected" : ""}>${n}</option>`).join("")}</select></div></div>`)}
  ${sec("Слово «Джарвис»", "Его распознаёт сама колонка, без интернета",
    `<label class="sw"><input type="checkbox" id="s-wake"${s.wake_enabled ? " checked" : ""}><span class="tr"></span>Откликаться на «Джарвис» ${tip("Выключите, если Джарвис нужен только по кнопке «Поговорить». Алиса работает в любом случае.")}</label>
    <label class="sw"><input type="checkbox" id="s-check"${s.wake_check ? " checked" : ""}><span class="tr"></span>Проверять имя перед ответом ${tip("Выключено — как у Алисы: услышал слово, сразу загорается кольцо и Джарвис слушает. Включено — сначала облако проверяет, что прозвучало именно «Джарвис»: ложных разговоров меньше, но отклик на полсекунды-секунду позже.")}</label>
    <div class="f"><label>Чувствительность ${tip("Выше — слышит издалека и тихо, но чаще срабатывает зря (например, на разговор в Discord). Ниже — реже ошибается, но звать нужно чётче.")}<b id="s-sens-v" class="val">${s.wake_sensitivity}</b></label>
      <input type="range" id="s-sens" min="1" max="10" value="${s.wake_sensitivity}"><div class="scale"><span>осторожно</span><span>слышит всё</span></div></div>
    <div class="f"><label>Общее усиление микрофона ${tip("Для колонок, которые слышат вас только вблизи (так бывает на Станции Мини 2: её обработка отдаёт голос тихо). Усиливает звук для всего: слова «Джарвис», ИИ и записей для обучения. Прибавляйте по 6 дБ и проверяйте; слишком много — чаще ложные срабатывания. На Лайт обычно не нужно.")}<b id="s-gain-v" class="val">+${s.mic_gain_db || 0} дБ</b></label>
      <input type="range" id="s-gain" min="0" max="30" step="3" value="${s.mic_gain_db || 0}"><div class="scale"><span>как есть</span><span>+30 дБ</span></div></div>
    <div class="f"><label>Усиление микрофона для ИИ ${tip("Если модель плохо слышит и переспрашивает, хотя вы говорите не тихо (так бывает на Станции Лайт 2: её обработка отдаёт голос тише), прибавьте 6–12 дБ. Слово «Джарвис» это не меняет. Уровень голоса за каждый разговор пишется в журнал: «микрофон: за разговор голос …».")}<b id="s-mic-v" class="val">+${s.mic_boost_db || 0} дБ</b></label>
      <input type="range" id="s-mic" min="0" max="18" step="3" value="${s.mic_boost_db || 0}"><div class="scale"><span>как есть</span><span>+18 дБ</span></div></div>
    ${S.speaker.wake_model ? "" : `<p class="note">Модель слова ещё не установлена — пока Джарвиса можно позвать только кнопкой «Поговорить» на главной.</p>`}`)}
  ${sec("Язык", "На каком языке вы говорите с Джарвисом. Распознавание речи и ответы будут на нём — так он не «услышит» испанский там, где был русский",
    `<div class="seg" id="s-lang">${[["ru", "Русский"], ["en", "English"], ["uk", "Українська"], ["", "Определять самому"]].map(([k, n]) => `<button type="button" data-lang="${k}" class="${(s.language ?? "ru") === k ? "on" : ""}">${n}</button>`).join("")}</div>`)}
  ${sec(`<span class="moon">${icon("moon")}</span>Ночной режим`, "По расписанию Джарвис говорит тише и короче, кольцо светит слабее, а сама колонка — Алиса, музыка, YouTube — не играет громче заданного",
    `<label class="sw"><input type="checkbox" id="n-on"${s.night?.enabled ? " checked" : ""}><span class="tr"></span>Включать по расписанию</label>
    <div class="night-t"><div class="f"><label>С</label><input type="time" id="n-from" value="${esc(s.night?.from || "23:00")}"></div><div class="f"><label>До</label><input type="time" id="n-to" value="${esc(s.night?.to || "08:00")}"></div>${s.night_now ? `<span class="badge acc">сейчас ночь</span>` : ""}</div>
    <div class="night-sl"><div class="f"><label>Голос Джарвиса не громче <b class="val" id="n-voice-v"></b></label><div id="n-voice"></div></div>
      ${S.speaker.leds ? `<div class="f"><label>Кольцо не ярче <b class="val" id="n-bright-v"></b></label><div id="n-bright"></div></div>` : ""}
      <div class="f"><label>Колонка не громче ${tip("Громкость самой колонки по её шкале 0–10. Если ночью сделать громче, через полминуты станет обратно.")}<b class="val" id="n-speaker-v"></b></label><div id="n-speaker"></div></div></div>`)}
  ${sec("Подсветка", "Кольцо Джарвиса. Цвета у каждого ассистента свои — в его настройках. У Алисы подсветка своя и не меняется",
    S.speaker.leds ? `<label class="sw"><input type="checkbox" id="l-on"${s.led.enabled ? " checked" : ""}><span class="tr"></span>Подсвечивать кольцо, когда говорит Джарвис</label>
    ${ledLabHtml()}`
    : `<p class="note">У этой колонки нет подсветки, которой может управлять Джарвис.</p>`)}`;
  $("#view").oninput = $("#view").onchange = $("#view").onclick = null;
  const put = (body) => api("PUT", "/api/settings", body).then(reload).catch((e) => toast(e.message, true));
  $$("input[data-home]").forEach((b) => b.addEventListener("change", () => {
    const ch = b.dataset.home, on = b.checked, name = HOME_USE.find((x) => x[0] === ch)?.[1] || ch;
    put({ home_use: { [ch]: on } }).then(() => toast(`${name}: ${on ? "используется" : "не используется"}`));
  }));
  $("#s-wake").addEventListener("change", (e) => put({ wake_enabled: e.target.checked }).then(() => toast(e.target.checked ? "Джарвис снова слушает" : "Джарвис больше не откликается на слово")));
  $("#s-check").addEventListener("change", (e) => put({ wake_check: e.target.checked }).then(() => toast(e.target.checked ? "Сначала проверяю имя, потом отвечаю" : "Отклик сразу, как у Алисы")));
  $("#s-city").addEventListener("change", (e) => put({ city: e.target.value.trim() }).then(() => toast("Город сохранён")));
  $("#s-room").addEventListener("change", (e) => put({ speaker_room: e.target.value.trim() }).then(() => toast(e.target.value.trim() ? `Колонка стоит: ${e.target.value.trim()}` : "Комната колонки не задана")));
  $("#s-tz").addEventListener("change", (e) => put({ timezone: e.target.value }).then(() => toast("Часовой пояс сохранён — время в журнале тоже по нему")));
  $("#s-sens").addEventListener("input", (e) => ($("#s-sens-v").textContent = e.target.value));
  $("#s-sens").addEventListener("change", (e) => put({ wake_sensitivity: +e.target.value }).then(() => toast("Чувствительность сохранена")));
  $("#s-gain").addEventListener("input", (e) => { $("#s-gain-v").textContent = `+${e.target.value} дБ`; });
  $("#s-gain").addEventListener("change", (e) => put({ mic_gain_db: +e.target.value }).then(() => toast("Общее усиление микрофона сохранено")));
  $("#s-mic").addEventListener("input", (e) => { $("#s-mic-v").textContent = `+${e.target.value} дБ`; });
  $("#s-mic").addEventListener("change", (e) => put({ mic_boost_db: +e.target.value }).then(() => toast("Усиление микрофона сохранено")));
  const night = { enabled: false, from: "23:00", to: "08:00", voice: 20, bright: 10, speaker: 3, ...(s.night || {}) };
  const saveNight = () => put({ night }).then(() => toast(night.enabled ? `Ночной режим: ${night.from}–${night.to}` : "Ночной режим выключен"));
  $("#n-on").addEventListener("change", (e) => { night.enabled = e.target.checked; saveNight(); });
  $("#n-from").addEventListener("change", (e) => { if (e.target.value) { night.from = e.target.value; saveNight(); } });
  $("#n-to").addEventListener("change", (e) => { if (e.target.value) { night.to = e.target.value; saveNight(); } });
  const nsl = (key, label, min, max, step, fmt) => {
    const out = $(`#n-${key}-v`), show = (v) => { out.textContent = fmt(v); };
    if (!$(`#n-${key}`)) return;
    show(night[key]);
    jelly($(`#n-${key}`), { label, min, max, step, value: night[key], format: fmt, onInput: show, onChange: (v) => { show(v); night[key] = v; saveNight(); } });
  };
  nsl("voice", "Голос ночью", 5, 100, 5, (v) => v + "%");
  nsl("bright", "Кольцо ночью", 0, 100, 5, (v) => v + "%");
  nsl("speaker", "Колонка ночью", 1, 10, 1, (v) => `${v} из 10`);
  $$("#s-lang [data-lang]").forEach((b) => b.addEventListener("click", () => {
    $$("#s-lang button").forEach((x) => x.classList.toggle("on", x === b));
    put({ language: b.dataset.lang }).then(() => toast(b.dataset.lang ? `Язык: ${b.textContent}` : "Язык определяется сам"));
  }));
  if (S.speaker.leds) {
    ledLabBind();
    $("#l-on").addEventListener("change", (e) => put({ led: { enabled: e.target.checked } }).then(() => toast(e.target.checked ? "Подсветка включена" : "Подсветка выключена")));
  }
}
