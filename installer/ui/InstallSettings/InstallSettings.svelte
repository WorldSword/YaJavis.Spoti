<script>
  import styles from "./InstallSettings.module.css";

  const timeZones = [
    ["UTC", "UTC"],
    ["Europe/Moscow", "Москва (UTC+3)"],
    ["Europe/Minsk", "Минск (UTC+3)"],
    ["Europe/Kaliningrad", "Калининград (UTC+2)"],
    ["Europe/Samara", "Самара (UTC+4)"],
    ["Asia/Yekaterinburg", "Екатеринбург (UTC+5)"],
    ["Asia/Almaty", "Алматы (UTC+5)"],
    ["Asia/Omsk", "Омск (UTC+6)"],
    ["Asia/Novosibirsk", "Новосибирск (UTC+7)"],
    ["Asia/Krasnoyarsk", "Красноярск (UTC+7)"],
    ["Asia/Irkutsk", "Иркутск (UTC+8)"],
    ["Asia/Yakutsk", "Якутск (UTC+9)"],
    ["Asia/Vladivostok", "Владивосток (UTC+10)"],
    ["Europe/Kyiv", "Киев"],
    ["Europe/Berlin", "Берлин"],
  ];

  let { api, upload, selectedSpeaker, onBack, onStarted } = $props();
  let adbAddress = $derived($selectedSpeaker?.adb_address ?? $selectedSpeaker?.ip);
  let updating = $derived(Boolean($selectedSpeaker?.installed));
  let password = $state("");
  let confirmation = $state("");
  let timezone = $state("UTC");
  let error = $state("");
  let submitting = $state(false);
  let backup = $state(null);
  let checkingBackup = $state(false);

  $effect(() => {
    if (!adbAddress) return;

    password = "";
    confirmation = "";
    timezone = updating ? "" : "UTC";
    error = "";
    backup = null;
  });

  async function pickBackup(event) {
    const input = event.currentTarget;
    const file = input.files?.[0];
    input.value = "";
    if (!file || checkingBackup || submitting) return;
    error = "";
    checkingBackup = true;
    try {
      backup = await upload("/api/backup/inspect", file);
      if (!password && !updating) {
        timezone = "";
      }
    } catch (failure) {
      error = failure.message;
    } finally {
      checkingBackup = false;
    }
  }

  function clearBackup() {
    backup = null;
    if (!updating && !timezone) timezone = "UTC";
  }

  async function install(event) {
    event.preventDefault();
    if (submitting || checkingBackup || !adbAddress) return;

    error = "";
    if (!updating && !backup && password.length < 6) {
      error = "Придумайте пароль — минимум 6 символов";
      return;
    }
    if (password && password.length < 6) {
      error = "Пароль должен быть от 6 символов";
      return;
    }
    if (password && password !== confirmation) {
      error = "Пароли не совпадают";
      return;
    }

    submitting = true;
    try {
      await api("POST", "/api/install", {
        ip: adbAddress,
        password,
        timezone,
        backup_id: backup?.id ?? "",
      });
      password = "";
      confirmation = "";
      onStarted();
    } catch (failure) {
      error = failure.message;
    } finally {
      submitting = false;
    }
  }
</script>

<form id="install-settings" class={["step-body", styles.body]} onsubmit={install}>
  <span class="eyebrow">Последний штрих</span>
  <h1>Почти готово.</h1>
  <p class="lead">Придумайте пароль. Сервисы и голос можно добавить позже.</p>

  <div class={styles.field}>
    <label for="pw">Пароль панели · от 6 символов</label>
    <div class={styles.passwordRow}>
      <input
        id="pw"
        type="password"
        autocomplete="new-password"
        placeholder={backup ? "Из бэкапа (или новый)" : updating ? "Не менять" : "Пароль"}
        bind:value={password}
        disabled={submitting}
      >
      <input
        id="pw2"
        type="password"
        autocomplete="new-password"
        placeholder="Повторите"
        aria-label="Повторите пароль"
        bind:value={confirmation}
        disabled={submitting}
      >
    </div>
  </div>

  <div class={styles.field}>
    <label for="tz">Часовой пояс</label>
    <select id="tz" bind:value={timezone} disabled={submitting}>
      {#if updating || backup}
        <option id="tz-keep" value="">{backup ? "Оставить из бэкапа" : "Оставить как есть"}</option>
      {/if}
      {#each timeZones as [value, label] (value)}
        <option {value}>{label}</option>
      {/each}
    </select>
  </div>

  <div class={styles.field}>
    <label for="backup-file">Восстановить из бэкапа (необязательно)</label>
    {#if backup}
      <div class={styles.backupBox}>
        <span>Бэкап v{backup.app_version || "?"} · {backup.files} файлов</span>
        <button class="btn ghost" type="button" onclick={clearBackup} disabled={submitting}>Убрать</button>
      </div>
    {:else}
      <label class={["btn", styles.backupBtn]}>
        {checkingBackup ? "Проверяю файл…" : "Выбрать файл бэкапа (.zip)…"}
        <input id="backup-file" type="file" accept=".zip,application/zip" disabled={submitting || checkingBackup} onchange={pickBackup}>
      </label>
    {/if}
  </div>

  <p class="hint">После установки панель предложит выбрать и настроить плагины.</p>
  <p class={styles.error} role="alert" hidden={!error}>{error}</p>
</form>

<div class="step-footer">
  <div class="actions">
    <button class="btn ghost" type="button" onclick={onBack} disabled={submitting}>Назад</button>
    <button class="btn accent" type="submit" form="install-settings" disabled={submitting}>
      Установить <span aria-hidden="true">→</span>
    </button>
  </div>
</div>
