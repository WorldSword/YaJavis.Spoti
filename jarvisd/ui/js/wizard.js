// Знакомство: мастер первой настройки.
import { connOpen } from "./connections.js";
import { $, act, api, esc, icon } from "./core.js";
import { S, go, reload } from "./main.js";
import { PL, plCatalog, plIcon } from "./plugins.js";

// ---------------------------------------------------------------- знакомство
// Показывается один раз после установки (и по кнопке на странице «Безопасность»): что такое Джарвис,
// что он умеет, кто будет думать, какие плагины поставить. Можно уйти настраивать и вернуться.
export const WIZ = { step: 0, picked: null, away: false };
export const WIZ_STEPS = ["Привет", "Что умею", "Кто думает", "Плагины", "Готово"];

export function wizOpen(step = 0) {
  WIZ.step = step; WIZ.away = false;
  $("#wiz-pill")?.remove();
  let w = $("#wiz");
  if (!w) {
    w = document.createElement("div");
    w.id = "wiz"; w.className = "wiz"; w.setAttribute("role", "dialog"); w.setAttribute("aria-modal", "true");
    document.body.append(w);
    w.addEventListener("click", wizClick);
  }
  w.classList.remove("hidden");
  wizDraw();
}

export function wizAI() {
  return [
    { id: "chatgpt", on: !!S.chatgpt.signed_in, name: "Подписка ChatGPT", text: "Если у вас есть ChatGPT Plus: живой голос GPT, вход кодом, без ключей и оплаты сверху." },
    { id: "gemini", on: !!S.keys.gemini, name: "Google Gemini", text: "Бесплатный ключ из AI Studio за минуту. Хороший выбор, чтобы просто начать." },
    { id: "openai", on: !!S.keys.openai, name: "OpenAI", text: "Ключ API с оплатой по факту: самый быстрый живой голос." },
    { id: "grok", on: !!S.keys.grok, name: "xAI Grok", text: "Ключ xAI: голос Grok с поиском по X и интернету." },
  ];
}

