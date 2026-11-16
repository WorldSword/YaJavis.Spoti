// Погода: Open-Meteo (open-meteo.com, бесплатно, без ключа). Инструмент для модели и быстрые команды.

const CODES = {
  0: "ясно", 1: "в основном ясно", 2: "переменная облачность", 3: "пасмурно", 45: "туман", 48: "изморозь и туман",
  51: "слабая морось", 53: "морось", 55: "сильная морось", 56: "ледяная морось", 57: "сильная ледяная морось",
  61: "небольшой дождь", 63: "дождь", 65: "сильный дождь", 66: "ледяной дождь", 67: "сильный ледяной дождь",
  71: "небольшой снег", 73: "снег", 75: "сильный снег", 77: "снежная крупа", 80: "ливень", 81: "ливни", 82: "сильные ливни",
  85: "снегопад", 86: "сильный снегопад", 95: "гроза", 96: "гроза с градом", 99: "сильная гроза с градом",
};
const DAYS = { today: 0, tomorrow: 1, after_tomorrow: 2 };

const signed = (t) => { const n = Math.round(t); return (n > 0 ? "+" : "") + n; };

// «+4 градуса» — именительный («сейчас +4 градуса»)
function deg(t) {
  const n = Math.round(t), a = Math.abs(n) % 100, b = a % 10;
  const w = a > 10 && a < 20 ? "градусов" : b === 1 ? "градус" : b >= 2 && b <= 4 ? "градуса" : "градусов";
  return signed(t) + " " + w;
}

// «до +4 градусов» — родительный («от», «до»)
function degOf(t) {
  const a = Math.abs(Math.round(t)) % 100;
  return signed(t) + (a % 10 === 1 && a !== 11 ? " градуса" : " градусов");
}

function place(city) {
  const name = (city || jarvis.settings.city || jarvis.info().city || "").trim();
  if (!name) throw new Error("не знаю город: укажите его в настройках ассистента или плагина");
  const cached = jarvis.store.get("geo:" + name.toLowerCase());
  if (cached) return cached;
  const r = jarvis.fetch("https://geocoding-api.open-meteo.com/v1/search?count=1&language=ru&name=" + encodeURIComponent(name));
  const hit = (r.json().results || [])[0];
  if (!hit) throw new Error("не нашёл город «" + name + "»");
  const p = { name: hit.name, lat: hit.latitude, lon: hit.longitude };
  jarvis.store.set("geo:" + name.toLowerCase(), p);
  return p;
}

function forecast(city, day) {
  const p = place(city);
  const url = "https://api.open-meteo.com/v1/forecast?latitude=" + p.lat + "&longitude=" + p.lon +
    "&current=temperature_2m,apparent_temperature,weather_code,wind_speed_10m" +
    "&daily=weather_code,temperature_2m_max,temperature_2m_min,precipitation_probability_max" +
    "&timezone=auto&forecast_days=3&wind_speed_unit=ms";
  const d = jarvis.fetch(url).json();
  const i = DAYS[day] ?? 0;
  const daily = d.daily;
  const rain = daily.precipitation_probability_max[i];
  let text;
  if (i === 0 && d.current) {
    const c = d.current;
    text = p.name + ": сейчас " + deg(c.temperature_2m) + ", " + (CODES[c.weather_code] || "без осадков") +
      ", ощущается как " + deg(c.apparent_temperature) + ", ветер " + Math.round(c.wind_speed_10m) + " м/с. " +
      "Днём до " + degOf(daily.temperature_2m_max[0]) + ", ночью до " + degOf(daily.temperature_2m_min[0]) + ".";
  } else {
    text = p.name + (i === 1 ? " завтра: " : " послезавтра: ") + CODES[daily.weather_code[i]] + ", от " +
      signed(daily.temperature_2m_min[i]) + " до " + degOf(daily.temperature_2m_max[i]) + ".";
  }
  if (rain != null) {
    text += rain >= 50 ? " Вероятность осадков " + rain + "%, зонт пригодится." : rain >= 20 ? " Осадки возможны: " + rain + "%." : "";
  }
  return text;
}

jarvis.tool({
  name: "get_weather",
  description: "Погода сейчас и прогноз на сегодня, завтра или послезавтра. Город не назвали — не передавай его.",
  parameters: {
    type: "object",
    properties: {
      city: { type: "string", description: "город, если назвали" },
      day: { type: "string", enum: ["today", "tomorrow", "after_tomorrow"], description: "на какой день" },
    },
  },
}, (args) => forecast(args.city, args.day));

const when = (t) => (/послезавтра/.test(t) ? "after_tomorrow" : /завтра/.test(t) ? "tomorrow" : "today");

jarvis.command([
  "(какая |что за |что с |что там с )?погод[аоуы]( сейчас| сегодня| завтра| послезавтра)?( на улице)?",
  "(сколько |какая )?(градусов|температура)( на улице| сейчас| завтра)?",
  "(нужен ли|брать ли) зонт( сегодня| завтра)?",
], (text) => (jarvis.settings.fast === false ? null : forecast("", when(text))));

const showCity = (s) => jarvis.status("город: " + (s.city || jarvis.info().city || "не задан"));
showCity(jarvis.settings);
jarvis.onSettings(showCity);
