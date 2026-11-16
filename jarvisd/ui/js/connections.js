// Подключения и память Джарвиса.
import { $, $$, act, api, esc, icon, plural, tip, toast } from "./core.js";
import { S, page, reload } from "./main.js";

// ---------------------------------------------------------------- характеры
// ---------------------------------------------------------------- подключения
// Подключения — плитками, как «Характеры»: на плитке назначение и состояние, всё остальное — в окне по клику.
// Новое подключение = ещё одна запись в connDefs().
export const CONN = { open: "", net: {}, haEntities: [] };

export function haRoomRow(r = {}) {
  const options = (filter, value) => `<option value="">Выберите…</option>` +
    [...new Map([...(value ? [{entity_id:value,attributes:{friendly_name:value}}] : []), ...CONN.haEntities.filter(filter)].map(e => [e.entity_id,e])).values()]
      .map(e => `<option value="${esc(e.entity_id)}"${e.entity_id===value?" selected":""}>${esc(e.attributes?.friendly_name || e.entity_id)}</option>`).join("");
  return `<div class="card" data-ha-room data-ha-binding="${esc(JSON.stringify(r))}"><div class="f"><label>Название комнаты</label><input class="ha-name" value="${esc(r.name || "")}" placeholder="Кухня"></div>
    <div class="f"><label>Пылесос</label><select class="ha-vacuum">${options(e=>e.entity_id.startsWith("vacuum."),r.vacuum)}</select></div>
    <div class="f"><label>Сценарий или действие уборки этой комнаты</label><select class="ha-entity">${options(e=>/^(script|notify|text)\./.test(e.entity_id),r.entity)}</select></div>
    <div class="f"><label>Номера зон на карте — только для действия Xiaomi</label><input class="ha-ids" value="${esc(r.room_ids || "")}" placeholder="Например, 3 или 3,9"></div>
    <button class="btn ghost" data-ha-remove>Убрать комнату</button></div>`;
}
export const NET_NAMES = { openai: "OpenAI", gemini: "Gemini", grok: "Grok", elevenlabs: "ElevenLabs", youtube: "YouTube" };
export const VPN_NAMES = [["chatgpt", "Подписка ChatGPT"], ["openai", "OpenAI"], ["gemini", "Google Gemini"], ["grok", "xAI Grok"],
  ["elevenlabs", "ElevenLabs"], ["youtube", "YouTube"], ["smartlife", "Smart Life"], ["xiaomi", "Xiaomi Home"], ["gdrive", "Google Диск"], ["yandex", "Яндекс"], ["plugins", "Плагины"]];
