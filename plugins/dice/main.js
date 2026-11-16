// Самый простой плагин: быстрые команды без нейросети и инструмент, который может вызвать модель.

const rnd = (a, b) => a + Math.floor(Math.random() * (b - a + 1));

jarvis.command(["(брось|подбрось|кинь) монет(ку|у)", "орел или решка"],
  () => (Math.random() < 0.5 ? "Орёл." : "Решка."));

jarvis.command(["(брось|кинь) (кубик|кости)"], () => "Выпало " + rnd(1, 6) + ".");

jarvis.command(["(загадай|выбери|назови) (случайное )?число от (\\d+) до (\\d+)"], (text, m) => {
  const a = Number(m[2]), b = Number(m[3]);
  return b > a ? "Пусть будет " + rnd(a, b) + "." : null;
});

jarvis.tool({
  name: "random_number",
  description: "Честное случайное число в диапазоне (жребий, кубик, лотерея).",
  parameters: { type: "object", properties: { min: { type: "integer" }, max: { type: "integer" } }, required: ["min", "max"] },
}, (a) => String(rnd(Math.min(a.min, a.max), Math.max(a.min, a.max))));
