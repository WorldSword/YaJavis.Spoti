// Характеры.
import { $, act, api, esc, icon, setDirty } from "./core.js";
import { S, reload } from "./main.js";

// ---------------------------------------------------------------- характеры: карточки + редактор поверх страницы
export const CHAR_PRESETS = [
  ["Дворецкий", "Ты — Джарвис, голосовой помощник в умной колонке. Говоришь по-русски, коротко и по делу, с лёгкой иронией, как дворецкий из фильма. Обращаешься к хозяину на «ты»."],
  ["Кореш", "Ты — Джарвис, но общаешься как близкий друг: на «ты», с юмором и подколами, живо и по-простому. Говоришь коротко и честно: не знаешь — так и скажи."],
  ["Учёный", "Ты — Джарвис, спокойный и точный, как учёный. Объясняешь просто, но без ошибок, приводишь одну яркую деталь или цифру. Отвечаешь в 2–3 предложения, если не просят подробнее."],
  ["Лаконичный", "Ты — Джарвис. Отвечай максимально коротко: одно предложение, без вступлений и вежливых оборотов. Только суть."],
  ["Детский", "Ты — Джарвис, добрый помощник для ребёнка. Говоришь просто, тепло и весело, без сложных слов и без страшного. Хвалишь за интересные вопросы."],
];

export function viewCharacters() {
  $("#view").innerHTML = `<header class="ph ph-row"><div><h1>Характеры</h1><p class="sub">Кто такой Джарвис и как он с вами общается. Характер выбирается в настройках ассистента</p></div>
    <div class="actions"><button class="btn primary" id="ch-add">${icon("plus")}Новый характер</button></div></header>
  <div class="chars stagger" id="chars"></div>
  <div class="modal hidden" id="ch-modal" role="dialog" aria-modal="true" aria-labelledby="ch-mt"><div class="modal-bg" data-close></div>
    <div class="modal-card">
      <div class="card-head"><h2 id="ch-mt">Новый характер</h2><button class="ib" type="button" data-close aria-label="Закрыть">${icon("x")}</button></div>
      <div class="f"><label>Название</label><input type="text" id="c-name" maxlength="40" placeholder="Например, Дворецкий" autocomplete="off"></div>
      <div class="f"><label>Начать с образца</label><div class="presets-row" id="ch-pre">${CHAR_PRESETS.map(([n], i) => `<button type="button" class="chip" data-p="${i}">${esc(n)}</button>`).join("")}</div></div>
      <div class="f"><label>Как он говорит <span class="meta" id="c-count"></span></label><textarea class="big" id="c-text" placeholder="Ты — Джарвис… Опишите манеру: тон, обращение, длину ответов, любимые словечки"></textarea></div>
      <p class="note">Пишите от второго лица: «Ты — …». Не вставляйте ключи и пароли — текст уходит модели с каждым разговором.</p>
      <div class="row"><button class="btn primary" id="c-save">Сохранить</button><button class="btn ghost" type="button" data-close>Отмена</button><span class="spacer"></span><button class="btn ghost danger hidden" id="c-del">Удалить</button></div>
    </div></div>`;
  let editing = null;
  const modal = $("#ch-modal");
  const count = () => { $("#c-count").textContent = `${$("#c-text").value.length} симв.`; };
  const open = (c) => {
    editing = c || null;
    $("#ch-mt").textContent = c ? `Изменить «${c.name}»` : "Новый характер";
    $("#c-name").value = c?.name || ""; $("#c-text").value = c?.text || "";
    const used = c && S.profiles.some((p) => p.prompt === c.id);
    $("#c-del").classList.toggle("hidden", !c || used);
    count();
    modal.classList.remove("hidden", "out"); void modal.offsetWidth; modal.classList.add("in");
    setTimeout(() => $("#c-name").focus(), 60);
  };
  const close = () => { modal.classList.remove("in"); modal.classList.add("out"); setDirty(false); setTimeout(() => { if (modal.classList.contains("out")) modal.classList.add("hidden"); }, 260); };
  const draw = () => {
    $("#chars").innerHTML = S.prompts.map((c) => {
      const used = S.profiles.filter((p) => p.prompt === c.id);
      return `<article class="card char" data-id="${esc(c.id)}" tabindex="0">
        <div class="char-h"><span class="char-av">${esc((c.name || "?").trim()[0] || "?")}</span><div class="char-n"><h2>${esc(c.name)}</h2>
          <span class="meta">${used.length ? `у ${used.map((p) => esc(p.name)).join(", ")}` : "пока ни у кого"}</span></div></div>
        <p class="char-t">${esc(c.text)}</p>
        <div class="char-f"><span class="meta">${c.text.length} симв.</span><span class="spacer"></span><span class="char-edit">${icon("tune")}Изменить</span></div>
      </article>`;
    }).join("") + `<button class="char char-new" type="button" id="ch-new">${icon("plus")}<b>Новый характер</b><small>Опишите манеру — и Джарвис заговорит по-другому</small></button>`;
  };
  draw();
  $("#chars").addEventListener("click", (e) => {
    if (e.target.closest("#ch-new")) return open();
    const card = e.target.closest(".char[data-id]");
    if (card) open(S.prompts.find((c) => c.id === card.dataset.id));
  });
  $("#chars").addEventListener("keydown", (e) => { const card = e.target.closest(".char[data-id]"); if (card && (e.key === "Enter" || e.key === " ")) { e.preventDefault(); open(S.prompts.find((c) => c.id === card.dataset.id)); } });
  $("#ch-add").addEventListener("click", () => open());
  modal.addEventListener("click", (e) => { if (e.target.closest("[data-close]")) close(); });
  modal.addEventListener("keydown", (e) => { if (e.key === "Escape") close(); });
  $("#ch-pre").addEventListener("click", (e) => {
    const b = e.target.closest("[data-p]"); if (!b) return;
    const [n, t] = CHAR_PRESETS[+b.dataset.p];
    if ($("#c-text").value.trim() && !confirm("Заменить текст образцом?")) return;
    $("#c-text").value = t; if (!$("#c-name").value.trim()) $("#c-name").value = n;
    setDirty(true); count();
  });
  $("#c-text").addEventListener("input", () => { setDirty(true); count(); });
  $("#c-name").addEventListener("input", () => { setDirty(true); });
  $("#c-save").addEventListener("click", (e) => act(e.target, async () => {
    await api("PUT", "/api/prompt", { id: editing?.id || "", name: $("#c-name").value.trim(), text: $("#c-text").value.trim() });
    setDirty(false); await reload(); draw(); close();
  }, "Характер сохранён"));
  $("#c-del").addEventListener("click", (e) => { if (editing && confirm(`Удалить характер «${editing.name}»?`)) act(e.target, async () => { await api("DELETE", "/api/prompt?id=" + encodeURIComponent(editing.id)); await reload(); draw(); close(); }, "Удалено"); });
}
