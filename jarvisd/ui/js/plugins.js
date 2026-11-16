// Плагины.
import { $, $$, ICONS, act, api, esc, icon, tip, toast, upload } from "./core.js";
import { S, page } from "./main.js";

// ---------------------------------------------------------------- плагины
// Ядро Джарвиса лёгкое, остальное ставится плагинами. Страница — переключатель «Официальные / Неофициальные»,
// внутри — разделы по категориям (автор выбирает категорию в plugin.json, на GitHub — темой yajarvis-<категория>).
// Установленные и каталог — одни карточки: у установленного переключатель, настройки, обновление, удаление.
// Перед первым неофициальным — предупреждение с «больше не показывать».
export const PL = { side: "official", cat: "all", mine: false, data: null, list: null, catErr: "", open: "", loading: false };
export const PL_KIND = { js: "лёгкий, JS", native: "программа" };
export const plIcon = (n) => icon(ICONS[n] ? n : "puzzle");
export const plSize = (b) => (b < 1048576 ? `${Math.max(1, Math.round(b / 1024))} КБ` : `${(b / 1048576).toFixed(1).replace(".", ",")} МБ`);
export const plBadge = (official) => official ? `<span class="plg-b ok">${icon("shield")}официальный</span>` : `<span class="plg-b warn">неофициальный</span>`;
const plCats = () => PL.data?.categories || [{ id: "other", name: "Другое" }];
const plCatName = (id) => plCats().find((c) => c.id === id)?.name || "Другое";

export function viewPlugins() {
  $("#view").innerHTML = `<header class="ph ph-row"><div><h1>Плагины</h1><p class="sub">Джарвис сам по себе лёгкий: всё, что нужно не всем, ставится плагинами. Официальные делает автор проекта, неофициальные — кто угодно на GitHub.</p></div>
    <div class="actions"><label class="btn" data-tip="Архив .zip с plugin.json — например, скачанный из релиза плагина">${icon("upload")}Из файла<input type="file" id="pl-file" accept=".zip,application/zip" hidden></label></div></header>
  <div class="row plg-bar"><div class="seg" id="pl-side"><button type="button" data-side="official">${icon("shield")}Официальные</button><button type="button" data-side="community">Неофициальные</button></div>
    <span class="spacer"></span><label class="sw plg-mine"><input type="checkbox" id="pl-mine"${PL.mine ? " checked" : ""}><span class="tr"></span>Только установленные</label></div>
  <div class="chips plg-cats" id="pl-cats"></div>
  <div id="pl-box"></div>
  <div class="row plg-foot"><span class="meta" id="pl-free"></span><span class="spacer"></span><button class="btn ghost sm" type="button" id="pl-refresh">${icon("search")}Обновить каталог</button></div>
  <div class="modal hidden" id="pl-modal" role="dialog" aria-modal="true" aria-labelledby="pl-mt"><div class="modal-bg" data-close></div><div class="modal-card cn-card" id="pl-card"></div></div>`;
  const view = $("#view");
  view.onclick = plClick;
  view.onchange = plChange;
  view.oninput = null;
  view.onkeydown = (e) => { if (page === "plugins" && e.key === "Escape" && !$("#pl-modal").classList.contains("hidden")) plClose(); };
  plTabs();
  plLoad();
}

export function plTabs() {
  $$("#pl-side button").forEach((b) => b.classList.toggle("on", b.dataset.side === PL.side));
}

export async function plLoad() {
  try { PL.data = await api("GET", "/api/plugins"); } catch (e) { toast(e.message, true); return; }
  if (!PL.list) plCatalog(false);
  plDraw();
}

export async function plCatalog(fresh) {
  PL.loading = true;
  if (page === "plugins") plDraw();
  try { const r = await api("GET", "/api/plugins/catalog" + (fresh ? "?fresh=1" : "")); PL.list = r.items || []; PL.catErr = r.error || ""; }
  catch (e) { PL.catErr = e.message; PL.list = PL.list || []; }
  PL.loading = false;
  if (page === "plugins") plDraw();
}

export function plInstalled(id) { return (PL.data?.installed || []).find((p) => p.id === id); }
// plCatItem — запись каталога для установленного плагина (официальный — по id, сторонний — по репозиторию)
export function plCatItem(p) { return (PL.list || []).find((c) => c.id === p.id && (p.official ? c.official : c.repo === p.source)); }