export function smartLifeConn() {
  const t = S.smartlife || {}, lg = t.login || {}, devs = t.devices || [];
  const local = devs.filter((d) => d.local).length;
  const list = devs.length ? `<div class="xm-devs">${devs.map((d) => `<div class="xm-dev sl-dev${d.online ? "" : " off"}"><span class="sl-way ${d.local ? "lan" : "cloud"}" data-tip="${d.local ? "По домашней сети — мгновенно" : "Через облако Tuya — около полсекунды. Устройства Zigbee (через шлюз) всегда идут так"}">${icon(d.local ? "wifi" : "cloud")}</span><b>${esc(d.name)}</b><small>${esc([d.room, d.product].filter(Boolean).join(" · "))}${d.online ? "" : " · не в сети"}</small></div>`).join("")}</div>
      <p class="note">${local ? `По домашней сети, мгновенно: ${local} из ${devs.length}. Остальные — через облако Tuya.` : "Устройства в домашней сети ещё не откликнулись — пока команды идут через облако Tuya. Обычно они находятся за минуту."}</p>`
    : `<p class="note">Устройств не нашлось. Если они есть в Smart Life — нажмите «Обновить устройства».</p>`;
  return { id: "smartlife", group: "home", mono: "SL", name: "Smart Life", about: "Лампы, розетки и другие устройства Tuya — напрямую, без Алисы",
    more: "«Включи/выключи …» выполняется сразу по домашней сети, за доли секунды",
    on: !!t.signed_in, wait: !t.signed_in && !!lg.code, state: t.signed_in ? `${devs.length} ${plural(devs.length, "устройство", "устройства", "устройств")}` : lg.code ? "ждёт QR-код" : "не подключено",
    body: t.signed_in ? `<div class="cn-ok">${icon("done")}<span>Колонка вошла в Smart Life</span></div>${list}
        <div class="row"><button class="btn" id="sl-refresh">Обновить устройства</button></div>${vpnRow("smartlife")}`
      : lg.code ? `<div class="xm-qr"><img src="/api/smartlife/qr?t=${lg.at || 0}" alt="QR-код для входа в Smart Life" width="220" height="220">
          <ol class="how cn-steps"><li>Откройте Smart Life → значок сканера в правом верхнем углу.</li><li>Отсканируйте код и подтвердите вход.</li><li>Это окно само увидит вход и найдёт устройства.</li></ol></div>`
      : `${lg.error ? `<p class="err">${esc(lg.error)}</p>` : ""}
        <div class="f"><label for="sl-code">Код пользователя ${tip("В Smart Life: «Я» → шестерёнка «Настройки» → «Аккаунт и безопасность» → «Код пользователя». Это не пароль.")}</label>
          <div class="row"><input type="text" id="sl-code" placeholder="Например, AbC123x" autocomplete="off" spellcheck="false"><button class="btn primary" id="sl-in">Получить QR-код</button></div></div>
        <p class="note">Код пользователя — в Smart Life: «Я» → «Настройки» → «Аккаунт и безопасность». Потом отсканируете QR в самом приложении. Пароль колонка не видит.</p>${vpnRow("smartlife")}`,
    drop: t.signed_in ? `<button class="btn ghost danger" id="sl-out">Выйти</button>` : "" };
}
export function xiaomiConn() {
  const x = S.xiaomi || {}, lg = x.login || {}, devs = x.devices || [];
  const n = devs.length, word = n % 10 === 1 && n % 100 !== 11 ? "устройство" : [2, 3, 4].includes(n % 10) && ![12, 13, 14].includes(n % 100) ? "устройства" : "устройств";
  const list = devs.length ? `<div class="xm-devs">${devs.map((d) => `<div class="xm-dev${d.online ? "" : " off"}"><b>${esc(d.name)}</b><small>${esc([d.room, d.type || d.model, d.region && "сервер " + d.region].filter(Boolean).join(" · "))}${d.online ? "" : " · не в сети"}</small></div>`).join("")}</div>`
    : `<p class="note">Устройств не нашлось. Если они есть в Mi Home — нажмите «Обновить устройства».</p>`;
  return { id: "xiaomi", group: "home", mono: "Mi", name: "Xiaomi Home", about: "Лампы, розетки, пылесосы и другие устройства из приложения Mi Home",
    more: "Джарвис управляет ими голосом: «включи лампу в спальне», «запусти пылесос»",
    on: !!x.signed_in, wait: !x.signed_in && !!lg.code, state: x.signed_in ? `${n} ${word}` : lg.code ? "ждёт QR-код" : "не подключено",
    body: x.signed_in ? `<div class="cn-ok">${icon("done")}<span>Колонка вошла в Xiaomi Home${x.region ? `<small>${x.region.includes(",") ? "серверы" : "сервер"} ${esc(x.region)}</small>` : ""}</span></div>${list}
        <div class="row"><button class="btn" id="xm-refresh">Обновить устройства</button></div>${vpnRow("xiaomi")}`
      : lg.code ? `<div class="xm-qr"><img src="/api/xiaomi/qr?t=${lg.at || 0}" alt="QR-код для входа в Xiaomi" width="220" height="220">
          <ol class="how cn-steps"><li>Отсканируйте код камерой телефона или в приложении Mi Home: <b>+</b> → <b>Сканировать</b>.</li>
          <li>Войдите в свой аккаунт Xiaomi и подтвердите вход.</li><li>Это окно само увидит вход и найдёт устройства.</li></ol></div>
          ${lg.url ? `<p class="note">Открыли эту страницу на телефоне? <a href="${esc(lg.url)}" target="_blank" rel="noopener">Войти по ссылке</a></p>` : ""}`
      : `${lg.error ? `<p class="err">${esc(lg.error)}</p>` : ""}<div class="row"><button class="btn primary" id="xm-in">Войти по QR-коду</button></div>
          <p class="note">Пароль колонка не видит: вы подтверждаете вход на телефоне. Устройства берутся из того же аккаунта, что и в Mi Home.</p>${vpnRow("xiaomi")}`,
    drop: x.signed_in ? `<button class="btn ghost danger" id="xm-out">Выйти</button>` : "" };
}
// галочка «через VPN» у подключения
export function vpnRow(svc) {
  const on = !!S.vpn?.[svc], set = !!S.vless?.set;
  return `<div class="cn-vpn"><label class="sw"><input type="checkbox" data-vpn="${svc}"${on ? " checked" : ""}><span class="tr"></span>Через VPN</label>
    <small class="muted">${!set ? "VPN не задан — галочка заработает, когда добавите его в «Сеть → VPN»" : on ? "идёт через ваш VPN" : "напрямую, без VPN"}</small></div>`;
}
export function geminiKeysHtml() {
  const list = S.gemini_keys || [];
  if (list.length < 2) return list.length && list[0].spent ? `<p class="note">Лимит кончился у ${list[0].spent} из ${list[0].models} моделей этого ключа. Добавьте ещё ключ — Джарвис перейдёт на него сам.</p>` : "";
  return `<div class="ek-list">${list.map((k, i) => `<div class="ek${!k.spent && !list.slice(0, i).some((x) => x.spent < x.models) ? " on" : ""}${k.spent >= k.models ? " rest" : ""}"><b>${i + 1}</b><code>${esc(k.tail)}</code>
      <span class="ek-st">${k.spent >= k.models ? "лимит кончился · ждёт" : k.spent ? `лимит кончился у ${k.spent} из ${k.models} моделей` : "лимит есть"}</span><span class="spacer"></span>
      <button class="ib" data-gk-remove="${i}" aria-label="Убрать ключ ${i + 1}">${icon("x")}</button></div>`).join("")}</div>
    <p class="note">У каждого ключа свой бесплатный лимит. Кончился у всех моделей первого — Джарвис сам переходит на следующий.</p>`;
}
export function elevenKeysHtml() {
  const list = S.eleven_keys || [];
  if (!list.length) return "";
  const date = (u) => new Date(u * 1000).toLocaleDateString("ru-RU", { day: "numeric", month: "long" });
  return `<div class="ek-list">${list.map((k, i) => `<div class="ek${k.active ? " on" : ""}${k.until ? " rest" : ""}"><b>${i + 1}</b><code>${esc(k.tail)}</code>
      <span class="ek-st">${k.until ? `квота кончилась · ждёт до ${date(k.until)}` : k.active ? "сейчас работает" : "в запасе"}</span><span class="spacer"></span>
      ${k.until ? `<button class="btn sm ghost" data-ek-reset="${i}" data-tip="Например, если докупили символы">Вернуть</button>` : ""}
      <button class="ib" data-ek-remove="${i}" aria-label="Убрать ключ ${i + 1}">${icon("x")}</button></div>`).join("")}</div>
    <p class="note">Работает первый ключ с квотой. Кончилась — ключ откладывается до сброса лимита (обычно через месяц), и Джарвис сам берёт следующий.</p>`;
}

