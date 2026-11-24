<script>
  import { onMount } from "svelte";
  import styles from "./InstallProgress.module.css";

  const pollDelay = 700;
  const stages = [
    ["Подключение", /Подключаюсь|Определена|Найден Джарвис/],
    ["Место", /Стираю|Удаляю|Свободно на колонке/],
    ["Файлы", /Копирую|%$/],
    ["Автозапуск", /Выключаю|Добавляю автозапуск/],
    ["Запуск панели", /Проверяю запуск панели|Проверяю завершение установки по ADB/],
    ["Готово", /Готово/],
  ];

  let { api, operation, onAgain } = $props();
  let job = $state.raw(null);
  let detailsOpen = $state(false);
  let rebooting = $state(false);
  let rebootSent = $state(false);
  let rebootError = $state("");
  let log = $state();

  let installing = $derived(operation === "install");
  let restoring = $derived(operation === "restore");
  let lines = $derived(job?.lines ?? []);
  let logText = $derived(lines.join("\n"));
  let pendingStatus = $derived(installing ? "Подготавливаем установку…" : restoring ? "Подготавливаем восстановление…" : "Подготавливаем удаление…");
  let status = $derived(job
    ? job.error || job.warning || (rebootSent ? "Команда перезагрузки отправлена. Подождите около минуты и откройте панель." : rebootError) || job.reboot_reason || lines.findLast((line) => !/^\s*\d+%$/.test(line)) || pendingStatus
    : "Подключаемся к колонке…");
  let progress = $derived(readProgress(lines, job?.done && !job.error));
  let copying = $derived(stages[progress.stage]?.[0] === "Файлы" && progress.percent != null);
  let progressValue = $derived(job && (job.done ? 100 : Math.round(
    (progress.stage + (copying ? progress.percent / 100 : 0.4)) / stages.length * 100,
  )));

  onMount(() => {
    const controller = new AbortController();
    const { signal } = controller;
    let timer = setTimeout(poll, pollDelay);

    async function poll() {
      try {
        const result = await api("GET", "/api/job", undefined, signal);
        if (signal.aborted) return;
        job = result;
        if (result.done && result.error) detailsOpen = true;
      } catch {
        // The installer can be temporarily unavailable while the job runs.
      }
      if (!signal.aborted && !job?.done) timer = setTimeout(poll, pollDelay);
    }

    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  });

  $effect(() => {
    if (detailsOpen && log && logText) log.scrollTop = log.scrollHeight;
  });

  function readProgress(lines, completed) {
    let stage = 0;
    let percent;
    for (const line of lines) {
      stage = Math.max(stage, stages.findLastIndex(([, pattern]) => pattern.test(line)));
      const match = line.match(/(\d+)%$/);
      if (match) percent = Number(match[1]);
    }
    return { stage: completed ? stages.length : stage, percent };
  }

  function resultTitle(result) {
    if (result.error) return "Нужна ваша помощь.";
    if (result.warning) return "Настройте автозапуск.";
    if (result.reboot_needed) return "Нужна перезагрузка.";
    if (restoring) return "Бэкап восстановлен.";
    return "Всё готово.";
  }

  function resultDescription(result) {
    if (result.error) {
      const stopped = installing ? "Установка остановлена." : restoring ? "Восстановление остановлено." : "Удаление остановлено.";
      return stopped + " Причина показана ниже; подробности есть в журнале.";
    }
    if (result.panel_check_skipped) {
      return result.reboot_needed
        ? "Колонка подтвердила запуск Джарвиса, но для работы микрофона её нужно перезагрузить."
        : "Колонка подтвердила запуск Джарвиса. Откройте панель по адресу, настроенному при пробросе портов.";
    }
    if (result.warning) return "Джарвис установлен и уже работает.";
    if (result.reboot_needed) return "Джарвис установлен, но для подключения микрофона перезагрузите колонку.";
    if (restoring && result.url) return "Настройки, память и данные плагинов перенесены. Недостающие плагины доустановятся в фоне.";
    if (result.url) return "Джарвис установлен. Откройте панель, подключите любимый AI-сервис и выберите голос.";
    return "Операция завершена. Можно вернуться к выбору колонки.";
  }

  async function rebootSpeaker() {
    if (rebooting || rebootSent || !job?.adb_address) return;
    rebootError = "";
    rebooting = true;
    try {
      await api("POST", "/api/reboot", { ip: job.adb_address });
      rebootSent = true;
    } catch (failure) {
      rebootError = failure.message;
    } finally {
      rebooting = false;
    }
  }
</script>

<div class={["step-body", styles.body]}>
  <div
    class={[styles.orb, job?.done && (job.error ? styles.failed : styles.complete)]}
    aria-hidden="true"
  ><span></span></div>
  <h1>{job?.done ? resultTitle(job) : installing ? "Устанавливаю…" : restoring ? "Восстанавливаю бэкап…" : "Удаляю…"}</h1>
  <p class={["lead", styles.description]}>
    {#if job?.done}
      {resultDescription(job)}
    {:else if installing}
      Переносим файлы и готовим всё для первого разговора. Пока можно познакомиться с возможностями
      <span class={styles.desktop}>справа.</span><span class={styles.mobile}>ниже.</span>
    {:else if restoring}
      Переносим настройки, память и данные плагинов на колонку.
    {:else}
      Удаляем Джарвиса с колонки.
    {/if}
  </p>
  {#if installing}
    <ol class={styles.stages} aria-label="Этапы установки">
      {#each stages as [label], index (label)}
        {@const manual = Boolean(job?.warning) && label === "Автозапуск"}
        {@const failed = manual || (index === progress.stage && job?.error)}
        <li
          class={[
            failed && styles.failed,
            !failed && index < progress.stage && styles.complete,
            !failed && index === progress.stage && styles.active,
          ]}
        >
          <i aria-hidden="true"></i>
          {label}
          {#if manual}
            <small>Вручную</small>
          {:else if job?.panel_check_skipped && label === "Запуск панели"}
            <small>Без проверки по HTTP</small>
          {:else if copying && index === progress.stage && !job?.done}
            <small>{progress.percent}%</small>
          {/if}
        </li>
      {/each}
    </ol>
  {/if}
  <progress class={styles.progress} aria-label="Ход установки" max="100" value={progressValue ?? undefined}></progress>
  <p class={styles.status} role="status">{status}</p>
  <details class={styles.details} bind:open={detailsOpen}>
    <summary>Технический журнал</summary>
    <pre class={styles.log} bind:this={log}>{logText}</pre>
  </details>
</div>

{#if job?.done}
  <div class="step-footer">
    <div class={styles.actions}>
      {#if job.reboot_needed && job.adb_address}
        <button class="btn accent wide" type="button" onclick={rebootSpeaker} disabled={rebooting || rebootSent}>
          {rebooting ? "Отправляю команду…" : rebootSent ? "Перезагрузка запущена" : "Перезагрузить колонку"}
          <span aria-hidden="true">↻</span>
        </button>
      {/if}
      {#if job.url}
        <a class={["btn wide", job.reboot_needed ? "" : "accent"]} href={job.url} target="_blank" rel="noopener">
          Открыть панель Джарвиса <span aria-hidden="true">↗</span>
        </a>
      {/if}
      <button class="btn ghost wide" type="button" onclick={onAgain}>Другая колонка</button>
    </div>
  </div>
{/if}