// plItems — карточки стороны: каталог плюс установленное, чего в каталоге нет (из файла, снятое с каталога).
function plItems(side) {
  const official = side === "official";
  const inst = PL.data?.installed || [];
  const out = (PL.list || []).filter((c) => c.official === official).map((c) => ({ ...c, inst: inst.find((p) => p.id === c.id && (c.official ? p.official : p.source === c.repo)) }));
  for (const p of inst) {
    if (p.official !== official || out.some((x) => x.inst === p)) continue;
    out.push({ id: p.id, repo: p.source, name: p.name, description: p.description, category: p.category, icon: p.icon, kind: p.kind,
      version: p.version, author: p.author, official: p.official, url: p.homepage, inst: p });
  }
  for (const x of out) if (!plCats().some((c) => c.id === x.category)) x.category = "other";
  return out;
}

export function plDraw() {
  const box = $("#pl-box");
  if (!box || !PL.data) return;
  $("#pl-free").textContent = PL.data.free_mb ? `свободно на колонке ${PL.data.free_mb} МБ` : "";
  const refresh = $("#pl-refresh");
  if (refresh) refresh.disabled = PL.loading;
  let items = plItems(PL.side);
  if (PL.mine) items = items.filter((x) => x.inst);
  // категории: «Все» и те, где что-то есть; выбранная остаётся, даже если опустела
  const count = (id) => items.filter((x) => x.category === id).length;
  const cats = plCats().filter((c) => count(c.id) || c.id === PL.cat);
  if (PL.cat !== "all" && !cats.some((c) => c.id === PL.cat)) PL.cat = "all";
  $("#pl-cats").innerHTML = [{ id: "all", name: "Все" }, ...cats].map((c) => `<button type="button" class="chip plg-cat${PL.cat === c.id ? " on" : ""}" data-pl-cat="${c.id}"${c.hint ? ` data-tip="${esc(c.hint)}"` : ""}>${esc(c.name)} <b>${c.id === "all" ? items.length : count(c.id)}</b></button>`).join("");
  const note = PL.side === "community" ? `<p class="note plg-warn">${icon("shield")}<span>Неофициальные плагины пишут другие люди. Автор YaJarvis их не проверяет и за них не отвечает: ставьте то, чему доверяете.
    Хотите сделать свой — <a class="lnk" href="https://github.com/${esc(PL.data.repo)}/blob/main/docs/PLUGINS.md" target="_blank" rel="noopener">инструкция</a>.</span></p>` : "";
  const err = PL.catErr ? `<p class="note bad">Каталог загрузился не полностью: ${esc(PL.catErr)}</p>` : "";
  if (!items.length) {
    const empty = PL.loading ? "Загружаю каталог…" : PL.mine ? "Здесь пока ничего не установлено." :
      PL.side === "official" ? "Каталог не загрузился — колонке нужен интернет." : "Пока никто не опубликовал. Будьте первым!";
    box.innerHTML = `${note}${err}<div class="card plg-empty">${icon("puzzle")}<p class="muted">${empty}</p></div>`;
    return;
  }
  const show = PL.cat === "all" ? plCats().filter((c) => count(c.id)) : cats.filter((c) => c.id === PL.cat);
  box.innerHTML = note + err + show.map((c) => {
    const list = items.filter((x) => x.category === c.id).sort((a, b) => (!!b.inst - !!a.inst) || (b.recommended ? 1 : 0) - (a.recommended ? 1 : 0) || (b.stars || 0) - (a.stars || 0) || a.name.localeCompare(b.name));
    return `<section class="cn-group"><div class="cn-gh"><h2>${esc(c.name)}</h2><span class="meta">${esc(c.hint || "")}</span></div>
      <div class="chars cn-grid">${list.map(plCard).join("")}</div></section>`;
  }).join("");
}