// memoryConn — карточка общей памяти: галочка, кто делает конспект, архив, конспект, «Начать новый чат», «Стереть память».
export const MEM_PROV = [["chatgpt", "Подписка ChatGPT"], ["gemini", "Google Gemini — ключ"], ["openai", "OpenAI — ключ, платно"]];
export function memoryConn(m, ms) {
  const day = (u) => new Date(u * 1000).toLocaleDateString("ru-RU", { day: "numeric", month: "long" });
  const at = (u) => new Date(u * 1000).toLocaleString("ru-RU", { day: "numeric", month: "long", hour: "2-digit", minute: "2-digit" });
  const n = m.archive || 0, turns = m.turns || 0, has = ms.has || {}, off = !!ms.off;
  const pname = (id) => MEM_PROV.find(([x]) => x === id)?.[1] || id;
  const mname = (x) => x || "по очереди: кончился лимит — следующая";
  const prov = ms.provider || "", mprov = has[prov] ? prov : ms.active;   // модели — у выбранного или у того, кто работает сейчас
  const who = ms.active ? `Сейчас конспект делает: <b>${esc(pname(ms.active))}</b> · ${esc(mname(ms.active_model))}.` : "";
  const pick = off ? "" : `<div class="row">
      <div class="f half"><label for="mem-prov">Кто делает конспект ${tip("«Сам выбирает» берёт сначала бесплатное: подписку ChatGPT, потом Gemini, потом платный ключ OpenAI. Если один не ответил — следующий. Конспект — короткий текстовый запрос раз в несколько десятков реплик.")}</label>
        <select id="mem-prov"><option value=""${prov ? "" : " selected"}>Сам выбирает</option>${MEM_PROV.map(([id, nm]) => `<option value="${id}"${prov === id ? " selected" : ""}${has[id] ? "" : " disabled"}>${esc(nm)}${has[id] ? "" : " — не подключено"}</option>`).join("")}<option value="none"${prov === "none" ? " selected" : ""}>Не делать конспект</option></select></div>
      ${mprov && prov !== "none" ? `<div class="f half"><label for="mem-model">Модель</label><select id="mem-model">${(ms.models?.[mprov] || []).map((x) => `<option value="${esc(x)}"${x === (ms.active_model ?? "") ? " selected" : ""}>${esc(mname(x))}</option>`).join("")}</select></div>` : ""}
    </div>
    ${prov === "none" ? `<p class="note">Конспект не делается: Джарвис видит только последние реплики, а старое находит в архиве по «вспомни».</p>`
      : ms.active ? `<p class="note">${who}${prov && !has[prov] ? ` Выбранное не подключено — пока работает автовыбор.` : ""}</p>`
      : `<p class="err">Для конспекта нужна текстовая модель: подписка ChatGPT, ключ Gemini или ключ OpenAI. Пока её нет, старое не сжимается, но архив для «вспомни» пишется.</p>`}`;
  return { id: "memory", group: "voice", mono: "Пм", name: "Память Джарвиса",
    about: "Джарвис помнит вас: досье, конспект разговоров и архив реплик — общие для всех ассистентов",
    on: !off && n > 0, state: off ? "выключена" : n ? `${n} ${plural(n, "реплика", "реплики", "реплик")} в архиве` : "пусто",
    body: `<label class="sw"><input type="checkbox" id="mem-on"${off ? "" : " checked"}><span class="tr"></span>Запоминать разговоры ${tip("Выключите — и каждый разговор начнётся с чистого листа: Джарвис не получит ни конспекта, ни прошлых реплик, а архив не будет пополняться. То, что уже записано, останется.")}</label>
      ${off ? `<p class="note">Память выключена: каждый разговор — с чистого листа. Архив не пополняется, но и не стирается.</p>`
        : `<p class="note">В начале разговора Джарвис получает досье о вас, конспект недавних разговоров и последние реплики дословно, с датой и временем.
        Когда разговор становится длинным, в паузе старые реплики сжимаются: устойчивое о вас — в досье, остальное — в конспект, кто что сказал и когда.
        Последние ${5000} реплик хранятся дословно, а что старше — сжимается в летопись по месяцам, так что ничего не пропадает.
        Говорить «вспомни» не нужно: на «тот рецепт» или «как я рассказывал» он сам поищет в архиве и летописи.
        Попросите «забудь, что…» или «меня зовут не Сергей, а Павел» — он сотрёт это и перепишет память.</p>`}
      ${pick}
      <p class="note">${n ? `В архиве ${n} ${plural(n, "реплика", "реплики", "реплик")}${m.archive_since ? ` с ${day(m.archive_since)}` : ""}. В текущем чате — ${turns}.` : "Архив пока пуст."}</p>
      ${m.profile ? `<details open><summary>Что Джарвис знает о вас</summary><div class="mem-sum">${esc(m.profile)}</div></details>`
        : n && !off ? `<p class="note">Досье о вас пока пустое: оно заполняется, когда разговор сжимается (примерно каждые 20–30 реплик). Нажмите «Обновить досье сейчас», чтобы не ждать.</p>` : ""}
      ${m.summary ? `<details><summary>Конспект недавних разговоров${m.summary_at ? ` · обновлён ${at(m.summary_at)}` : ""}</summary><div class="mem-sum">${esc(m.summary)}</div></details>` : ""}
      ${(m.chronicle || []).length ? `<details><summary>Летопись · ${m.chronicle.length} ${plural(m.chronicle.length, "месяц", "месяца", "месяцев")}</summary>${m.chronicle.slice().reverse().map((c) => `<h4 class="mem-m">${esc(c.label)} <small>${c.turns} ${plural(c.turns, "реплика", "реплики", "реплик")}</small></h4><div class="mem-sum">${esc(c.text)}</div>`).join("")}</details>` : ""}
      ${m.recent ? `<details><summary>Последние реплики из архива</summary><div class="mem-sum">${esc(m.recent)}</div></details>` : ""}
      <div class="row">${!off && ms.active && n ? `<button class="btn" id="mem-compact" data-tip="Сжать разговор в досье и конспект сразу, не дожидаясь длинного разговора">Обновить досье сейчас</button>` : ""}<button class="btn" id="cg-newchat"${turns || m.summary ? "" : " disabled"} data-tip="Досье, летопись и архив останутся">Начать новый чат</button></div>`,
    drop: n ? `<button class="btn ghost danger" id="mem-erase">Стереть память</button>` : "" };
}

// memChange — галочка и выбор модели памяти (select не ловится кликом — слушаем change).
export async function memChange(e) {
  if (page !== "connections") return;
  const t = e.target;
  if (!["mem-on", "mem-prov", "mem-model"].includes(t.id)) return;
  const ms = S.chatgpt?.memory || {};
  const next = { off: !!ms.off, provider: ms.provider || "", model: ms.model || "" };
  if (t.id === "mem-on") next.off = !t.checked;
  if (t.id === "mem-prov") { next.provider = t.value; next.model = ""; }
  if (t.id === "mem-model") next.model = t.value;
  try {
    await api("PUT", "/api/settings", { memory: next });
    await reload(); drawConns();
    toast(t.id === "mem-on" ? (next.off ? "Память выключена — каждый разговор с чистого листа" : "Джарвис снова запоминает разговоры") : "Сохранено: кто делает конспект");
  } catch (err) { toast(err.message, true); await reload(); drawConns(); }
}

