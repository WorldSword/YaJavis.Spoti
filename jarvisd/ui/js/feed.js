// Переписка на главной.
import { Motion } from "./core.js";

// ---------------------------------------------------------------- переписка на главной
// Строки переписки живут по ключам и меняются на месте: опрос раз в полсекунды не пересоздаёт пузыри,
// поэтому начатая анимация не обрывается, а уже показанные слова не мигают.
// Ответ Джарвиса «печатается»: пузырь открывается из пилюли «Думаю» и растёт пружиной (260/26),
// каждое новое слово поднимается и проясняется на своей пружине (340/26) с шагом 70 мс, а высота
// пузыря идёт строка за строкой вслед за последним показанным словом. Если человек внизу переписки,
// окно каждый кадр следует за растущим пузырём — текст не уходит за край.
export const FEED = { el: null, rows: new Map(), first: true, stick: true };

export function feedReset(feed) {
  for (const st of FEED.rows.values()) if (st.anim) Motion.remove(st.anim.comp);
  Object.assign(FEED, { el: feed, rows: new Map(), first: true, stick: true });
  if (!feed) return;
  feed.addEventListener("scroll", () => { FEED.stick = feed.scrollHeight - feed.scrollTop - feed.clientHeight < 80; }, { passive: true });
}

export function feedFollow() {
  const f = FEED.el;
  if (f && FEED.stick && f.isConnected) f.scrollTop = f.scrollHeight;
}

export function feedSync(feed, rows) {
  if (FEED.el !== feed) feedReset(feed);
  const animate = !FEED.first && !Motion.reduce, keep = new Set();
  let prev = null;
  for (const r of rows) {
    keep.add(r.key);
    let st = FEED.rows.get(r.key);
    if (!st) {
      const el = document.createElement("div");
      st = { el, cls: "", html: null, text: null, meta: null, anim: null };
      FEED.rows.set(r.key, st);
      feedPaint(st, r, false);
      if (animate && !r.answer) el.classList.add("new");   // ответ не «выпрыгивает», а печатается
      if (animate && r.answer) { st.text = null; el.querySelector(":scope > .t").textContent = ""; }
    } else if (st.cls !== r.cls || st.html !== (r.html ?? null) || st.text !== (r.answer ?? null) || st.meta !== (r.meta ?? null)) {
      const old = animate ? { w: st.el.offsetWidth, h: st.el.offsetHeight } : null;
      feedPaint(st, r, animate, old);
    }
    const after = prev ? prev.el : null;
    if (st.el.parentNode !== feed || st.el.previousElementSibling !== after) feed.insertBefore(st.el, after ? after.nextSibling : feed.firstChild);
    if (animate && r.answer && st.text === null) feedAnswer(st, r.answer, null);   // новый ответ — уже в DOM, можно мерить
    prev = st;
  }
  for (const [k, st] of FEED.rows) if (!keep.has(k)) { if (st.anim) Motion.remove(st.anim.comp); st.el.remove(); FEED.rows.delete(k); }
  FEED.first = false;
  feedFollow();
}

// feedPaint — содержимое строки. old — размер до правки: из него пузырь плавно перейдёт в новый.
export function feedPaint(st, r, animate, old) {
  const el = st.el;
  if (st.cls !== r.cls) {
    el.className = r.cls + (st.anim ? " growing" : "");
    st.cls = r.cls;
    if (r.label) el.setAttribute("aria-label", r.label); else el.removeAttribute("aria-label");
  }
  if (!r.answer) {
    if (st.html !== r.html) { el.innerHTML = r.html; st.html = r.html; st.text = st.meta = null; }
    if (animate && old) feedMorph(st, old);
    return;
  }
  // ответ: <span class="t">слова</span><span class="m">время</span>
  if (st.html !== null || !el.querySelector(":scope > .t")) {
    el.innerHTML = `<span class="t"></span><span class="m"></span>`;
    st.html = null; st.text = null;
  }
  if (st.meta !== r.meta) { el.querySelector(":scope > .m").innerHTML = r.meta; st.meta = r.meta; }
  if (st.text === r.answer) { if (animate && old) feedMorph(st, old); return; }
  if (!animate || !old) { el.querySelector(":scope > .t").textContent = r.answer; st.text = r.answer; return; }
  feedAnswer(st, r.answer, old);
}

export const feedWords = (t) => t.split(/(\s+)/).filter((x) => x !== "");

// feedAnswer — допечатать ответ: дописалось в конец — новые слова всплывают по очереди после уже идущих;
// текст переписался целиком — показываем его заново.
export function feedAnswer(st, text, old) {
  const t = st.el.querySelector(":scope > .t");
  let add = text;
  if (st.text && text.startsWith(st.text)) add = text.slice(st.text.length);
  else t.textContent = "";
  st.text = text;
  const spans = [];
  for (const part of feedWords(add)) {
    if (/^\s+$/.test(part)) { t.append(part); continue; }
    const w = document.createElement("span");
    w.className = "wd"; w.textContent = part; w.style.opacity = "0";
    t.append(w); spans.push(w);
  }
  feedMorph(st, old || { w: Math.min(st.el.offsetWidth, 96), h: Math.min(st.el.offsetHeight, 36) }, spans);
}