export function plCard(x) {
  const p = x.inst;
  const upd = p && x.version && p.version && x.version !== p.version && x.repo;
  const meta = `<div class="plg-meta">${plBadge(x.official)}${(p?.version || x.version) ? `<span>v${esc(p?.version || x.version)}</span>` : ""}
    ${x.kind ? `<span>${PL_KIND[x.kind] || esc(x.kind)}</span>` : ""}${p ? `<span>${plSize(p.size || 0)}</span>` : ""}
    ${x.author ? `<span>${esc(x.author)}</span>` : ""}${x.stars ? `<span>★ ${x.stars}</span>` : ""}
    ${x.url ? `<a class="lnk" href="${esc(x.url)}" target="_blank" rel="noopener">${icon("ext")}код</a>` : ""}</div>`;
  if (!p) return `<article class="card cn plg">
    <div class="cn-h"><span class="char-av cn-av">${plIcon(x.icon)}</span><h2>${esc(x.name)}</h2></div>
    <p class="cn-t">${esc(x.description || "Без описания")}</p>${meta}
    <div class="cn-f"><span class="spacer"></span><button class="btn sm primary" type="button" data-pl-install="${esc(x.repo)}" data-id="${esc(x.id)}" data-official="${x.official ? 1 : ""}">${icon("plus")}Установить</button></div>
  </article>`;
  const state = p.error ? `<span class="st ${p.error.startsWith("заработает") ? "warn" : "bad"}">${esc(p.error)}</span>` : !p.enabled ? `<span class="st">выключен</span>`
    : p.running ? `<span class="st ok">${esc(p.status || "работает")}</span>` : `<span class="st warn">не запущен</span>`;
  return `<article class="card cn plg on" data-pl="${esc(p.id)}">
    <div class="cn-h"><span class="char-av cn-av">${plIcon(p.icon)}</span><h2>${esc(p.name)}</h2><span class="spacer"></span>
      <label class="sw" data-tip="${p.enabled ? "Выключить" : "Включить"}"><input type="checkbox" data-pl-on="${esc(p.id)}"${p.enabled ? " checked" : ""}><span class="tr"></span></label></div>
    <p class="cn-t">${esc(p.description || "")}</p>${meta}
    <div class="cn-f">${state}<span class="spacer"></span>
      ${upd ? `<button class="btn sm primary" type="button" data-pl-update="${esc(p.id)}">Обновить до ${esc(x.version)}</button>` : ""}
      ${(p.settings || []).length ? `<button class="btn sm" type="button" data-pl-set="${esc(p.id)}">${icon("tune")}Настроить</button>` : ""}
      <button class="btn sm ghost danger" type="button" data-pl-del="${esc(p.id)}" aria-label="Удалить" data-tip="Удалить">${icon("x")}</button></div>
  </article>`;
}

// plWarn — предупреждение перед неофициальным плагином; true — ставим.
export function plWarn(name) {
  if (PL.data?.warned || S.settings.plugins_warned) return Promise.resolve(true);
  return new Promise((done) => {
    const card = $("#pl-card");
    card.innerHTML = `<div class="cn-mh"><span class="char-av cn-av warn">${icon("shield")}</span><div class="char-n"><h2 id="pl-mt">Неофициальный плагин</h2>
        <span class="st warn">${esc(name)}</span></div></div>
      <p>Этот плагин написал не автор YaJarvis, и никто его не проверял. Он будет работать на вашей колонке и сможет ходить в интернет,
        а нативный плагин — вообще всё, что может программа на колонке.</p>
      <p class="muted">Автор YaJarvis не отвечает за сторонние плагины и их последствия. Ставьте только то, чему доверяете: лучше — с открытым кодом и от известного автора.</p>
      <label class="sw"><input type="checkbox" id="pl-warn-ok"><span class="tr"></span>Больше не показывать</label>
      <div class="row cn-foot"><span class="spacer"></span><button class="btn ghost" type="button" id="pl-warn-no">Отмена</button><button class="btn primary" type="button" id="pl-warn-yes">Понимаю, установить</button></div>`;
    plShow();
    const finish = async (yes) => {
      if (yes && $("#pl-warn-ok").checked) { await api("POST", "/api/plugins/warned").catch(() => {}); PL.data.warned = true; S.settings.plugins_warned = true; }
      plClose(); done(yes);
    };
    $("#pl-warn-yes").onclick = () => finish(true);
    $("#pl-warn-no").onclick = () => finish(false);
    $("#pl-modal .modal-bg").onclick = () => finish(false);
  });
}

export function plShow() {
  const m = $("#pl-modal");
  m.classList.remove("hidden", "out"); void m.offsetWidth; m.classList.add("in");
}

export function plClose() {
  const m = $("#pl-modal");
  if (!m) return;
  PL.open = "";
  m.classList.remove("in"); m.classList.add("out");
  setTimeout(() => { if (m.classList.contains("out")) m.classList.add("hidden"); }, 260);
}

