<script>
  import { onDestroy } from "svelte";
  import { writable } from "svelte/store";
  import DeviceCheck from "../DeviceCheck/DeviceCheck.svelte";
  import InstallProgress from "../InstallProgress/InstallProgress.svelte";
  import InstallSettings from "../InstallSettings/InstallSettings.svelte";
  import Showcase from "../Showcase/Showcase.svelte";
  import SpeakerDiscovery from "../SpeakerDiscovery/SpeakerDiscovery.svelte";
  import styles from "./Installer.module.css";

  const steps = ["Колонка", "Проверка", "Настройки", "Установка"];
  const selectedSpeaker = writable(null);
  const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)");

  let { api, upload, version } = $props();
  let currentStep = $state(1);
  let operation = $state("install");
  let wizard = $state();
  let pointerFrame = 0;
  let pointer;

  $effect(() => {
    const body = wizard?.querySelector(`#s${currentStep} .step-body`);
    if (body) body.scrollTop = 0;
  });

  onDestroy(() => cancelAnimationFrame(pointerFrame));

  function selectSpeaker(speaker) {
    selectedSpeaker.set(speaker);
    currentStep = 2;
  }

  function showProgress(nextOperation) {
    operation = nextOperation;
    currentStep = 4;
  }

  function moveGlow(event) {
    if (event.pointerType === "touch" || reducedMotion.matches) return;
    pointer = event;
    if (pointerFrame) return;

    pointerFrame = requestAnimationFrame(() => {
      pointerFrame = 0;
      const card = pointer.target.closest(`.${styles.card}`);
      if (!card) return;

      const box = card.getBoundingClientRect();
      card.style.setProperty("--mx", `${pointer.clientX - box.left}px`);
      card.style.setProperty("--my", `${pointer.clientY - box.top}px`);
    });
  }
</script>

<svelte:window onpointermove={moveGlow}/>

<main class={styles.wrap}>
  <header class={styles.topbar}>
    <div class={styles.brand}>
      <span class={styles.logo}></span>
      <span>YaJarvis<small>Установка Джарвиса</small></span>
    </div>
    <div class={styles.topMeta}>
      <span class={styles.version}>{version}</span>
      <a href="https://github.com/newfpv/YaJarvis" target="_blank" rel="noopener noreferrer">
        Проект на GitHub <span aria-hidden="true">↗</span>
      </a>
    </div>
  </header>

  <div class={styles.setupGrid}>
    <div class={styles.wizard} bind:this={wizard}>
      <ol class={styles.steps} style:--step={currentStep - 1} aria-label="Этапы установки">
        {#each steps as title, index (title)}
          {@const number = index + 1}
          <li
            class={[currentStep === number && styles.current, currentStep > number && styles.completed]}
            aria-current={currentStep === number ? "step" : undefined}
          >{title}</li>
        {/each}
      </ol>
      <section id="s1" class={styles.card} hidden={currentStep !== 1}>
        <SpeakerDiscovery {api} onSelect={selectSpeaker}/>
      </section>
      <section id="s2" class={styles.card} hidden={currentStep !== 2}>
        <DeviceCheck
          {api}
          {upload}
          {selectedSpeaker}
          onBack={() => (currentStep = 1)}
          onContinue={() => (currentStep = 3)}
          onRestored={() => showProgress("restore")}
          onUninstalled={() => showProgress("uninstall")}
        />
      </section>
      <section id="s3" class={styles.card} hidden={currentStep !== 3}>
        <InstallSettings
          {api}
          {upload}
          {selectedSpeaker}
          onBack={() => (currentStep = 2)}
          onStarted={() => showProgress("install")}
        />
      </section>
      {#if currentStep === 4}
        <section id="s4" class={styles.card} aria-live="polite">
          <InstallProgress {api} {operation} onAgain={() => (currentStep = 1)}/>
        </section>
      {/if}
    </div>
    <Showcase/>
  </div>

  <footer class={styles.bottomBar}>
    <span>Установка на вашу колонку · Без отдельного сервера</span>
    <a href="https://github.com/newfpv/YaJarvis#contributors-и-авторы-проектов" target="_blank" rel="noopener noreferrer">
      Авторы и благодарности
    </a>
  </footer>
</main>
