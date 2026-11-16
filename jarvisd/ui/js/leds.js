// Подсветка: выбор цвета и лаборатория цвета.
import { $, $$, api, esc, jelly, toast } from "./core.js";
import { S } from "./main.js";

// ---------------------------------------------------------------- подсветка: выбор цвета
export const LED_SWATCHES = ["#ffffff", "#4fd1c5", "#00b4ff", "#3d6bff", "#5a2dff", "#b04dff", "#ff4d8d", "#ff5a36", "#ffb020", "#3ddc84"];
export const LED_STATES = [["listening", "Слушает", "кольцо плавно загорается и дышит"], ["thinking", "Думает", "огонёк бежит по кругу"], ["speaking", "Говорит", "вспыхивает в такт голосу"]];

// строка выбора цвета: живое мини-кольцо + образцы + свой цвет
export function ledPickerHtml(led, prefix) {
  return `<div class="ledrows">${LED_STATES.map(([k, n, hint]) => `
    <div class="ledrow" data-k="${k}">
      <span class="ledring ${k}" data-ring="${prefix}-${k}"></span>
      <div class="ledname"><b>${n}</b><small>${hint}</small></div>
      <div class="swatches">${LED_SWATCHES.map((c) => `<button type="button" class="swatch${c === led[k] ? " on" : ""}" data-c="${c}" data-k="${k}" aria-label="Цвет ${c}"></button>`).join("")}
        <label class="swatch custom${LED_SWATCHES.includes(led[k]) ? "" : " on"}" aria-label="Свой цвет"><input type="color" data-k="${k}" value="${esc(led[k])}"></label></div>
    </div>`).join("")}</div>`;
}

// раскрасить образцы и кольца (цвета — через CSSOM: встроенные style="" запрещены политикой страницы)
export function paintLeds(root, led, prefix) {
  $$(".swatch[data-c]", root).forEach((b) => b.style.setProperty("--c", b.dataset.c));
  LED_STATES.forEach(([k]) => {
    const ring = $(`[data-ring="${prefix}-${k}"]`, root);
    if (ring) { ring.style.setProperty("--c", led[k]); ring.style.setProperty("--b", String(0.35 + 0.65 * (led.brightness / 100))); }
    const custom = $(`.ledrow[data-k="${k}"] .swatch.custom`, root);
    if (custom) custom.style.setProperty("--c", led[k]);
  });
}

// обработчики: onPick(k, color) — выбран цвет; на колонке он сразу загорается на 3 секунды
export function ledPickerBind(root, led, prefix, onPick) {
  paintLeds(root, led, prefix);
  const pick = (k, c) => {
    led[k] = c;
    $$(`.ledrow[data-k="${k}"] .swatch`, root).forEach((s) => s.classList.toggle("on", s.dataset.c === c || (s.classList.contains("custom") && !LED_SWATCHES.includes(c))));
    paintLeds(root, led, prefix);
    onPick(k, c);
    if (S.speaker.leds) api("POST", "/api/leds/preview", { mode: k, color: c, brightness: led.brightness }).catch(() => {});
  };
  root.addEventListener("click", (e) => { const s = e.target.closest(".swatch[data-c]"); if (s) pick(s.dataset.k, s.dataset.c); });
  root.addEventListener("input", (e) => { if (e.target.matches('input[type=color]')) { led[e.target.dataset.k] = e.target.value; paintLeds(root, led, prefix); } });
  root.addEventListener("change", (e) => { if (e.target.matches('input[type=color]')) pick(e.target.dataset.k, e.target.value); });
}

// ---------------------------------------------------------------- подсветка: «лаборатория цвета» — кольцо оттенков, HEX, насыщенность
export const hsv2hex = (h, s, v = 1) => {
  const f = (n) => { const k = (n + h / 60) % 6; return v - v * s * Math.max(0, Math.min(k, 4 - k, 1)); };
  return "#" + [f(5), f(3), f(1)].map((x) => Math.round(x * 255).toString(16).padStart(2, "0")).join("");
};
export const hex2hs = (hex) => {
  const m = /^#?([0-9a-f]{6})$/i.exec(hex.trim());
  if (!m) return null;
  const n = parseInt(m[1], 16), r = (n >> 16 & 255) / 255, g = (n >> 8 & 255) / 255, b = (n & 255) / 255;
  const mx = Math.max(r, g, b), mn = Math.min(r, g, b), d = mx - mn;
  let h = 0;
  if (d) h = mx === r ? ((g - b) / d) % 6 : mx === g ? (b - r) / d + 2 : (r - g) / d + 4;
  return { h: (h * 60 + 360) % 360, s: mx ? d / mx : 0 };
};
export const LED_PRESETS = ["#ffffff", "#ff3b30", "#ff9500", "#ffcc00", "#34c759", "#00c7be", "#00b4ff", "#3d6bff", "#5a2dff", "#ff2d92"];

