// Счётчик установок для README: читает скачивания файлов служебного релиза «stats»
// (их скачивают колонки, см. jarvis/core/src/counter.rs) и рисует stats.svg + badge.json + stats.json.
// Запуск: GH_TOKEN=… REPO=owner/name node scripts/stats.mjs <папка>
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const repo = process.env.REPO || "newfpv/YaJarvis";
const out = process.argv[2] || "stats-out";
const headers = { Accept: "application/vnd.github+json", "User-Agent": "yajarvis-stats" };
if (process.env.GH_TOKEN) headers.Authorization = "Bearer " + process.env.GH_TOKEN;

async function gh(path) {
  const r = await fetch("https://api.github.com" + path, { headers });
  if (!r.ok) throw new Error(path + ": " + r.status);
  return r.json();
}
async function all(path) {
  const res = [];
  for (let page = 1; ; page++) {
    const part = await gh(`${path}${path.includes("?") ? "&" : "?"}per_page=100&page=${page}`);
    res.push(...part);
    if (part.length < 100) return res;
  }
}

const NAMES = {
  lite: "Станция Лайт", lite2: "Станция Лайт 2", mini1: "Станция Мини", mini2: "Станция Мини 2", mini3: "Станция Мини 3",
  mini3pro: "Станция Мини 3 Про", midi: "Станция Миди", station2: "Станция 2", station3: "Станция 3", street: "Станция Стрит",
  fiero: "Fiero Hi", other: "Другие",
};

const releases = await all(`/repos/${repo}/releases`);
const stats = releases.find((r) => r.tag_name === "stats");
const assets = stats ? await all(`/repos/${repo}/releases/${stats.id}/assets`) : [];
const models = {}, versions = {};
let devices = 0;
for (const a of assets) {
  let m = a.name.match(/^new-([a-z0-9]+)$/);
  if (m) { models[m[1]] = (models[m[1]] || 0) + a.download_count; devices += a.download_count; continue; }
  m = a.name.match(/^v(\d+\.\d+\.\d+)-([a-z0-9]+)$/);
  if (m) versions[m[1]] = (versions[m[1]] || 0) + a.download_count;
}
// Боты-зеркала скачивают каждый файл каждого релиза — даже установщики для macOS, которыми никто не
// пользуется. Поэтому в каждом релизе вычитаем минимум по установщикам: это и есть проход бота.
let setups = 0;
for (const r of releases) {
  const counts = (r.assets || []).filter((a) => /^JarvisSetup-/.test(a.name)).map((a) => a.download_count);
  if (counts.length) setups += counts.reduce((s, n) => s + n, 0) - Math.min(...counts) * counts.length;
}

const cmpVer = (a, b) => { const x = a.split(".").map(Number), y = b.split(".").map(Number); for (let i = 0; i < 3; i++) if (x[i] !== y[i]) return y[i] - x[i]; return 0; };
const verList = Object.entries(versions).filter(([, n]) => n > 0).sort((a, b) => cmpVer(a[0], b[0]));
const latest = releases.filter((r) => !r.prerelease && !r.draft && /^v\d/.test(r.tag_name)).map((r) => r.tag_name.slice(1)).sort(cmpVer)[0] || "";
const modelList = Object.entries(models).filter(([, n]) => n > 0).sort((a, b) => b[1] - a[1]);
const updated = new Date().toISOString();

mkdirSync(out, { recursive: true });
writeFileSync(join(out, "stats.json"), JSON.stringify({ devices, models, versions, latest, latest_devices: versions[latest] || 0, setup_downloads: setups, updated }, null, 2));
writeFileSync(join(out, "badge.json"), JSON.stringify({ schemaVersion: 1, label: "колонок с Джарвисом", message: String(devices), color: "f77b2c", labelColor: "202027" }));