export function wizDraw() {
  const w = $("#wiz");
  if (!w) return;
  const st = WIZ.step, last = WIZ_STEPS.length - 1;
  const dots = WIZ_STEPS.map((n, i) => `<button type="button" class="wz-dot${i === st ? " on" : i < st ? " done" : ""}" data-wz-go="${i}" aria-label="${n}"><i></i><span>${n}</span></button>`).join("");
  let body = "";
  if (st === 0) body = `<div class="wz-hero"><span class="orb big" data-s="speaking"><i></i></span>
      <h1>Привет! Я Джарвис</h1>
      <p>Голосовой ассистент, который живёт прямо на вашей Яндекс Станции. Скажите <b>«Джарвис, …»</b> — и я отвечу голосом живой нейросети:
        ChatGPT, Gemini, Grok или OpenAI, как вы выберете. Алиса никуда не девается: на «Алиса» она отвечает как раньше, а умный дом, таймеры и музыку я могу поручать ей.</p>
      <p class="muted">Всё работает на самой колонке: без сервера и без облака автора. Ключи и разговоры хранятся только у вас.</p></div>`;
  if (st === 1) body = `<h1>Что я умею</h1><div class="wz-grid">${[
      ["mic", "Живой разговор", "Перебивайте, переспрашивайте, говорите как с человеком. Разговор держится, пока вы говорите."],
      ["voice", "Голоса и характеры", "Десятки голосов и свои характеры: строгий дворецкий, весёлый друг, учитель английского."],
      ["home", "Умный дом", "Яндекс, Home Assistant, Xiaomi, Smart Life — «Джарвис, выключи свет на кухне»."],
      ["music", "Музыка", "YouTube на колонке, очередь, радио похожих, плеер в панели."],
      ["read", "Долгая память", "Помню, что вы рассказывали неделю назад, и сам вспоминаю к месту. «Забудь» — забываю."],
      ["puzzle", "Плагины", "Погода, курсы валют и что угодно ещё. Ставятся в один клик, написать свой — вечер."],
    ].map(([i, h, t]) => `<div class="wz-f">${icon(i)}<b>${h}</b><span>${t}</span></div>`).join("")}</div>
    <p class="muted">Настройки — здесь, в панели: слева «Ассистенты» (голос, модель, характер), «Колонка» (слово «Джарвис», громкость, подсветка), «Подключения» (ключи и сервисы), «Плагины».</p>`;
  if (st === 2) {
    const ai = wizAI(), any = ai.some((a) => a.on);
    body = `<h1>Кто будет думать</h1><p>Мне нужна нейросеть — хотя бы одна. ${any ? `<b class="ok">Уже подключено — можно идти дальше.</b>` : "Выберите, что вам удобнее, и подключите в «Подключениях» — я подожду здесь."}</p>
      <div class="wz-list">${ai.map((a) => `<div class="wz-ai${a.on ? " on" : ""}">${icon(a.on ? "done" : "todo")}<div><b>${a.name}</b><span>${a.text}</span></div>
        ${a.on ? `<span class="st ok">подключено</span>` : `<button class="btn sm" type="button" data-wz-conn="${a.id}">Подключить</button>`}</div>`).join("")}</div>
      <p class="muted">Из России и Беларуси OpenAI, Gemini и Grok работают только через VPN: ссылку VLESS можно добавить там же, в «Подключениях».</p>`;
  }
  if (st === 3) {
    const cat = (PL.list || []).filter((c) => c.official);
    if (!PL.list && !PL.loading) plCatalog(false).then(() => WIZ.step === 3 && wizDraw());
    if (!WIZ.picked && PL.list) WIZ.picked = new Set(cat.filter((c) => c.recommended).map((c) => c.id)); // отметить рекомендованные, когда каталог загрузился
    const have = new Set((S.plugins?.installed) || []);
    body = `<h1>Плагины</h1><p>Отметьте, что пригодится, — поставлю сразу. Потом всё можно поменять на странице «Плагины»: там же каталог от других людей и установка из файла.</p>
      <div class="wz-list">${cat.length ? cat.map((c) => `<label class="wz-ai${have.has(c.id) ? " on" : ""}">${plIcon(c.icon)}<div><b>${esc(c.name)}</b><span>${esc(c.description)}</span></div>
        ${have.has(c.id) ? `<span class="st ok">стоит</span>` : `<span class="sw"><input type="checkbox" data-wz-pick="${esc(c.id)}"${WIZ.picked.has(c.id) ? " checked" : ""}><span class="tr"></span></span>`}</label>`).join("")
        : `<p class="muted">${PL.loading ? "Загружаю каталог…" : "Каталог не загрузился — колонке нужен интернет. Плагины можно поставить потом."}</p>`}</div>`;
  }
  if (st === 4) body = `<div class="wz-hero"><span class="orb big" data-s="idle"><i></i></span><h1>Готово</h1>
      <p>Скажите <b>«Джарвис, привет»</b> — и поговорим. Пара идей для начала:</p>
      <ul class="wz-tips"><li>«Джарвис, какая погода завтра?»</li><li>«Джарвис, включи что-нибудь спокойное с ютуба»</li><li>«Джарвис, запомни: я не ем мясо»</li><li>«Джарвис, выключи свет в спальне»</li></ul>
      <p class="muted">Это знакомство можно открыть снова на странице «Безопасность».</p></div>`;
  w.innerHTML = `<div class="wiz-card"><div class="wz-top"><nav class="wz-dots">${dots}</nav><button class="btn ghost sm" type="button" data-wz-skip>Пропустить</button></div>
    <div class="wz-body">${body}</div>
    <div class="wz-foot">${st > 0 ? `<button class="btn ghost" type="button" data-wz-go="${st - 1}">Назад</button>` : ""}<span class="spacer"></span>
      ${st === 3 ? `<button class="btn primary" type="button" data-wz-install>Поставить и дальше</button>`
        : st === last ? `<button class="btn primary" type="button" data-wz-done>Начать</button>` : `<button class="btn primary" type="button" data-wz-go="${st + 1}">Дальше</button>`}</div></div>`;
}

export async function wizDone() {
  await api("POST", "/api/onboarded").catch(() => {});
  S.settings.onboarded = true;
  $("#wiz")?.classList.add("hidden");
  $("#wiz-pill")?.remove();
}

export async function wizClick(e) {
  const t = e.target.closest("button, input");
  if (!t) return;
  if (t.dataset.wzPick != null) { WIZ.picked[t.checked ? "add" : "delete"](t.dataset.wzPick); return; }
  if (t.dataset.wzGo != null) { WIZ.step = Number(t.dataset.wzGo); wizDraw(); return; }
  if (t.dataset.wzSkip != null || t.dataset.wzDone != null) { wizDone(); return; }
  if (t.dataset.wzConn) { // уйти подключать, а знакомство свернуть в плашку
    WIZ.away = true;
    $("#wiz").classList.add("hidden");
    const pill = document.createElement("button");
    pill.id = "wiz-pill"; pill.className = "btn primary wiz-pill"; pill.type = "button";
    pill.innerHTML = `${icon("right")}Вернуться к знакомству`;
    pill.onclick = async () => { await reload(); wizOpen(2); };
    document.body.append(pill);
    go("#connections");
    setTimeout(() => connOpen(t.dataset.wzConn), 120);
    return;
  }
  if (t.dataset.wzInstall != null) {
    const ids = [...(WIZ.picked || [])].filter((id) => !(S.plugins?.installed || []).includes(id));
    await act(t, async () => {
      for (const id of ids) {
        t.textContent = `Ставлю «${(PL.list || []).find((c) => c.id === id)?.name || id}»…`;
        await api("POST", "/api/plugins/install", { repo: S.repo || "newfpv/YaJavis", id });
      }
      await reload();
    }, ids.length ? `Поставлено плагинов: ${ids.length}` : "");
    WIZ.step = 4; wizDraw();
  }
}

