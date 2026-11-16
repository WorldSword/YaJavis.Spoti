// Безопасность и обновления.
import { $, $$, act, api, esc, icon, sec, tip, toast, upload, waitRestart } from "./core.js";
import { S, reload } from "./main.js";
import { wizOpen } from "./wizard.js";

// ---------------------------------------------------------------- безопасность
export function secHead(tab) {
  return `<header class="ph"><h1>${tab === "training" ? "Обучение" : "Безопасность"}</h1><p class="sub">${tab === "training" ? "Помогите Джарвису лучше узнавать своё имя — по вашему голосу и вашей комнате" : "Кто может попасть в колонку и её настройки"}</p></header>`;
}

export function viewSecurity() {
  $("#view").innerHTML = `${secHead("security")}
  ${sec("Пароль настроек", "После смены все остальные браузеры выйдут из настроек",
    `<div class="grid g2"><div class="f"><label>Текущий</label><input type="password" id="pw-old" autocomplete="current-password"></div>
      <div class="f"><label>Новый ${tip("Минимум 6 символов.")}</label><input type="password" id="pw-new" autocomplete="new-password"></div></div>
    <div><button class="btn" id="pw-save">Сменить пароль</button></div>`)}
  ${sec("Доступ к настройкам", "После 5 неверных паролей вход временно блокируется. Ключи и ссылка VLESS не уходят обратно в браузер",
    `<label class="sw"><input type="checkbox" id="lan"${S.settings.lan_only ? " checked" : ""}><span class="tr"></span>Только из домашней сети ${tip("Настройки открываются только с устройств в вашей Wi-Fi сети. Выключайте, только если точно понимаете зачем (например, доступ через свой VPN).")}</label>`)}
  ${sec("Что открыто на колонке", "Служебные входы, через которые в колонку можно попасть из домашней сети",
    `<div class="checks">${S.security.map((c) => `<div class="check"><div class="body"><b>${esc(c.title)}</b><small>${esc(c.hint)}</small></div><span class="st ${c.open ? "warn" : "ok"}">${c.open ? "открыт" : "закрыт"}</span></div>`).join("") || `<p class="note">Проверять нечего</p>`}</div>`)}
  ${sec("Обновления", "", `<div id="upd"><p class="note">…</p></div>`)}
  ${sec("Знакомство", "Что такое Джарвис, что он умеет, кто думает и какие плагины поставить", `<div><button class="btn" id="wiz-again">${icon("right")}Показать знакомство</button></div>`)}
  ${sec("Для разработчика", "", `<label class="sw"><input type="checkbox" id="dev"${S.settings.dev_mode ? " checked" : ""}><span class="tr"></span><span>Режим разработчика <small class="muted">— вкладка «Обучение»: дообучение слова «Джарвис» и запись фона</small></span></label>`)}`;
  drawUpdate();
  $("#wiz-again").addEventListener("click", () => wizOpen(0));
  $("#dev").addEventListener("change", (e) => act(null, async () => { await api("PUT", "/api/settings", { dev_mode: e.target.checked }); await reload(); viewSecurity(); }, e.target.checked ? "Режим разработчика включён — вкладка «Обучение» рядом" : "Режим разработчика выключен"));
  $("#view").oninput = $("#view").onchange = $("#view").onclick = null;
  $("#pw-save").addEventListener("click", (e) => act(e.target, async () => { await api("PUT", "/api/password", { old: $("#pw-old").value, new: $("#pw-new").value }); $("#pw-old").value = $("#pw-new").value = ""; }, "Пароль сменён"));
  $("#lan").addEventListener("change", async (e) => {
    if (!e.target.checked && !confirm("Разрешить вход в настройки не только из домашней сети?")) { e.target.checked = true; return; }
    await act(null, async () => { await api("PUT", "/api/settings", { lan_only: e.target.checked }); await reload(); });
  });
}