// ---- карточка для README: тёмная/светлая по теме GitHub
const esc = (s) => String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
const ru = (n) => String(n).replace(/\B(?=(\d{3})+(?!\d))/g, " ");
const plural = (n, one, few, many) => { const a = n % 10, b = n % 100; return a === 1 && b !== 11 ? one : a >= 2 && a <= 4 && (b < 12 || b > 14) ? few : many; };
const W = 820, pad = 32, colW = (W - pad * 3) / 2;
const rows = Math.max(modelList.length, Math.min(verList.length, 8), 1);
const H = 150 + rows * 34 + 34;
const maxM = Math.max(1, ...modelList.map(([, n]) => n));
const maxV = Math.max(1, ...verList.map(([, n]) => n));
const bar = (x, y, label, n, max, hot) => {
  const w = Math.max(4, Math.round((colW - 70) * n / max));
  return `<text x="${x}" y="${y}" class="l">${esc(label)}</text>
  <rect x="${x}" y="${y + 8}" width="${colW - 70}" height="6" rx="3" class="track"/>
  <rect x="${x}" y="${y + 8}" width="${w}" height="6" rx="3" class="${hot ? "acc" : "fill"}"/>
  <text x="${x + colW}" y="${y + 14}" class="n" text-anchor="end">${ru(n)}</text>`;
};
const left = modelList.length ? modelList.map(([m, n], i) => bar(pad, 150 + i * 34, NAMES[m] || m, n, maxM, i === 0)).join("")
  : `<text x="${pad}" y="160" class="l">Пока никто не отметился — первые колонки появятся после обновления.</text>`;
const right = verList.slice(0, 8).map(([v, n], i) => bar(pad * 2 + colW, 150 + i * 34, "v" + v + (v === latest ? " · последняя" : ""), n, maxV, v === latest)).join("");
const date = new Date(updated).toLocaleString("ru-RU", { timeZone: "Europe/Moscow", day: "numeric", month: "long", hour: "2-digit", minute: "2-digit" });
const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${W}" height="${H}" viewBox="0 0 ${W} ${H}" role="img" aria-label="Колонок с Джарвисом: ${devices}">
<style>
  .bg{fill:#ffffff;stroke:#d0d7de} .h{fill:#1f2328} .d{fill:#59636e} .l{fill:#1f2328;font-size:14px} .n{fill:#1f2328;font-size:14px;font-weight:700}
  .track{fill:#eaeef2} .fill{fill:#8c959f} .acc{fill:#f77b2c} .ring{fill:none;stroke:#f77b2c}
  text{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Manrope,Roboto,Helvetica,Arial,sans-serif}
  @media (prefers-color-scheme: dark){ .bg{fill:#16161c;stroke:#2c2c36} .h,.l,.n{fill:#efedf4} .d{fill:#9b97a8} .track{fill:#25252e} .fill{fill:#5d5a6b} }
</style>
<rect x="0.5" y="0.5" width="${W - 1}" height="${H - 1}" rx="20" class="bg"/>
<circle cx="${pad + 17}" cy="${pad + 27}" r="14" stroke-width="5" class="ring"/>
<text x="${pad + 46}" y="${pad + 40}" class="h" font-size="40" font-weight="800">${ru(devices)}</text>
<text x="${pad + 46 + String(ru(devices)).length * 24 + 12}" y="${pad + 23}" class="h" font-size="16" font-weight="700">${plural(devices, "колонка", "колонки", "колонок")} с Джарвисом</text>
<text x="${pad + 46 + String(ru(devices)).length * 24 + 12}" y="${pad + 43}" class="d" font-size="14">${latest ? `на последней v${esc(latest)} — ${ru(versions[latest] || 0)}` : ""}${setups ? ` · установщик скачали ${ru(setups)} раз` : ""}</text>
<text x="${pad}" y="122" class="d" font-size="13" font-weight="700" letter-spacing=".4">МОДЕЛИ</text>
<text x="${pad * 2 + colW}" y="122" class="d" font-size="13" font-weight="700" letter-spacing=".4">ВЕРСИИ</text>
${left}
${right}
<text x="${pad}" y="${H - 18}" class="d" font-size="12">Колонка отмечается сама (только модель и версия) · обновлено ${esc(date)} МСК</text>
</svg>`;
writeFileSync(join(out, "stats.svg"), svg);
console.log(`колонок ${devices}, моделей ${modelList.length}, версий ${verList.length}, установщик скачали ${setups}`);