export function ledLabHtml() {
  return `<div class="ledlab">
    <div class="hue-ring" id="lp-ring" role="slider" tabindex="0" aria-label="Оттенок"><div class="hue-knob" id="lp-knob"></div>
      <div class="hue-core" id="lp-core" data-fx="listening"><span class="core-glow"></span><span class="core-dot"></span></div></div>
    <div class="ledlab-side">
      <div class="seg" id="lp-fx"><button type="button" data-fx="listening" class="on">Дыхание</button><button type="button" data-fx="thinking">Огонёк по кругу</button><button type="button" data-fx="speaking">Пульс</button></div>
      <div class="hexrow"><span class="hexdot" id="lp-dot"></span><input type="text" id="lp-hex" value="#00B4FF" maxlength="7" spellcheck="false" autocomplete="off" aria-label="Цвет в HEX"><span class="meta">впишите цвет или крутите кольцо</span></div>
      <div class="f"><label>Насыщенность <b class="val" id="lp-sat-v"></b></label><div id="lp-sat"></div></div>
      <div class="f"><label>Яркость <b class="val" id="lp-br-v"></b></label><div id="lp-br"></div></div>
      <div class="presets" id="lp-pre">${LED_PRESETS.map((c) => `<button type="button" class="swatch" data-c="${c}" aria-label="Цвет ${c}"></button>`).join("")}</div>
      <div class="row"><button class="btn primary" id="lp-go">Зажечь на 30 секунд</button><button class="btn ghost" id="lp-off">Погасить</button><span class="meta" id="lp-live"></span></div>
    </div></div>`;
}

export function ledLabBind() {
  const st = { h: 197, s: 1, br: 70, fx: "listening", lit: false };
  const ring = $("#lp-ring"), knob = $("#lp-knob"), core = $("#lp-core");
  let sendT = 0, lastSend = 0;
  const color = () => hsv2hex(st.h, st.s);
  const send = () => {
    lastSend = Date.now();
    st.lit = true;
    $("#lp-live").textContent = "горит на колонке";
    api("POST", "/api/leds/preview", { mode: st.fx, color: color(), brightness: st.br, seconds: 30 }).catch((e) => toast(e.message, true));
  };
  const sendSoon = () => { clearTimeout(sendT); const wait = Math.max(0, 140 - (Date.now() - lastSend)); sendT = setTimeout(send, wait); };
  const paint = (fromHex) => {
    const c = color();
    const a = ((st.h - 90) * Math.PI) / 180;
    knob.style.setProperty("left", `${50 + 40 * Math.cos(a)}%`);
    knob.style.setProperty("top", `${50 + 40 * Math.sin(a)}%`);
    knob.style.setProperty("--c", hsv2hex(st.h, 1));
    core.style.setProperty("--c", c);
    core.style.setProperty("--b", String(0.35 + 0.65 * (st.br / 100)));
    core.dataset.fx = st.fx;
    $("#lp-dot").style.setProperty("--c", c);
    if (!fromHex) $("#lp-hex").value = c.toUpperCase();
    $("#lp-sat-v").textContent = Math.round(st.s * 100) + "%";
    $("#lp-br-v").textContent = st.br + "%";
    $$("#lp-pre .swatch").forEach((b) => b.classList.toggle("on", b.dataset.c.toLowerCase() === c.toLowerCase()));
  };
  const hueAt = (e) => { const r = ring.getBoundingClientRect(); return (Math.atan2(e.clientY - (r.top + r.height / 2), e.clientX - (r.left + r.width / 2)) * 180 / Math.PI + 90 + 360) % 360; };
  let drag = false;
  ring.addEventListener("pointerdown", (e) => { if (e.target.closest(".hue-core")) return; drag = true; ring.setPointerCapture(e.pointerId); st.h = hueAt(e); paint(); sendSoon(); });
  ring.addEventListener("pointermove", (e) => { if (!drag) return; st.h = hueAt(e); paint(); sendSoon(); });
  ring.addEventListener("pointerup", () => { drag = false; });
  ring.addEventListener("keydown", (e) => { const d = { ArrowRight: 5, ArrowUp: 5, ArrowLeft: -5, ArrowDown: -5 }[e.key]; if (d == null) return; e.preventDefault(); st.h = (st.h + d + 360) % 360; paint(); sendSoon(); });
  core.addEventListener("click", send);
  $$("#lp-pre .swatch").forEach((b) => b.style.setProperty("--c", b.dataset.c));
  $("#lp-pre").addEventListener("click", (e) => { const b = e.target.closest("[data-c]"); if (!b) return; const v = hex2hs(b.dataset.c); st.h = v.h; st.s = v.s; satJ.set(Math.round(st.s * 100), true); paint(); send(); });
  $("#lp-hex").addEventListener("input", (e) => {
    let v = e.target.value.trim();
    if (!v.startsWith("#")) v = "#" + v;
    const hs = hex2hs(v);
    e.target.classList.toggle("bad", !hs && v.length === 7);
    if (!hs) return;
    st.h = hs.h; st.s = hs.s; satJ.set(Math.round(st.s * 100), true); paint(true); sendSoon();
  });
  const satJ = jelly($("#lp-sat"), { label: "Насыщенность", min: 0, max: 100, step: 1, value: 100, format: (v) => v + "%", onInput: (v) => { st.s = v / 100; paint(); sendSoon(); }, onChange: (v) => { st.s = v / 100; paint(); send(); } });
  jelly($("#lp-br"), { label: "Яркость", min: 5, max: 100, step: 5, value: st.br, format: (v) => v + "%", onInput: (v) => { st.br = v; paint(); sendSoon(); }, onChange: (v) => { st.br = v; paint(); send(); } });
  $$("#lp-fx button").forEach((b) => b.addEventListener("click", () => { $$("#lp-fx button").forEach((x) => x.classList.toggle("on", x === b)); st.fx = b.dataset.fx; paint(); send(); }));
  $("#lp-go").addEventListener("click", send);
  $("#lp-off").addEventListener("click", () => { st.lit = false; $("#lp-live").textContent = ""; api("POST", "/api/leds/preview", { mode: "off" }).catch(() => {}); });
  paint();
}
