<script>
  import { onMount } from "svelte";
  import { api } from "#helpers/core.js";

  let state = $state(null);
  let busy = $state(false);
  let message = $state("");
  let deviceName = $state("");
  let apiKey = $state("");
  let clientId = $state("");
  let clientSecret = $state("");

  onMount(() => { refresh(); });

  function apply(next) {
    state = next;
    deviceName = next.device_name ?? "";
  }

  async function run(work) {
    busy = true;
    message = "";
    try { await work(); }
    catch (error) { message = error.message; }
    finally { busy = false; }
  }

  function refresh() {
    return run(async () => apply(await api("GET", "/api/spotify/soloist")));
  }

  function save(event) {
    event.preventDefault();
    return run(async () => {
      const next = await api("PUT", "/api/spotify/soloist", {
        device_name: deviceName.trim(),
        api_key: apiKey.trim() || undefined,
        client_id: clientId.trim() || undefined,
        client_secret: clientSecret.trim() || undefined,
      });
      apiKey = clientId = clientSecret = "";
      apply(next);
      message = next.warning || "Настройки сохранены";
    });
  }

  function action(name) {
    return run(async () => {
      const next = await api("POST", "/api/spotify/soloist/" + name);
      if (name === "restart") {
        apply(next);
        message = "Команда перезапуска выполнена. Обновите статус после запуска Soloist.";
      } else if (name === "tune") {
        apply(await api("GET", "/api/spotify/soloist"));
        message = "Выбран выход " + next.audio_device;
      } else {
        message = "Spotify воспроизводит трек, звуковой выход ALSA активен";
      }
    });
  }
</script>

<section class="cn-body plg-form" aria-label="Настройки Soloist">
  <h3>Soloist</h3>
  {#if state?.installed}
    <p>{state.running ? "Процесс запущен" : "Процесс остановлен"} ·
      {state.auth === "connected" ? "Spotify Connect подключён" :
        state.auth === "not_logged_in" ? "Требуется вход в Spotify Connect" : "Статус Spotify Connect недоступен"}</p>
    {#if state.audio_device}<p>Аудиовыход <code>{state.audio_device}</code></p>{/if}
    <form class="plg-form" onsubmit={save}>
      <label>Имя устройства <input type="text" maxlength="128" bind:value={deviceName} disabled={busy} /></label>
      <label>API-ключ Soloist <input type="password" autocomplete="new-password" placeholder={state.api_key_set ? "Задан" : ""} bind:value={apiKey} disabled={busy} /></label>
      <label>Spotify Client ID <input type="password" autocomplete="new-password" placeholder={state.client_id_set ? "Задан" : ""} bind:value={clientId} disabled={busy} /></label>
      <label>Spotify Client Secret <input type="password" autocomplete="new-password" placeholder={state.client_secret_set ? "Задан" : ""} bind:value={clientSecret} disabled={busy} /></label>
      <button class="btn primary" type="submit" disabled={busy}>Сохранить</button>
    </form>
    <div class="row soloist-actions">
      <button class="btn" type="button" disabled={busy} onclick={() => action("restart")}>Перезапустить Soloist</button>
      <button class="btn" type="button" disabled={busy} onclick={() => action("check")}>Проверить звук</button>
      {#if !state.yam}
        <button class="btn" type="button" disabled={busy} onclick={() => action("tune")}>Подобрать выход</button>
      {/if}
      <button class="btn" type="button" disabled={busy} onclick={refresh}>Обновить статус</button>
    </div>
  {:else if state}
    <p>Soloist не установлен на колонке.</p>
    <a class="lnk" href="https://github.com/Horohi/soloist-station-lite" target="_blank" rel="noopener">Установка Soloist</a>
  {/if}
  {#if busy}<p role="status">Выполняется операция Soloist…</p>{/if}
  {#if message}<p role="status">{message}</p>{/if}
</section>

<style>
  form { display: grid; gap: .65rem; }
  label { display: grid; gap: .25rem; }
  .soloist-actions { flex-wrap: wrap; gap: .5rem; }
</style>