export function connDefs() {
  const k = S.keys, v = S.vless, cg = S.chatgpt, lg = cg.login || {}, yt = S.youtube || {}, ya = yt.account || {}, sp = S.spotify || {};
  const keyBody = (id, place, how, label) => `<div class="f"><label for="k-${id}">${label ? label[0] : k[id] ? "Новый ключ" : "Ключ"}</label><div class="row"><input type="password" id="k-${id}" placeholder="${place}" autocomplete="off"><button class="btn primary" data-key="${id}">${label ? label[1] : k[id] ? "Заменить" : "Сохранить"}</button></div></div>
    ${how ? `<details${k[id] ? "" : " open"}><summary>Где его взять</summary><ol class="how">${how}</ol></details>` : ""}`;
  const key = (id, o) => ({ id, group: o.group, mono: o.mono, name: o.name, about: o.about, more: o.more,
    on: !!k[id], need: !k[id] && !o.optional, state: k[id] ? `ключ ${esc(String(k[id]).replace(/^задан,?\s*/, ""))}` : o.optional ? "не подключено" : "нужен ключ",
    body: (o.top || "") + keyBody(id, o.place, o.how, o.label) + (o.vpn ? vpnRow(o.vpn) : ""), drop: k[id] ? `<button class="btn ghost danger" data-clear="${id}">${o.dropText || "Убрать ключ"}</button>` : "" });
  return [
    { id:"homeassistant", group:"home", mono:"HA", name:"Home Assistant", about:"Устройства дома, Xiaomi Home и уборка выбранных комнат",
      on:!!S.home_assistant?.configured, state:S.home_assistant?.configured?"подключено":"не подключено",
      body:`<p class="note">Подключите свой сервер Home Assistant. Аккаунт Xiaomi, регион и устройства выбираются в самом Home Assistant.</p>
        <div class="f"><label for="ha-url">Адрес сервера</label><input id="ha-url" type="url" value="${esc(S.home_assistant?.url || "")}" placeholder="http://192.168.1.10:8123"></div>
        <div class="f"><label for="ha-token">Долгосрочный токен из профиля Home Assistant</label><input id="ha-token" type="password" autocomplete="off" placeholder="${S.home_assistant?.configured?"Оставьте пустым, чтобы сохранить текущий":"Вставьте токен"}"></div>
        <div class="row"><button class="btn primary" id="ha-save">Проверить и сохранить</button>${S.home_assistant?.configured?`<button class="btn" id="ha-load">Загрузить устройства</button><a href="${esc(S.home_assistant.url)}/config/integrations/dashboard" target="_blank" rel="noopener">Открыть Home Assistant</a>`:""}</div>
        <p class="note">Для Xiaomi добавьте интеграцию Xiaomi Home и войдите в свой аккаунт. Название комнаты в приложении и номер зоны на карте пылесоса могут различаться.</p>
        <h3>Уборка по комнатам</h3><p class="note">Выберите готовый сценарий уборки либо действие Xiaomi с номерами зон. Без соответствия Джарвис попросит настроить комнату.</p>
        <div id="ha-rooms">${(S.home_assistant?.rooms || []).map(haRoomRow).join("")}</div><div class="row"><button class="btn" id="ha-discover-rooms">Найти комнаты пылесоса</button><button class="btn" id="ha-add-room">Добавить комнату</button></div>`,
      drop:S.home_assistant?.configured?`<button class="btn ghost danger" id="ha-disconnect">Отключить</button>`:"" },
    { id: "chatgpt", group: "voice", mono: "GP", name: "Подписка ChatGPT", about: "Голос Джарвиса за счёт подписки Plus или Pro — без оплаты API за минуты",
      more: "Включается у ассистента: движок «GPT-Live по подписке».",
      on: !!cg.signed_in, state: cg.signed_in ? esc(["вход выполнен", cg.plan].filter(Boolean).join(" · ")) : lg.code ? "ждёт код" : "не подключено",
      wait: !cg.signed_in && !!lg.code,
      body: cg.signed_in ? `<div class="cn-ok">${icon("done")}<span>Колонка вошла в ChatGPT${cg.plan ? ` · <b>${esc(cg.plan)}</b>` : ""}${cg.email ? `<small>${esc(cg.email)}</small>` : ""}</span></div>
          ${cg.limited_until * 1000 > Date.now() ? `<p class="err">Кончился лимит голоса в подписке. До ${new Date(cg.limited_until * 1000).toLocaleTimeString("ru-RU", { hour: "2-digit", minute: "2-digit" })} Джарвис говорит через Gemini${S.keys?.gemini ? "" : " — но ключа Gemini нет, добавьте его ниже"}.</p>` : ""}
          <p class="note">Все звонки — один разговор с общей памятью, она описана в карточке «Память Джарвиса».</p>`
        : lg.code ? `<ol class="how cn-steps"><li>Откройте <a href="${esc(lg.url)}" target="_blank" rel="noopener">${esc(lg.url.replace("https://", ""))}</a> (из России и Беларуси — через VPN) и войдите в свой ChatGPT.</li><li>Введите код: <b class="code-big">${esc(lg.code)}</b></li><li>Это окно само увидит вход — ничего нажимать не нужно.</li></ol>`
        : `${lg.error ? `<p class="err">${esc(lg.error)}</p>` : ""}<div class="row"><button class="btn primary" id="cg-in">Войти в ChatGPT</button></div>
          <p class="note">Колонка получит свой вход, пароль она не видит. Способ неофициальный, как у Codex: OpenAI может его изменить.</p>`
        + vpnRow("chatgpt"),
      drop: cg.signed_in ? `<button class="btn ghost danger" id="cg-out">Выйти</button>` : "" },
    memoryConn(cg.chat || {}, cg.memory || {}),
    key("openai", { vpn: "openai", group: "voice", mono: "AI", name: "OpenAI", place: "sk-…", about: "Голос и «мозг» Джарвиса. Живой разговор — примерно 5 центов в минуту",
      how: "<li>Откройте platform.openai.com и войдите.</li><li>Слева — API keys → Create new secret key.</li><li>Скопируйте ключ (начинается с sk-) и вставьте сюда.</li><li>Пополните баланс в разделе Billing — от 5 $.</li>" }),
    key("gemini", { vpn: "gemini", top: geminiKeysHtml(), label: k.gemini ? ["Ещё ключ — в запас", "Добавить"] : null,
      dropText: (S.gemini_keys || []).length > 1 ? "Убрать все ключи" : "Убрать ключ", group: "voice", mono: "Ge", name: "Google Gemini", place: "AIza…", optional: true, about: "Gemini Live — примерно вдвое дешевле GPT-Live, есть бесплатный уровень. Можно добавить несколько ключей: у каждого свой бесплатный лимит",
      more: "Какой движок отвечает, выбирается у ассистента.",
      how: "<li>Откройте aistudio.google.com и войдите в аккаунт Google (из России и Беларуси — через VPN).</li><li>Нажмите Get API key → Create API key.</li><li>Скопируйте ключ и вставьте сюда.</li>" }),
    key("grok", { vpn: "grok", group: "voice", mono: "xA", name: "xAI Grok", place: "xai-…", optional: true, about: "Grok Voice — сам ищет в интернете",
      more: "Какой движок отвечает, выбирается у ассистента.",
      how: "<li>Откройте console.x.ai и войдите.</li><li>Пополните баланс (Billing → Credits) — без кредитов ключ не работает.</li><li>API Keys → Create API key, скопируйте ключ (начинается с xai-) и вставьте сюда.</li>" }),
    key("elevenlabs", { vpn: "elevenlabs", top: elevenKeysHtml(), label: k.elevenlabs ? ["Ещё ключ — в запас", "Добавить"] : null,
      dropText: (S.eleven_keys || []).length > 1 ? "Убрать все ключи" : "Убрать ключ", group: "voice", mono: "11", name: "ElevenLabs", place: "sk_…", optional: true, about: "Распознавание речи и голоса классического режима. Выбирается в настройках ассистента → Мозг" }),
    key("yandex", { vpn: "yandex", group: "home", mono: "Я", name: "Яндекс Умный дом", place: "y0_…", about: "Джарвис сам управляет светом, пылесосом и сценариями",
      more: "В режиме «через Алису» можно обойтись без него.",
      how: "<li>Нужен OAuth-токен Яндекса с правами iot:view и iot:control.</li><li>Его выдаёт приложение, зарегистрированное на oauth.yandex.ru (тип «Веб-сервисы», права «Умный дом»).</li><li>Вставьте токен (начинается с y0_).</li>" }),
    smartLifeConn(),
    xiaomiConn(),
    { id: "youtube", group: "home", mono: "YT", name: "YouTube", about: "Музыка и видео голосом. Вход — для ваших рекомендаций и видео 18+", more: "Там, где YouTube заблокирован (Россия), включите «Через VPN»",
      on: !!(ya.signed_in || yt.set), state: ya.signed_in ? "вход выполнен" : yt.set ? "куки загружены" : ya.code ? "ждёт код" : "не подключено",
      wait: !ya.signed_in && !!ya.code,
      body: ya.code ? `<div class="ycode"><span>Откройте <a href="${esc(ya.url)}" target="_blank" rel="noopener">${esc(ya.url.replace("https://", ""))}</a> и введите</span><b class="code-big" role="button" tabindex="0" title="Нажмите, чтобы скопировать">${esc(ya.code)}</b><small class="muted">окно само увидит вход</small></div>`
        : ya.signed_in ? `<div class="cn-ok">${icon("done")}<span>Колонка вошла в YouTube<small>видео 18+ тоже играют</small></span></div>`
        : `${ya.error ? `<p class="err">${esc(ya.error)}</p>` : ""}<div class="row"><button class="btn primary" id="ya-in">Войти в YouTube</button><small class="muted">кодом, как на телевизоре — пароль колонка не видит</small></div>
          <details class="mini"><summary>Не получается кодом? Куки браузера</summary><ol class="how"><li>Расширение «Get cookies.txt LOCALLY» → окно инкогнито → войдите на youtube.com.</li><li>Export (Netscape) → скопируйте весь текст сюда → закройте окно, не выходя из аккаунта.</li></ol>
          <textarea id="yt-cookies" class="code" placeholder="# Netscape HTTP Cookie File…" spellcheck="false" aria-label="Куки YouTube"></textarea>
          <div class="row"><button class="btn sm" id="yt-save">${yt.set ? "Заменить" : "Сохранить и проверить"}</button></div></details>`
        + vpnRow("youtube"),
      drop: ya.signed_in ? `<button class="btn ghost danger" id="ya-out">Выйти</button>` : yt.set ? `<button class="btn ghost danger" id="yt-clear">Убрать куки</button>` : "" },
    { id: "spotify", group: "home", mono: "Sp", name: "Spotify", about: "Музыка голосом: «Джарвис, включи … в Spotify». Играет Soloist — его ставите вы сами, Джарвис только передаёт запрос",
      more: "Нужен Spotify Premium и установленный Soloist (Spotify Connect на колонке).",
      on: !!sp.installed, need: !sp.installed, wait: !sp.installed && sp.install?.state === "running",
      state: !sp.installed ? (sp.install?.state === "running" ? "ставлю…" : "Soloist не найден") : sp.playing ? "играет" : sp.running ? "готов" : "Soloist не отвечает",
      body: !sp.installed ? (sp.install?.state === "running"
          ? `<p class="note">Ставлю Soloist: ${esc(sp.install.step || "…")}. Это займёт пару минут; Джарвис перезапустится сам.</p>`
          : `${sp.install?.state === "error" ? `<p class="err">${esc(sp.install.error || "Не получилось установить Soloist")}</p>` : ""}
        <p class="note">Soloist не найден. Джарвис сам скачает его с GitHub автора, проверит файл и поставит на колонку. Нужен Spotify Premium.</p>
        <div class="f"><label for="sp-i-key">API-ключ Soloist</label><input id="sp-i-key" type="password" autocomplete="off" placeholder="spak_…"></div>
        <details class="mini"><summary>Где взять ключ</summary><ol class="how"><li>Откройте <a href="https://developer.spotify.com/dashboard/soloist" target="_blank" rel="noopener">developer.spotify.com/dashboard/soloist</a> и войдите в Spotify Premium.</li><li>Скопируйте ключ (начинается с spak_) и вставьте сюда.</li></ol></details>
        <div class="f"><label for="sp-i-name">Имя устройства в Spotify (по желанию)</label><input id="sp-i-name" type="text" autocomplete="off" placeholder="Определится по модели колонки"></div>
        <details class="mini"><summary>Голосовой поиск: свои Client ID и Secret (по желанию)</summary>
          <div class="f"><label for="sp-i-cid">Client ID</label><input id="sp-i-cid" type="password" autocomplete="off"></div>
          <div class="f"><label for="sp-i-sec">Client Secret</label><input id="sp-i-sec" type="password" autocomplete="off"></div></details>
        <div class="row"><button class="btn primary" id="sp-install">Установить Soloist</button></div>`)
        : `<div class="cn-ok">${icon("done")}<span>Soloist найден<small>${esc(sp.root || "")}</small></span></div>
        <div class="f"><label for="sp-name">Имя устройства в Spotify</label><input id="sp-name" type="text" value="${esc(sp.device_name || "")}" autocomplete="off" placeholder="Например: Станция"></div>
        <div class="f"><label for="sp-key">API-ключ Soloist</label><input id="sp-key" type="password" value="" autocomplete="off" placeholder="${esc(sp.api_key || "Вставьте ключ")}"></div>
        <details class="mini"><summary>Свои Client ID и Secret (по желанию)</summary>
          <div class="f"><label for="sp-cid">Client ID</label><input id="sp-cid" type="password" value="" autocomplete="off" placeholder="${esc(sp.client_id || "")}"></div>
          <div class="f"><label for="sp-sec">Client Secret</label><input id="sp-sec" type="password" value="" autocomplete="off" placeholder="${esc(sp.client_secret || "")}"></div></details>
        <div class="row"><button class="btn primary" id="sp-save">Сохранить</button><button class="btn" id="sp-restart">Перезапустить Soloist</button></div>
        <div class="row"><button class="btn" id="sp-tune"${sp.yam ? " disabled" : ""}>Настроить звук</button></div>
        ${sp.tune?.state === "running" ? `<p class="note">Подбираю звук: ${esc(sp.tune.step || "…")}. Музыка на это время замолчит, это займёт около двух минут.</p>`
          : sp.tune?.state === "done" ? `<p class="note">Звук настроен: ${esc(sp.tune.device)}.</p>`
          : sp.tune?.state === "error" ? `<p class="err">${esc(sp.tune.error || "Звук настроить не удалось")}</p>` : ""}
        <p class="note">Ключ API работает только с Spotify Premium. Soloist перезапускается сам после «Сохранить». Нет звука при играющей музыке? Сначала привяжите Soloist к Spotify (выберите колонку в приложении Spotify), потом нажмите «Настроить звук»${sp.yam ? "; для установки через YAM кнопка не нужна" : ""}.</p>`,
      drop: "" },
    { id: "vpn", group: "net", icon: "shield", name: "VPN для Джарвиса", about: "Через VPN идёт только сам Джарвис, Алиса — всегда напрямую. Что именно пускать через VPN — галочки ниже",
      on: !!v.set && !v.error, bad: !!v.error, need: !v.set, state: v.set ? (v.error ? "ошибка" : esc(v.name || v.host)) : "не задан",
      body: `${v.set ? `<div class="vpn ${v.error ? "bad" : "ok"}"><b>${esc(v.name || v.host)}</b><small>${esc(v.host)}:${esc(v.port)} · ${esc(v.network)} + ${esc(v.security)}</small></div>` : `<p class="note">Пока не задан — колонка ходит в интернет напрямую. Вставьте ссылку vless://… из вашего VPN (3x-ui, Marzban, Hiddify и т. п.).</p>`}
        ${v.error ? `<p class="err">${esc(v.error)}</p>` : ""}
        <textarea id="vless" class="code" placeholder="vless://…" spellcheck="false" aria-label="Ссылка VLESS"></textarea>
        <div class="row"><button class="btn primary" id="vless-save">${v.set ? "Заменить ссылку" : "Сохранить ссылку"}</button><span class="spacer"></span><button class="btn" data-net>Проверить связь</button></div>
        <div class="netres" id="net-res"></div>
        <h3>Что идёт через VPN</h3><div class="vpn-list">${VPN_NAMES.map(([id, n]) => `<label class="sw"><input type="checkbox" data-vpn="${id}"${S.vpn?.[id] ? " checked" : ""}><span class="tr"></span>${n}</label>`).join("")}</div>`,
      drop: v.set ? `<button class="btn ghost danger" id="vless-clear">Убрать VPN</button>` : "" },
  ];
}

export const CONN_GROUPS = [["voice", "Голос и мозг", "Чем Джарвис говорит и думает. Достаточно одного"], ["home", "Дом и музыка", ""], ["net", "Сеть", ""]];

export function viewConnections() {
  $("#view").innerHTML = `<header class="ph ph-row"><div><h1>Подключения</h1><p class="sub">Ключи хранятся только на колонке и обратно в браузер не отдаются</p></div>
    <div class="actions"><button class="btn" data-net data-tip="Колонка достучится до OpenAI, Gemini, Grok и ElevenLabs с вашими ключами и покажет время ответа">${icon("radio")}Проверить связь</button></div></header>
  <div id="conns"></div>
  <div class="modal hidden" id="cn-modal" role="dialog" aria-modal="true" aria-labelledby="cn-mt"><div class="modal-bg" data-close></div><div class="modal-card cn-card" id="cn-card"></div></div>`;
  CONN.open = "";
  const view = $("#view");
  view.oninput = null;
  view.onchange = memChange;
  view.onclick = connClick;
  view.onkeydown = (e) => {
    if (page !== "connections") return; // обработчик висит на #view и после ухода со страницы
    const t = e.target.closest(".cn[data-conn]");
    if (t && (e.key === "Enter" || e.key === " ")) { e.preventDefault(); connOpen(t.dataset.conn); }
    if (e.key === "Escape" && CONN.open) connClose();
    if (e.key === "Enter" && e.target.matches("input[id^='k-']")) $(`[data-key="${e.target.id.slice(2)}"]`)?.click();
    if (e.key === "Enter" && e.target.id === "sl-code") $("#sl-in")?.click();
  };
  drawConns();
}

export function connTile(c) {
  const net = CONN.net[c.id];
  const cls = c.bad || (net && !net.ok) ? "bad" : c.on ? "ok" : c.need ? "warn" : "";
  const state = net ? (net.ok ? `${c.state} · ${net.ms} мс` : esc(net.error || "нет связи")) : c.state;
  return `<article class="card cn${c.on ? " on" : ""}" data-conn="${c.id}" tabindex="0" role="button" aria-label="${esc(c.name)}">
    <div class="cn-h"><span class="char-av cn-av">${c.icon ? icon(c.icon) : esc(c.mono)}</span><h2>${esc(c.name)}</h2></div>
    <p class="cn-t">${c.about}</p>
    <div class="cn-f"><span class="st ${cls}">${state}</span><span class="spacer"></span><span class="char-edit">${c.on ? `${icon("tune")}Настроить` : `${icon("plus")}Подключить`}</span></div>
  </article>`;
}

export function drawConns() {
  const box = $("#conns");
  if (!box) return;
  const defs = connDefs();
  box.innerHTML = CONN_GROUPS.map(([g, title, sub]) => {
    const list = defs.filter((c) => c.group === g);
    const n = list.filter((c) => c.on).length;
    return list.length ? `<section class="cn-group"><div class="cn-gh"><h2>${title}</h2><span class="meta">${n ? `подключено ${n} из ${list.length}` : sub}</span></div>
      <div class="chars cn-grid">${list.map(connTile).join("")}</div></section>` : "";
  }).join("");
  if (CONN.open) connBody(defs.find((c) => c.id === CONN.open));
  // вход кодом: ждём, пока человек введёт код на сайте
  clearTimeout(drawConns.wait);
  if (defs.some((c) => c.wait)) drawConns.wait = setTimeout(async () => {
    if (location.hash !== "#connections") return;
    const was = { cg: S.chatgpt.signed_in, yt: S.youtube?.account?.signed_in, xm: S.xiaomi?.signed_in, sl: S.smartlife?.signed_in };
    await reload(); drawConns();
    if (!was.sl && S.smartlife?.signed_in) toast("Smart Life подключён — ищу устройства");
    if (!was.xm && S.xiaomi?.signed_in) toast("Xiaomi Home подключён — ищу устройства");
    if (!was.cg && S.chatgpt.signed_in) toast("ChatGPT подключён");
    if (!was.yt && S.youtube?.account?.signed_in) toast("YouTube подключён");
  }, 3000);
}

export function connBody(c) {
  const card = $("#cn-card");
  if (!card || !c) return;
  // поля, которые человек уже начал заполнять, перерисовка не стирает
  const keep = {};
  card.querySelectorAll("input, textarea").forEach((el) => { if (el.id && el.value) keep[el.id] = el.value; });
  const fa = document.activeElement, focus = fa && card.contains(fa) && fa.id ? { id: fa.id, a: fa.selectionStart, b: fa.selectionEnd } : null;
  const net = CONN.net[c.id];
  card.innerHTML = `<div class="cn-mh"><span class="char-av cn-av">${c.icon ? icon(c.icon) : esc(c.mono)}</span>
      <div class="char-n"><h2 id="cn-mt">${esc(c.name)}</h2><span class="st ${c.bad ? "bad" : c.on ? "ok" : c.need ? "warn" : ""}">${c.state}${net?.ok ? ` · ${net.ms} мс` : ""}</span></div>
      <button class="ib" type="button" data-close aria-label="Закрыть">${icon("x")}</button></div>
    <p class="cn-about">${c.about}${c.more ? `. ${c.more}` : ""}</p>
    <div class="cn-body">${c.body}</div>
    ${c.drop ? `<div class="row cn-foot"><span class="spacer"></span>${c.drop}</div>` : ""}`;
  for (const [id, val] of Object.entries(keep)) { const el = card.querySelector("#" + id); if (el) el.value = val; }
  // перерисовка по таймеру не должна выбивать человека из поля, где он печатает
  if (focus) { const el = card.querySelector("#" + focus.id); if (el) { el.focus(); try { el.setSelectionRange(focus.a, focus.b); } catch (e) { /* у password/number выделения нет */ } } }
}

export function connOpen(id) {
  const c = connDefs().find((x) => x.id === id);
  if (!c) return;
  CONN.open = id;
  connBody(c);
  const m = $("#cn-modal");
  m.classList.remove("hidden", "out"); void m.offsetWidth; m.classList.add("in");
  // на телефоне не фокусируем поле: иначе сразу выпрыгивает клавиатура и закрывает окно
  if (matchMedia("(hover: hover)").matches) setTimeout(() => $("#cn-card input, #cn-card textarea, #cn-card .btn.primary")?.focus(), 60);
}

export function connClose() {
  const m = $("#cn-modal");
  CONN.open = "";
  m.classList.remove("in"); m.classList.add("out");
  setTimeout(() => { if (m.classList.contains("out")) m.classList.add("hidden"); }, 260);
}

export async function connNetTest(btn) {
  await act(btn, async () => {
    if ($("#net-res")) $("#net-res").innerHTML = `<div class="nr"><span class="spin"></span>Проверяю…</div>`;
    let r;
    try { r = await api("POST", "/api/test/net"); } catch (err) { if ($("#net-res")) $("#net-res").innerHTML = ""; throw err; }
    CONN.net = {};
    for (const [name, x] of Object.entries(r)) CONN.net[name] = x;
    drawConns();
    if ($("#net-res")) $("#net-res").innerHTML = Object.entries(r).map(([name, x]) => `<div class="nr ${x.ok ? "ok" : "bad"}"><b>${NET_NAMES[name] || esc(name)}</b>${x.ok ? `работает · ${x.ms} мс` : esc(x.error || "нет связи")}</div>`).join("");
    const bad = Object.entries(r).filter(([, x]) => !x.ok).map(([n]) => NET_NAMES[n] || n);
    if (!Object.keys(r).length) toast("Нечего проверять — нет ни одного ключа", true);
    else if (bad.length) toast(`Нет связи: ${bad.join(", ")}`, true);
    else toast("Связь со всеми есть");
  });
}

export async function connClick(e) {
  if (page !== "connections") return;
  const t = e.target;
  const redraw = async () => { await reload(); drawConns(); };
    if(t.closest("[data-ha-remove]")){t.closest("[data-ha-room]").remove();return;}
  if (t.closest("[data-close]")) return connClose();
  if (t.matches("input[data-vpn]")) {
    const svc = t.dataset.vpn, on = t.checked;
    try { await api("PUT", "/api/settings", { vpn: { [svc]: on } }); delete CONN.net[svc]; await redraw(); toast(`${VPN_NAMES.find(([id]) => id === svc)?.[1] || svc}: ${on ? "через VPN" : "напрямую"}`); }
    catch (err) { t.checked = !on; toast(err.message, true); }
    return;
  }
  const gk = t.closest("[data-gk-remove]");
  if (gk) { if (confirm("Убрать этот ключ Google с колонки?")) act(gk, async () => { await api("PUT", "/api/gemini-keys", { remove: +gk.dataset.gkRemove }); await redraw(); }, "Ключ убран"); return; }
  const ek = t.closest("[data-ek-remove], [data-ek-reset]");
  if (ek) {
    const rm = ek.dataset.ekRemove !== undefined, i = +(rm ? ek.dataset.ekRemove : ek.dataset.ekReset);
    if (rm && !confirm(`Убрать ключ ${i + 1} с колонки?`)) return;
    return act(ek, async () => { await api("PUT", "/api/eleven-keys", rm ? { remove: i } : { reset: i }); await redraw(); }, rm ? "Ключ убран" : "Ключ снова в работе");
  }
  const tile = t.closest(".cn[data-conn]");
  if (tile) return connOpen(tile.dataset.conn);
  const net = t.closest("[data-net]");
  if (net) return connNetTest(net);
  const b = t.closest("[data-key]"), c = t.closest("[data-clear]");
  if (b) {
    const val = $("#k-" + b.dataset.key).value.trim();
    if (!val) return toast("Сначала вставьте ключ в поле", true);
    return act(b, async () => { await api("PUT", "/api/keys", { [b.dataset.key]: val }); $("#k-" + b.dataset.key).value = ""; delete CONN.net[b.dataset.key]; await redraw(); }, (b.dataset.key === "elevenlabs" && (S.eleven_keys || []).length > 1) || (b.dataset.key === "gemini" && (S.gemini_keys || []).length > 1) ? "Ключ добавлен в запас" : "Ключ сохранён");
  }
  if (c) { if (confirm(c.dataset.clear === "elevenlabs" && (S.eleven_keys || []).length > 1 ? "Убрать все ключи ElevenLabs с колонки?" : c.dataset.clear === "gemini" && (S.gemini_keys || []).length > 1 ? "Убрать все ключи Google с колонки?" : "Убрать этот ключ с колонки?")) act(c, async () => { await api("PUT", "/api/keys", { [c.dataset.clear]: "" }); delete CONN.net[c.dataset.clear]; await redraw(); }); return; }
  const btn = t.closest("button[id]");
  if (!btn) return;
  switch (btn.id) {
    case "ha-discover-rooms": return act(btn,async()=>{
      const r=await api("POST","/api/home-assistant/rooms/discover",{});
      if(!r.rooms?.length){toast("Автоматическое определение комнат недоступно. Можно выбрать готовые сценарии уборки.",true);return;}
      const catalog=await api("GET","/api/home-assistant");CONN.haEntities=catalog.entities || [];
      $("#ha-rooms").innerHTML=r.rooms.map(haRoomRow).join("");toast(`Найдено комнат: ${r.rooms.length}. Нажмите «Проверить и сохранить».`);
    });
    case "ha-add-room": $("#ha-rooms").insertAdjacentHTML("beforeend",haRoomRow()); return;
    case "ha-load": return act(btn,async()=>{
      const r=await api("GET","/api/home-assistant");CONN.haEntities=r.entities || [];
      const rooms=$$("[data-ha-room]").map(el=>({...JSON.parse(el.dataset.haBinding || "{}"),name:el.querySelector(".ha-name").value,vacuum:el.querySelector(".ha-vacuum").value,entity:el.querySelector(".ha-entity").value,room_ids:el.querySelector(".ha-ids").value}));
      $("#ha-rooms").innerHTML=rooms.map(haRoomRow).join("");
      toast(`Получено устройств и действий: ${CONN.haEntities.length}`);
    });
    case "ha-save": return act(btn,async()=>{
      const rooms=$$("[data-ha-room]").map(el=>({...JSON.parse(el.dataset.haBinding || "{}"),name:el.querySelector(".ha-name").value.trim(),vacuum:el.querySelector(".ha-vacuum").value,entity:el.querySelector(".ha-entity").value,room_ids:el.querySelector(".ha-ids").value.trim()}));
      await api("PUT","/api/home-assistant",{url:$("#ha-url").value,token:$("#ha-token").value,rooms});
      $("#ha-token").value="";await redraw();
    },"Home Assistant подключён");
    case "ha-disconnect": return act(btn,async()=>{await api("DELETE","/api/home-assistant");CONN.haEntities=[];await redraw();},"Home Assistant отключён");
    case "cg-in": return act(btn, async () => { await api("POST", "/api/chatgpt/login"); await redraw(); });
    case "cg-newchat": if (confirm("Начать новый чат? Джарвис забудет конспект и недавние реплики. Досье о вас, летопись и архив останутся.")) act(btn, async () => { await api("POST", "/api/chatgpt/newchat"); await redraw(); }, "Новый чат начат"); return;
    case "mem-compact": act(btn, async () => { const r = await api("POST", "/api/memory/compact"); await redraw(); if (!r.turns) toast("Сжимать пока нечего"); }, "Досье и конспект обновлены"); return;
    case "mem-erase": if (confirm("Стереть всю память Джарвиса — досье о вас, конспект, летопись и архив разговоров? Это не отменить.")) act(btn, async () => { await api("POST", "/api/chatgpt/newchat?all=1"); await redraw(); }, "Память стёрта"); return;
    case "cg-out": if (confirm("Выйти из ChatGPT на колонке?")) act(btn, async () => { await api("DELETE", "/api/chatgpt"); await redraw(); }); return;
    case "sl-in": {
      const code = $("#sl-code").value.trim();
      if (!code) return toast("Сначала вставьте код пользователя из Smart Life", true);
      return act(btn, async () => { await api("POST", "/api/smartlife/login", { user_code: code }); await redraw(); });
    }
    case "sl-refresh": return act(btn, async () => { const r = await api("POST", "/api/smartlife/refresh"); await redraw(); toast(`Устройств: ${r.devices}`); });
    case "sl-out": if (confirm("Выйти из Smart Life на колонке?")) act(btn, async () => { await api("DELETE", "/api/smartlife"); await redraw(); }); return;
    case "xm-in": return act(btn, async () => { await api("POST", "/api/xiaomi/login"); await redraw(); });
    case "xm-refresh": return act(btn, async () => { const r = await api("POST", "/api/xiaomi/refresh"); await redraw(); toast(`Устройств: ${r.devices}`); });
    case "xm-out": if (confirm("Выйти из Xiaomi Home на колонке?")) act(btn, async () => { await api("DELETE", "/api/xiaomi"); await redraw(); }); return;
    case "ya-in": return act(btn, async () => { await api("POST", "/api/youtube/login"); await redraw(); });
    case "ya-out": if (confirm("Выйти из YouTube на колонке?")) act(btn, async () => { await api("DELETE", "/api/youtube/login"); await redraw(); }); return;
    case "yt-save": {
      const txt = $("#yt-cookies").value.trim();
      if (!txt) return toast("Сначала вставьте текст файла cookies.txt", true);
      return act(btn, async () => { await api("PUT", "/api/youtube", { cookies: txt }); $("#yt-cookies").value = ""; await redraw(); }, "YouTube принял вход");
    }
    case "yt-clear": if (confirm("Убрать вход в YouTube с колонки?")) act(btn, async () => { await api("DELETE", "/api/youtube"); await redraw(); }); return;
    case "sp-install": {
      const key = $("#sp-i-key").value.trim();
      if (!key) return toast("Сначала вставьте API-ключ Soloist", true);
      const body = { api_key: key, device_name: $("#sp-i-name").value.trim(), client_id: $("#sp-i-cid").value.trim(), client_secret: $("#sp-i-sec").value.trim() };
      return act(btn, async () => {
        await api("POST", "/api/spotify/install", body);
        // пока ставится, панель опрашивает состояние; в конце Джарвис перезапускается — ошибки связи в этот момент ждём
        for (let n = 0; n < 150; n++) {
          await new Promise((r) => setTimeout(r, 2000));
          try { await redraw(); } catch (e) { continue; }
          if (S.spotify?.installed || S.spotify?.install?.state === "error") break;
        }
        if (!S.spotify?.installed) throw new Error(S.spotify?.install?.error || "Установка не завершилась вовремя — посмотрите карточку позже");
      }, "Soloist поставлен — осталось включить музыку в Spotify");
    }
    case "sp-save": {
      const f = { device_name: "#sp-name", api_key: "#sp-key", client_id: "#sp-cid", client_secret: "#sp-sec" }, body = {};
      for (const [k, sel] of Object.entries(f)) { const el = $(sel); if (el && el.value !== el.defaultValue) body[k] = el.value.trim(); }
      if (!Object.keys(body).length) return toast("Ничего не изменилось");
      return act(btn, async () => {
        const r = await api("PUT", "/api/spotify", body);
        await redraw();
        if (r?.restart_error) throw new Error(r.restart_error);
      }, "Сохранено, Soloist перезапущен");
    }
    case "sp-tune": return act(btn, async () => {
      await api("POST", "/api/spotify/tune");
      for (let n = 0; n < 180; n++) {
        await new Promise((r) => setTimeout(r, 2000));
        try { await redraw(); } catch (e) { continue; }
        if (S.spotify?.tune?.state !== "running") break;
      }
      if (S.spotify?.tune?.state === "error") throw new Error(S.spotify.tune.error || "Звук настроить не удалось");
      if (S.spotify?.tune?.state === "running") throw new Error("Подбор ещё идёт — посмотрите карточку позже");
    }, "Звук настроен");
    case "sp-restart": return act(btn, async () => { await api("POST", "/api/spotify/restart"); await redraw(); }, "Soloist перезапускается");
    case "vless-save": return act(btn, async () => { await api("PUT", "/api/vless", { link: $("#vless").value }); $("#vless").value = ""; await redraw(); }, "Ссылка сохранена — проверьте связь");
    case "vless-clear": if (confirm("Убрать VPN? Колонка пойдёт в интернет напрямую.")) act(btn, async () => { await api("PUT", "/api/vless", { link: "" }); await redraw(); }); return;
  }
}
