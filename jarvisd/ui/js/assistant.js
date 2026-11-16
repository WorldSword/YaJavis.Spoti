// Ассистент: голос, модель, характер.
import { $, $$, act, api, esc, icon, sec, setDirty, tip, toast } from "./core.js";
import { ledPickerBind, ledPickerHtml, paintLeds } from "./leds.js";
import { S, avatar, go, reload, route } from "./main.js";

export let previewSeq = 0;

// ---------------------------------------------------------------- ассистент
export function viewProfile(id) {
  const isNew = id === "new";
  const base = S.profiles.find((x) => x.id === id);
  if (!isNew && !base) { go("#home"); return; }
  const p = structuredClone(base || { id: "", name: "", color: (S.profiles.length % 6) + 1, mode: "live", voice: "cedar", backend_model: S.backend_models[0], effort: "low", priority: true, web_search: true, idle_s: 8, prompt: S.prompts[0]?.id || "" });
  p.led ||= { ...(S.settings.led || {}) }; p.led.brightness ||= 45;
  p.volume ||= S.settings.volume || 40; p.city ??= S.settings.city || ""; p.timezone ||= S.settings.timezone || "UTC";
  p.engine ||= "openai";
  if(p.mode==="classic")p.engine="elevenlabs";
  p.llm_model ||= p.backend_model;
  p.tts_model ||= "eleven_flash_v2_5";
  if(p.engine==="elevenlabs")p.backend_model=p.llm_model;
  const active = p.id === S.active;
  $("#view").innerHTML = `<header class="ph who"><span id="pav" class="ringbox">${avatar(p, true)}</span><div><h1>${isNew ? "Новый ассистент" : esc(p.name)}</h1><p class="sub">${isNew ? "Свой голос, модель и характер" : active ? "Сейчас отвечает на «Джарвис»" : "Не активен"}</p></div></header>
  ${sec("Основное", "Как ассистент выглядит в списке и кем он себя считает",
    `<div class="grid g2"><div class="f"><label>Имя ${tip("Как ассистент называется в списке слева. На слово активации не влияет.")}</label><input type="text" id="p-name" maxlength="40" value="${esc(p.name)}" placeholder="Например, Джарвис"></div>
      <div class="f"><label>Цвет</label><div class="colors" id="p-color">${[1, 2, 3, 4, 5, 6].map((c) => `<button class="av c${c}${p.color === c ? " on" : ""}" data-c="${c}" type="button" aria-label="Цвет ${c}"></button>`).join("")}</div></div></div>
    <div class="f"><label>Характер ${tip("Системный промпт: кто такой Джарвис и как он общается. Характеры редактируются в разделе «Характеры».")}</label><select id="p-prompt">${S.prompts.map((c) => `<option value="${esc(c.id)}"${c.id === p.prompt ? " selected" : ""}>${esc(c.name)}</option>`).join("")}</select></div>`)}
  ${sec("Мозг", "Выберите живой разговор или классическую цепочку: распознавание речи → текстовая модель → озвучка",
    `<div class="choice" id="p-engine">
      <button class="opt" data-engine="elevenlabs" type="button"><b>ElevenLabs — классический</b><small>Распознавание Scribe и голос ElevenLabs. Текстовая модель OpenAI думает и вызывает инструменты. Нужны ключи ElevenLabs и OpenAI</small></button>
      <button class="opt" data-engine="eleven_realtime" type="button"><b>ElevenLabs — живой разговор</b><small>Eleven Realtime Mini или Max: распознавание, мозг, голос и перебивания вместе. Нужен только ключ ElevenLabs</small></button>
      <button class="opt" data-engine="chatgpt" type="button"><b>GPT-Live по подписке</b><small>Ваш ChatGPT Plus или Pro: минуты не оплачиваются, есть лимиты подписки. Нужен вход в «Подключениях»</small></button>
      <button class="opt" data-engine="openai" type="button"><b>GPT-Live</b><small>OpenAI по ключу API. Около 5 центов за минуту разговора</small></button>
      <button class="opt" data-engine="gemini" type="button"><b>Gemini Live</b><small>Google. Примерно вдвое дешевле, есть бесплатный уровень. Нужен ключ Gemini в «Подключениях»</small></button>
      <button class="opt" data-engine="grok" type="button"><b>Grok Voice</b><small>xAI. Около 5 центов за минуту, сам ищет в интернете. Нужен ключ xAI в «Подключениях»</small></button>
    </div>
      <div class="eng" data-for="elevenlabs"><div class="f half"><label>Распознавание речи</label><select id="p-stt-model">${["scribe_v3_live","scribe_v2_realtime"].map(m=>`<option value="${m}"${m===(p.stt_model || "scribe_v2_realtime")?" selected":""}>${m==="scribe_v3_live"?"Scribe v3 Live — определяет конец мысли":"Scribe v2 Realtime"}</option>`).join("")}</select></div><div class="f half"><label>Модель озвучки</label><select id="p-tts-model">${["eleven_v4_turbo","eleven_v4","eleven_v3_conversational","eleven_v3","eleven_flash_v2_5","eleven_multilingual_v2"].map(m=>`<option value="${m}"${m===p.tts_model?" selected":""}>${m}</option>`).join("")}</select></div></div>
      <div class="eng" data-for="eleven_realtime"><div class="f half"><label>Разговорная модель</label><select id="p-eleven-rtmodel">${["eleven_realtime_v1_mini","eleven_realtime_v1_max"].map(m=>`<option value="${m}"${m===p.live_model?" selected":""}>${m.endsWith("max")?"Realtime v1 Max — сложные задачи и инструменты":"Realtime v1 Mini — быстрый разговор"}</option>`).join("")}</select></div></div>
      <div class="eng" data-for="elevenlabs eleven_realtime"><p class="note">Голос выбирается ниже из вашего аккаунта ElevenLabs.</p><button class="btn" id="p-el-refresh" type="button">Обновить голоса ElevenLabs</button></div>
      <div class="eng" data-for="gemini"><div class="f half"><label>Модель ${tip("С бесплатным ключом из AI Studio все эти модели работают без оплаты, но у каждой свой дневной лимит. Когда он кончается, Джарвис сам переходит на следующую модель в списке.")}</label><select id="p-gmodel">${(S.gemini_model_info?.length ? S.gemini_model_info : S.gemini_models.map((id) => ({ id, name: id, note: "" }))).map((m) => `<option value="${esc(m.id)}"${m.id === p.live_model ? " selected" : ""}>${esc(m.name)}${m.note ? ` — ${esc(m.note)}` : ""}</option>`).join("")}</select></div></div>
    <div class="eng" data-for="openai chatgpt elevenlabs"><div class="f half"><label>Текстовая модель ${tip("Чем умнее модель, тем дольше она думает. Для голосового помощника обычно хватает самой быстрой.")}</label><select id="p-model">${S.backend_models.map((m) => `<option${m === p.backend_model ? " selected" : ""}>${esc(m)}</option>`).join("")}</select></div>
      <div class="f"><label>Сколько думать ${tip("Доступные уровни зависят от выбранной текстовой модели: не все модели поддерживают «не думать». Немного — быстрее, средне и долго — для сложных вопросов.")}</label><div class="seg" id="p-effort">${S.efforts.map((e) => `<button type="button" data-v="${e}" class="${e === p.effort ? "on" : ""}">${({ none: "не думать", low: "немного", medium: "средне", high: "долго" })[e] || e}</button>`).join("")}</div></div>
    <label class="sw"><input type="checkbox" id="p-priority"${p.priority ? " checked" : ""}><span class="tr"></span>Приоритет ${tip("Запросы обрабатываются в быстрой очереди OpenAI: ответ быстрее, но стоит дороже.")}</label></div>
    <div class="stack">
      <label class="sw"><input type="checkbox" id="p-web"${p.web_search ? " checked" : ""}><span class="tr"></span>Поиск в интернете ${tip("Для погоды, новостей, курсов и всего свежего. Без него Джарвис знает только то, чему его научили.")}</label></div>
    <div class="f narrow"><label>Конец разговора, с ${tip("Если вы оба молчите столько секунд, разговор закрывается и колонка снова ждёт «Джарвис». Пока разговор открыт, звать по имени не нужно.")}</label><input type="number" id="p-idle" min="2" max="60" step="1" value="${p.idle_s}"></div>`)}
  ${sec("Голос", "Кнопка рядом с голосом — колонка скажет им пробную фразу. Первый раз это около цента, дальше фраза играет с самой колонки бесплатно",
    `<div id="voices" class="vgroups"></div>
    <details id="v-custom"><summary>Создать свой голос по описанию</summary><div class="inner">
      <div class="f"><label>Название</label><input type="text" id="v-name" placeholder="Например, Бархатный"></div>
      <div class="f"><label>Описание ${tip("Тембр, возраст, манера, темп, эмоции. Подражать конкретным людям и персонажам OpenAI не даёт, но описать манеру можно. Создание бесплатное.")}</label><textarea id="v-prompt" placeholder="Низкий спокойный мужской голос, лет сорока, говорит неторопливо, с лёгкой иронией…"></textarea></div>
      <div><button class="btn" id="v-create" type="button">Создать голос</button></div></div></details>
    <div class="f"><label>Громкость голоса ${tip("Насколько голос Джарвиса громче или тише остального звука колонки. Общая громкость колонки — на главной: голос меняется вместе с ней, но мягче — на тихой колонке его всё равно слышно, а на громкой он не орёт громче Алисы. Голосом: «Джарвис, громче/тише».")}<b id="p-vol-v" class="val">${p.volume}%</b></label><input type="range" id="p-vol" min="10" max="100" step="5" value="${p.volume}"></div>`)}
  ${sec("Подсветка", "Цвет кольца у этого ассистента. Нажмите цвет — колонка покажет его на 3 секунды",
    `<div id="p-led">${ledPickerHtml(p.led, "p")}</div>
    <div class="f"><label>Яркость <b id="p-br-v" class="val">${p.led.brightness}%</b></label><input type="range" id="p-br" min="5" max="100" step="5" value="${p.led.brightness}"></div>
    ${S.settings.led?.enabled === false ? `<p class="note">Сейчас подсветка выключена для всех — включается на странице «Колонка».</p>` : ""}`)}
  <div class="savebar"><button class="btn primary" id="p-save">${isNew ? "Создать" : "Сохранить"}</button>
    ${!isNew && !active ? `<button class="btn" id="p-activate" data-tip="Этот ассистент будет отвечать на «Джарвис»">Сделать активным</button>` : ""}
    ${!isNew && !active ? `<button class="btn ghost danger" id="p-del">Удалить</button>` : ""}<span class="dirty hidden" id="dirty">есть несохранённые изменения</span></div>`;

  const mark = () => { setDirty(true); $("#dirty").classList.remove("hidden"); };
  const effortOptions = (model) => S.model_efforts?.[model] || S.efforts;
  const refreshEffortOptions = () => {
    const allowed = effortOptions($("#p-model").value);
    if (!allowed.includes(p.effort)) { p.effort = "low"; mark(); }
    $$("#p-effort button").forEach((b) => {
      b.style.display = allowed.includes(b.dataset.v) ? "" : "none";
      b.classList.toggle("on", b.dataset.v === p.effort);
    });
  };
  refreshEffortOptions();
  $("#p-model").addEventListener("change", () => { p.backend_model = $("#p-model").value; refreshEffortOptions(); mark(); });
  $("#view").oninput = mark;
  $("#view").onchange = mark;
  $("#p-name").addEventListener("input", (e) => { p.name = e.target.value; $("#pav").innerHTML = avatar(p, true); });
  $("#p-color").addEventListener("click", (e) => { const b = e.target.closest("button"); if (!b) return; p.color = +b.dataset.c; $$("#p-color button").forEach((x) => x.classList.toggle("on", x === b)); $("#pav").innerHTML = avatar(p, true); mark(); });
  $("#p-vol").addEventListener("input", (e) => ($("#p-vol-v").textContent = e.target.value + "%"));
  ledPickerBind($("#p-led"), p.led, "p", () => mark());
  $("#p-br").addEventListener("input", (e) => { p.led.brightness = +e.target.value; $("#p-br-v").textContent = e.target.value + "%"; paintLeds($("#p-led"), p.led, "p"); });
  $("#p-br").addEventListener("change", () => { mark(); if (S.speaker.leds) api("POST", "/api/leds/preview", { mode: "listening", color: p.led.listening, brightness: p.led.brightness }).catch(() => {}); });
  $("#p-effort").addEventListener("click", (e) => { const b = e.target.closest("button"); if (!b) return; p.effort = b.dataset.v; $$("#p-effort button").forEach((x) => x.classList.toggle("on", x === b)); mark(); });

  let elevenVoicesList=[],elevenLoaded=false,elevenLoading=false,elevenVoiceError="";
  const loadElevenVoices=async()=>{
    if(elevenLoaded||elevenLoading)return;elevenLoading=true;
    try{const r=await api("GET","/api/elevenlabs/voices");elevenVoicesList=r.voices || [];elevenVoiceError="";}
    catch(err){elevenVoiceError=err.message;}finally{elevenLoading=false;elevenLoaded=true;if(["elevenlabs","eleven_realtime"].includes(p.engine)&&$("#voices"))drawVoices();}
  };
  const drawVoices = () => {
    if(["elevenlabs","eleven_realtime"].includes(p.engine)){
      $("#voices").innerHTML=elevenVoiceError?`<p class="err">${esc(elevenVoiceError)}</p>`:!elevenLoaded?`<p class="note">Загружаю голоса вашего аккаунта ElevenLabs…</p>`:!elevenVoicesList.length?`<p class="note">В аккаунте нет доступных голосов.</p>`:
        `<div class="voices">${elevenVoicesList.map(v=>`<div class="voice${v.voice_id===p.tts_voice?" on":""}" data-v="${esc(v.voice_id)}"><div class="vn"><b>${esc(v.name)}</b><small>${esc([v.category,v.labels?.language].filter(Boolean).join(" · "))}</small></div><button class="ib" type="button" data-play="${esc(v.voice_id)}" title="Послушать на колонке" aria-label="Послушать голос">${icon("play","solid")}</button></div>`).join("")}</div>`;
      return;
    }
    const gem = p.engine !== "openai";
    const groups = [...(({ gemini: S.gemini_voice_groups, chatgpt: S.chatgpt_voice_groups, grok: S.grok_voice_groups })[p.engine] || S.voice_groups)];
    if (!gem && S.live_voices.length) groups.unshift({ name: "Мои голоса", voices: S.live_voices.map((v) => ({ ID: v.id, Note: v.prompt, mine: true, name: v.name })) });
    $("#voices").innerHTML = groups.map((g) => `<div class="vgroup"><div class="gh">${esc(g.name)}</div><div class="voices">${g.voices.map((v) => `
      <div class="voice${v.ID === p.voice ? " on" : ""}" data-v="${esc(v.ID)}"><div class="vn"><b>${esc(v.name || v.ID)}</b><small>${esc(v.Note)}</small></div>
      <button class="ib" type="button" data-play="${esc(v.ID)}" title="Послушать на колонке" aria-label="Послушать на колонке">${icon("play", "solid")}</button>${v.mine ? `<button class="ib" type="button" data-delv="${esc(v.ID)}" title="Удалить голос" aria-label="Удалить голос">${icon("x")}</button>` : ""}</div>`).join("")}</div></div>`).join("");
  };
  // движок: свои модели и свои голоса
  const drawEngine = () => {
    $$("#p-engine .opt").forEach((b) => b.classList.toggle("on", b.dataset.engine === p.engine));
    $$(".eng").forEach((el) => el.classList.toggle("hidden", !el.dataset.for.split(" ").includes(p.engine)));
    $("#v-custom").classList.toggle("hidden", p.engine !== "openai");
    if(["elevenlabs","eleven_realtime"].includes(p.engine)){drawVoices();loadElevenVoices();return;}
    const known = (({ gemini: S.gemini_voice_groups, chatgpt: S.chatgpt_voice_groups, grok: S.grok_voice_groups })[p.engine] || S.voice_groups).flatMap((g) => g.voices.map((v) => v.ID.toLowerCase()));
    const mine = S.live_voices.some((v) => v.id === p.voice);
    if (!known.includes(String(p.voice).toLowerCase()) && !(p.engine === "openai" && mine)) p.voice = ({ gemini: "Charon", chatgpt: "cove", grok: "rex" })[p.engine] || "cedar";
    drawVoices();
  };
  drawEngine();
  $("#p-el-refresh").addEventListener("click",()=>{elevenLoaded=false;elevenVoiceError="";drawVoices();loadElevenVoices();});
  $("#p-engine").addEventListener("click", (e) => { const b = e.target.closest(".opt"); if (!b || b.dataset.engine === p.engine) return; p.engine = b.dataset.engine; drawEngine(); mark(); });
  $("#voices").addEventListener("click", async (e) => {
    const play = e.target.closest("[data-play]"), del = e.target.closest("[data-delv]"), card = e.target.closest(".voice");
    if (play) { // последнее нажатие побеждает: ответ прошлой пробы не перекрывает новую
      e.stopPropagation(); const n = ++previewSeq; play.disabled = true;
      try {
        const r = await api("POST", "/api/preview", { voice: play.dataset.play, engine: p.engine, tts_model: $("#p-tts-model").value });
        if (n === previewSeq && !r.interrupted) toast(r.cached ? "Играет на колонке" : "Прозвучало на колонке и сохранено");
      } catch (err) { if (n === previewSeq) toast(err.message, true); } finally { play.disabled = false; }
      return;
    }
    if (del) { e.stopPropagation(); if (!confirm("Удалить этот голос из списка?")) return; await act(del, async () => { await api("DELETE", "/api/voices?id=" + encodeURIComponent(del.dataset.delv)); await reload(); drawVoices(); }); return; }
    if (card) { if(["elevenlabs","eleven_realtime"].includes(p.engine))p.tts_voice=card.dataset.v;else p.voice = card.dataset.v; drawVoices(); mark(); }
  });
  $("#v-create").addEventListener("click", (e) => act(e.target, async () => {
    const r = await api("POST", "/api/voices", { name: $("#v-name").value.trim(), prompt: $("#v-prompt").value.trim() });
    await reload(); p.voice = r.id; drawVoices(); mark();
  }, "Голос создан — послушайте его кнопкой рядом"));

  $("#p-save").addEventListener("click", (e) => act(e.target, async () => {
    Object.assign(p, { name: $("#p-name").value.trim(), prompt: $("#p-prompt").value, backend_model: $("#p-model").value,
      priority: $("#p-priority").checked, web_search: $("#p-web").checked, idle_s: +$("#p-idle").value || 8, mode: "live",
      volume: +$("#p-vol").value,
      live_model: p.engine === "gemini" ? $("#p-gmodel").value : "" });
    p.mode=p.engine==="elevenlabs"?"classic":"live";
    if(p.engine==="eleven_realtime"){p.live_model=$("#p-eleven-rtmodel").value;if(!p.tts_voice)throw new Error("Выберите голос ElevenLabs");}
    if(p.engine==="elevenlabs"){p.llm_model=p.backend_model;p.tts_model=$("#p-tts-model").value;p.stt_engine="elevenlabs";p.stt_model=$("#p-stt-model").value;if(!p.tts_voice)throw new Error("Выберите голос ElevenLabs");}
    const r = await api("PUT", "/api/profile", p);
    setDirty(false); await reload();
    if (isNew) go("#p/" + r.id); else route();
  }, "Сохранено"));
  $("#p-activate")?.addEventListener("click", (e) => act(e.target, async () => { await api("POST", "/api/active", { id: p.id }); await reload(); route(); }, `Теперь отвечает «${p.name}»`));
  $("#p-del")?.addEventListener("click", (e) => { if (!confirm(`Удалить ассистента «${p.name}»?`)) return; act(e.target, async () => { await api("DELETE", "/api/profile?id=" + encodeURIComponent(p.id)); setDirty(false); await reload(); go("#home"); }, "Удалено"); });
}