// feedMorph — пузырь плавно меняет размер и показывает новые слова, продолжая уже идущую анимацию.
// Раскладка текста сразу окончательная (ширина пузыря в потоке — полная), поэтому показанные слова никогда
// не перескакивают между строками. Видимую ширину открывает маска clip-path за последним показанным словом,
// высоту — пружина (overflow: hidden): строка за строкой. Подпись со временем едет вместе с правым краем.
export function feedMorph(st, old, spans = []) {
  const el = st.el, meta = el.classList.contains("ans") ? el.querySelector(":scope > .m") : null;
  let a = st.anim;
  el.style.height = el.style.clipPath = "";
  if (meta) meta.style.right = "";
  if (!a) el.classList.add("growing");
  if (meta) el.style.minWidth = meta.offsetWidth + 24 + "px";   // подпись в анимации вне потока — ширину держим сами
  const natural = { w: el.offsetWidth, h: el.offsetHeight };
  if (!a) {
    if (!spans.length && Math.abs(old.w - natural.w) < 1 && Math.abs(old.h - natural.h) < 1) {
      el.classList.remove("growing"); el.style.minWidth = "";
      return;
    }
    const cs = getComputedStyle(el);
    a = st.anim = {
      W: Motion.S(Math.min(old.w, natural.w), 260, 26), H: Motion.S(old.h, 260, 26), words: [], clock: 0, next: 0, natural,
      left: el.classList.contains("me"), meta, padB: parseFloat(cs.paddingBottom) || 8, padR: parseFloat(cs.paddingRight) || 12,
      round: `${cs.borderTopLeftRadius} ${cs.borderTopRightRadius} ${cs.borderBottomRightRadius} ${cs.borderBottomLeftRadius}`, reveal: false,
    };
    a.comp = {
      frame(dt) {
        a.clock += dt;
        for (const w of a.words) {
          if (!w.on && a.clock >= w.at) { w.on = true; w.s.t = 1; a.reveal = true; }
          if (w.on && !w.fin) Motion.step(w.s, dt);
        }
        Motion.step(a.W, dt); Motion.step(a.H, dt);
      },
      render() {
        if (!el.isConnected) return;
        if (a.reveal) { a.reveal = false; feedTargets(a); }
        feedClip(el, a);
        for (const w of a.words) {
          if (!w.on || w.fin) continue;
          const v = Motion.clamp(w.s.x, 0, 1.2), o = Motion.clamp(v, 0, 1);
          w.el.style.opacity = o.toFixed(3);
          w.el.style.transform = `translateY(${((1 - w.s.x) * 6).toFixed(2)}px)`;
          w.el.style.filter = v < 0.98 ? `blur(${((1 - o) * 4).toFixed(2)}px)` : "none";
          if (Motion.settled(w.s)) { w.fin = true; w.el.style.cssText = ""; }
        }
        feedFollow();
      },
      done() {
        const fin = !el.isConnected || (a.words.every((w) => w.fin) && Motion.settled(a.W) && Motion.settled(a.H));
        if (fin) {
          if (el.isConnected) {
            el.style.height = el.style.clipPath = el.style.minWidth = "";
            if (a.meta) a.meta.style.right = "";
            el.classList.remove("growing");
            for (const w of a.words) w.el.style.cssText = "";
          }
          if (st.anim === a) st.anim = null;
          feedFollow();
        }
        return fin;
      },
    };
  }
  a.natural = natural;
  a.next = Math.max(a.next, a.clock);
  for (const w of spans) { a.words.push({ el: w, s: Motion.S(0, 340, 26), on: false, fin: false, at: a.next }); a.next += 0.07; }
  feedTargets(a);
  feedClip(el, a);
  Motion.add(a.comp);   // уже в цикле — просто будит его
}

// Цели пружин: ширина — до правого края самого дальнего показанного слова (и не уже подписи),
// высота — до низа последнего показанного слова; когда показано всё — полный размер пузыря.
export function feedTargets(a) {
  let last = null, all = true, right = 0;
  for (const w of a.words) {
    if (!w.on) { all = false; continue; }
    last = w; right = Math.max(right, w.el.offsetLeft + w.el.offsetWidth);
  }
  if (all) { a.W.t = a.natural.w; a.H.t = a.natural.h; return; }
  const metaW = a.meta ? a.meta.offsetWidth + 2 * a.padR : 0;
  a.W.t = Math.min(a.natural.w, Math.max(a.W.t, right + a.padR, metaW));
  if (last) a.H.t = Math.min(a.natural.h, Math.max(a.H.t, last.el.offsetTop + last.el.offsetHeight + a.padB));
}

export function feedClip(el, a) {
  const cut = Math.max(0, a.natural.w - Math.min(a.W.x, a.natural.w));
  el.style.height = a.H.x.toFixed(1) + "px";
  el.style.clipPath = cut < 0.5 ? "" : a.left ? `inset(0 0 0 ${cut.toFixed(1)}px round ${a.round})` : `inset(0 ${cut.toFixed(1)}px 0 0 round ${a.round})`;
  if (a.meta) a.meta.style.right = (a.padR + cut).toFixed(1) + "px";
}
