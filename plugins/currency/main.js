// Курсы валют: ЦБ РФ (cbr-xml-daily.ru) или Нацбанк Беларуси (api.nbrb.by). Кеш на час.

const NAMES = {
  USD: ["доллар", "бакс"], EUR: ["евро"], CNY: ["юан"], GBP: ["фунт"], JPY: ["иен", "йен"], CHF: ["франк"],
  TRY: ["лир"], KZT: ["тенге"], BYN: ["белорусск"], RUB: ["российск", "рубл"], UAH: ["грив"], PLN: ["злот"],
};

function code(word) {
  const w = (word || "").toLowerCase();
  if (/^[a-z]{3}$/.test(w)) return w.toUpperCase();
  for (const [c, stems] of Object.entries(NAMES)) if (stems.some((s) => w.includes(s))) return c;
  return null;
}

function rates() {
  const bank = jarvis.settings.bank || "ЦБ РФ";
  const key = "rates:" + bank, cached = jarvis.store.get(key);
  if (cached && Date.now() - cached.at < 3600e3) return cached;
  let out;
  if (bank === "НБ РБ") {
    const list = jarvis.fetch("https://api.nbrb.by/exrates/rates?periodicity=0").json();
    out = { base: "BYN", unit: "белорусских рублей", r: {} };
    for (const x of list) out.r[x.Cur_Abbreviation] = x.Cur_OfficialRate / x.Cur_Scale;
  } else {
    const d = jarvis.fetch("https://www.cbr-xml-daily.ru/daily_json.js").json();
    out = { base: "RUB", unit: "рублей", r: {} };
    for (const [c, v] of Object.entries(d.Valute)) out.r[c] = v.Value / v.Nominal;
  }
  out.at = Date.now();
  jarvis.store.set(key, out);
  return out;
}

function fmt(n) {
  const p = n < 10 ? 10000 : 100;
  return String(Math.round(n * p) / p).replace(".", ",");
}

function convert(cur, amount) {
  const c = code(cur);
  if (!c) return "Не знаю такую валюту: " + cur;
  const r = rates();
  if (c === r.base) return "Это и есть местная валюта.";
  const v = r.r[c];
  if (!v) return "Банк не даёт курс " + c + ".";
  const n = amount > 0 ? amount : 1;
  return n + " " + c + " = " + fmt(n * v) + " " + r.unit + " (" + (jarvis.settings.bank || "ЦБ РФ") + ").";
}

jarvis.tool({
  name: "exchange_rate",
  description: "Официальный курс валюты к рублю (ЦБ РФ или НБ РБ, как выбрано) и пересчёт суммы.",
  parameters: {
    type: "object",
    properties: {
      currency: { type: "string", description: "код валюты (USD, EUR, CNY…) или название" },
      amount: { type: "number", description: "сколько единиц пересчитать, по умолчанию 1" },
    },
    required: ["currency"],
  },
}, (a) => convert(a.currency, a.amount));

jarvis.command([
  "(какой |сейчас )?курс ([а-я]+)( сегодня| сейчас)?",
  "сколько стоит ([а-я]+)",
  "сколько (\\d+) ([а-я]+)( в рублях)?",
], (text, m) => {
  const num = m.find((x) => /^\d+$/.test(x || ""));
  const word = m.filter((x) => x && /^[а-я]+$/.test(x)).find((x) => code(x));
  return word ? convert(word, num ? Number(num) : 1) : null;
});