export function plSettings(id) {
  const p = plInstalled(id);
  if (!p) return;
  PL.open = id;
  const field = (s) => {
    const v = p.values?.[s.key];
    const lab = `${esc(s.label || s.key)}${s.hint ? " " + tip(s.hint) : ""}`;
    const at = `data-k="${esc(s.key)}" data-type="${esc(s.type)}"`;
    if (s.type === "bool") return `<label class="sw"><input type="checkbox" ${at}${v ? " checked" : ""}><span class="tr"></span>${lab}</label>`;
    if (s.type === "select") return `<div class="f"><label>${lab}</label><select ${at}>${(s.options || []).map((o) => `<option${o === v ? " selected" : ""}>${esc(o)}</option>`).join("")}</select></div>`;
    if (s.type === "textarea") return `<div class="f"><label>${lab}</label><textarea rows="4" ${at}>${esc(v ?? "")}</textarea></div>`;
    if (s.type === "password") return `<div class="f"><label>${lab}</label><input type="password" autocomplete="off" ${at} placeholder="${v ? `задан ${esc(v)} — оставьте пустым, чтобы не менять` : "не задан"}"></div>`;
    return `<div class="f"><label>${lab}</label><input type="${s.type === "number" ? "number" : "text"}" ${at} value="${esc(v ?? "")}"></div>`;
  };
  $("#pl-card").innerHTML = `<div class="cn-mh"><span class="char-av cn-av">${plIcon(p.icon)}</span><div class="char-n"><h2 id="pl-mt">${esc(p.name)}</h2>${plBadge(p.official)}</div>
      <button class="ib" type="button" data-close aria-label="Закрыть">${icon("x")}</button></div>
    <p class="cn-about">${esc(p.description || "")}</p>
    <div class="cn-body plg-form">${(p.settings || []).map(field).join("")}</div>
    <div class="row cn-foot"><span class="spacer"></span><button class="btn primary" type="button" data-pl-save="${esc(id)}">Сохранить</button></div>`;
  plShow();
}

export async function plInstall(btn, repo, id, official) {
  if (!official && !(await plWarn(repo))) return;
  await act(btn, async () => {
    const r = await api("POST", "/api/plugins/install", { repo, id });
    await plLoad();
    toast(`«${r.plugin?.name || id}» установлен`);
  });
}

export async function plClick(e) {
  if (page !== "plugins") return;
  const t = e.target.closest("button, [data-close]");
  if (!t) return;
  if (t.matches("[data-close]")) { plClose(); return; }
  if (t.closest("#pl-side")) { PL.side = t.dataset.side; PL.cat = "all"; plTabs(); plDraw(); return; }
  if (t.dataset.plCat) { PL.cat = t.dataset.plCat; plDraw(); return; }
  if (t.id === "pl-refresh") { plCatalog(true); return; }
  if (t.dataset.plInstall) { plInstall(t, t.dataset.plInstall, t.dataset.id, !!t.dataset.official); return; }
  if (t.dataset.plUpdate) {
    const p = plInstalled(t.dataset.plUpdate), c = p && plCatItem(p);
    if (c) await act(t, async () => { await api("POST", "/api/plugins/install", { repo: c.repo, id: c.id }); await plLoad(); }, "Плагин обновлён");
    return;
  }
  if (t.dataset.plSet) { plSettings(t.dataset.plSet); return; }
  if (t.dataset.plSave) {
    const vals = {};
    $$("#pl-card [data-k]").forEach((el) => {
      const ty = el.dataset.type;
      vals[el.dataset.k] = ty === "bool" ? el.checked : ty === "number" ? (el.value === "" ? null : Number(el.value)) : el.value;
    });
    await act(t, async () => { await api("POST", `/api/plugins/${encodeURIComponent(t.dataset.plSave)}/settings`, vals); plClose(); await plLoad(); }, "Сохранено");
    return;
  }
  if (t.dataset.plDel) {
    const p = plInstalled(t.dataset.plDel);
    if (!confirm(`Удалить плагин «${p?.name || t.dataset.plDel}» вместе с его настройками и данными?`)) return;
    await act(t, async () => { await api("DELETE", `/api/plugins/${encodeURIComponent(t.dataset.plDel)}`); await plLoad(); }, "Плагин удалён");
  }
}

export async function plChange(e) {
  if (page !== "plugins") return;
  const on = e.target.closest("[data-pl-on]");
  if (e.target.id === "pl-mine") { PL.mine = e.target.checked; plDraw(); return; }
  if (on) {
    await act(null, async () => { await api("POST", `/api/plugins/${encodeURIComponent(on.dataset.plOn)}/enable`, { on: on.checked }); await plLoad(); });
    return;
  }
  if (e.target.id === "pl-file" && e.target.files?.[0]) {
    const f = e.target.files[0];
    e.target.value = "";
    if (!(await plWarn(f.name))) return;
    await act(null, async () => { const r = await upload("POST", "/api/plugins/upload", f); PL.side = r.plugin?.official ? "official" : "community"; PL.cat = "all"; plTabs(); await plLoad(); toast(`«${r.plugin?.name}» установлен`); });
  }
}