// ---------------------------------------------------------------- обновления (на странице «Безопасность»)
export async function drawUpdate() {
  const box = $("#upd");
  if (!box) return;
  let U;
  try { U = await api("GET", "/api/update"); } catch (e) { box.innerHTML = `<p class="err">${esc(e.message)}</p>`; return; }
  const auto = U.check || U.auto;
  box.innerHTML = `<div class="row upd-row">
      <span>Версия <b>${esc(U.version)}</b></span>
      <a class="btn sm ghost" href="https://github.com/newfpv/YaJavis/releases" target="_blank" rel="noopener noreferrer">GitHub Releases</a>
      ${U.busy ? `<span class="note"><span class="spin"></span>${esc(U.busy)}…</span>`
        : U.available ? `<span class="st ok">вышла ${esc(U.latest)}</span><button class="btn sm primary" id="u-install">Обновить</button>`
        : U.latest ? `<span class="st ok">последняя</span>` : ""}
      <span class="spacer"></span>
      ${U.repo ? `<button class="btn sm" id="u-checknow">Проверить</button>` : ""}
      <label class="btn sm ghost" for="u-file" data-tip="Поставить файл ${esc(U.asset)} с компьютера">Файлом…</label><input type="file" id="u-file" class="hidden">
    </div>
    <div class="seg" id="u-mode"><button type="button" data-auto="0" class="${auto ? "" : "on"}">Вручную</button><button type="button" data-auto="1" class="${auto ? "on" : ""}">Автообновление</button></div>
    <small class="muted" id="u-hint"></small>
    <label class="sw hu u-stats"><input type="checkbox" id="u-stats"${U.stats ? " checked" : ""}><span class="tr"></span>
      <span class="hu-t"><b>Счётчик установок</b><small>Раз на версию колонка скачивает пустой файл с GitHub — так в README видно, сколько колонок с Джарвисом. Передаются только модель и версия.</small></span></label>
    ${U.error ? `<p class="err">${esc(U.error)}</p>` : ""}
    ${U.available && U.notes ? `<details class="mini"><summary>Что нового в ${esc(U.latest)}</summary><pre class="unotes">${esc(U.notes)}</pre></details>` : ""}`;
  const hint = (on) => { $("#u-hint").textContent = !U.repo ? "Укажите репозиторий обновлений или выберите файл с компьютера." : on ? `Новая версия ставится сама ночью, с 3 до 6, когда колонка молчит${U.checked ? ` · проверено ${U.checked}` : ""}` : "Только когда нажмёте «Проверить» или выберете файл"; };
  hint(auto);
  $$("#u-mode [data-auto]").forEach((b) => b.addEventListener("click", () => {
    const on = b.dataset.auto === "1";
    $$("#u-mode button").forEach((x) => x.classList.toggle("on", x === b));
    hint(on);
    api("PUT", "/api/update", { check: on, auto: on }).then(() => toast(on ? "Автообновление включено" : "Обновления — вручную")).catch((err) => toast(err.message, true));
  }));
  $("#u-stats").addEventListener("change", (e) => api("PUT", "/api/update", { stats: e.target.checked }).then(() => toast(e.target.checked ? "Счётчик включён" : "Счётчик выключен")).catch((err) => toast(err.message, true)));
  $("#u-checknow")?.addEventListener("click", (e) => act(e.target, async () => { await api("POST", "/api/update/check"); drawUpdate(); }));
  $("#u-install")?.addEventListener("click", (e) => act(e.target, async () => { e.target.textContent = "Скачиваю…"; await api("POST", "/api/update/install"); await waitRestart("Обновление скачано — Джарвис перезапускается"); }));
  $("#u-file").addEventListener("change", (e) => { const file = e.target.files[0]; if (!file) return; act(null, async () => { toast("Загружаю на колонку…"); await upload("PUT", "/api/update/upload", file); await waitRestart("Файл принят — Джарвис перезапускается"); }); });
}
