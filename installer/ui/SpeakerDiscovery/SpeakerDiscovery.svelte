<script>
  import styles from "./SpeakerDiscovery.module.css";

  let { api, onSelect } = $props();
  let speakers = $state.raw([]);
  let scanStatus = $state("");
  let scanning = $state(false);
  let checking = $state(false);
  let address = $state("");

  async function scan() {
    scanning = true;
    speakers = [];
    scanStatus = "Ищу колонки в домашней сети…";
    try {
      const result = await api("GET", "/api/scan");
      speakers = result.speakers || [];
      scanStatus = speakers.length
        ? `Найдено: ${speakers.length}`
        : "Введите IP колонки, если она ещё не подключена по ADB.";
    } catch (error) {
      scanStatus = error.message;
    } finally {
      scanning = false;
    }
  }

  async function checkAddress(event) {
    event.preventDefault();
    if (checking) return;

    checking = true;
    try {
      const speaker = await api("POST", "/api/check", { ip: address.trim() });
      onSelect(speaker);
    } catch (error) {
      alert(error.message);
    } finally {
      checking = false;
    }
  }

  function speakerDetails(speaker) {
    const details = [speaker.adb_address ?? speaker.ip];
    if (speaker.free_mb) details.push(`свободно ${speaker.free_mb} МБ`);
    if (speaker.ram_mb) details.push(`${speaker.ram_mb} МБ памяти`);
    return details.join(" · ");
  }
</script>

<div class="step-body">
  <span class="eyebrow">Начнём знакомство</span>
  <h1>Найдём вашу<br> Станцию.</h1>
  <p class="lead">Выберите Станцию. Голос, музыку и умный дом настроим после установки.</p>
  <div class={styles.prerequisite}>
    <span class={styles.statusDot}></span>
    <p>
      Нужна колонка с root-доступом и ADB в той же сети, что и компьютер. Если на компьютере нет ADB,
      установщик скачает его с сайта Google. Продолжая, вы принимаете
      <a href="https://developer.android.com/studio/terms" target="_blank" rel="noopener noreferrer">условия Android SDK</a>
      для этой загрузки.
    </p>
  </div>

  <p class={styles.scanStatus} role="status" aria-live="polite" hidden={!scanStatus}>
    {#if scanning}
      <span class={styles.radar} aria-hidden="true"><i></i><i></i></span>
    {/if}
    {scanStatus}
  </p>
  <div>
    {#each speakers as speaker, index}
      <button
        type="button"
        class={[styles.speaker, speaker.problems?.length && styles.unavailable]}
        style:--i={index}
        onclick={() => onSelect(speaker)}
        disabled={checking}
      >
        <span class={[styles.ring, speaker.installed && styles.installed]} aria-hidden="true"></span>
        <span class={styles.speakerDetails}>
          <b>{speaker.model || "Колонка"}</b>
          <small>{speakerDetails(speaker)}</small>
        </span>
        {#if speaker.problems?.length}
          <span class={[styles.badge, styles.unsupported]}>не подходит</span>
        {:else if speaker.installed}
          <span class={[styles.badge, styles.installed]}>Джарвис {speaker.installed}</span>
        {:else}
          <span class={[styles.badge, styles.ready]}>можно ставить</span>
        {/if}
        <span class={styles.arrow} aria-hidden="true">→</span>
      </button>
    {/each}
  </div>

  <details class={styles.manual}>
    <summary>Указать адрес ADB вручную</summary>
    <form class={styles.addressForm} onsubmit={checkAddress}>
      <input type="text" placeholder="192.168.1.50:5556" aria-label="Адрес ADB колонки" bind:value={address}>
      <button class="btn" type="submit" disabled={checking}>Проверить</button>
    </form>
    <p class="hint">
      IP есть в приложении «Дом с Алисой» → колонка → «Информация об устройстве».
      Порт указывайте через двоеточие. По умолчанию используется 5555.
    </p>
  </details>
  <a class={styles.helpLink} href="https://hackalice.me/" target="_blank" rel="noopener noreferrer">
    Как подготовить колонку <span aria-hidden="true">↗</span>
  </a>
</div>
<div class="step-footer">
  <button class="btn accent wide" type="button" onclick={scan} disabled={scanning}>
    Найти колонки в сети <span aria-hidden="true">→</span>
  </button>
</div>
