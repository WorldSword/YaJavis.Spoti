<script>
  import { onMount } from "svelte";
  import dashboard from "../screenshots/dashboard.png";
  import connections from "../screenshots/connections.png";
  import assistant from "../screenshots/assistant.png";
  import charts from "../screenshots/charts.png";
  import styles from "./Showcase.module.css";

  const slides = [
    { image: dashboard, title: "Всё под рукой.", text: "Диалог, музыка и состояние Станции — на одной странице.", alt: "Главная панель YaJarvis" },
    { image: connections, title: "Вы выбираете, с кем говорить.", text: "Подключите OpenAI, Gemini, Grok или ElevenLabs и устройства умного дома.", alt: "Подключения AI и умного дома" },
    { image: assistant, title: "Голос и характер — ваши.", text: "Выберите голос, модель и характер. Создавайте разных ассистентов и переключайте их в панели.", alt: "Настройки голосового ассистента" },
    { image: charts, title: "Понимайте свою Станцию.", text: "Память, процессор, температура и Wi-Fi — всё состояние Станции в браузере.", alt: "Графики состояния колонки" },
  ];
  const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)");
  let index = $state(0);
  let slide = $derived(slides[index]);
  let enlarged = $state();
  let paused = $state(reducedMotion.matches);
  let preview = $state();
  let caption = $state();
  let dialog = $state();

  function animateSlide() {
    if (reducedMotion.matches) return;
    for (const element of [preview, caption]) {
      for (const animation of element.getAnimations()) animation.cancel();
      element.animate(
        [{ opacity: 0, transform: "translateY(8px)" }, { opacity: 1, transform: "translateY(0)" }],
        { duration: 420, easing: "cubic-bezier(.22,1,.36,1)" },
      );
    }
  }

  function showSlide(next) {
    index = (next + slides.length) % slides.length;
    animateSlide();
  }

  function selectSlide(next) {
    paused = true;
    showSlide(next);
  }

  function openPreview() {
    paused = true;
    enlarged = slide;
    dialog.showModal();
  }

  onMount(() => {
    const modal = dialog;
    function closeOnBackdrop(event) {
      if (event.target === modal) modal.close();
    }
    modal.addEventListener("click", closeOnBackdrop);
    animateSlide();
    const timer = setInterval(() => {
      if (!paused && !document.hidden) showSlide(index + 1);
    }, 8500);
    return () => {
      clearInterval(timer);
      modal.removeEventListener("click", closeOnBackdrop);
    };
  });
</script>

<aside class={styles.root} aria-label="Знакомство с YaJarvis">
  <div class={styles.heading}>
    <span class={styles.eyebrow}>Больше возможностей вашей Станции</span>
    <h2>Ваша Станция. <span>Новые возможности.</span></h2>
    <p>Выберите голос. Включите музыку. Управляйте домом.</p>
  </div>
  <button type="button" class={styles.preview} onclick={openPreview} aria-label="Рассмотреть скриншот панели">
    <span class={styles.previewBar}><span></span><span></span><span></span><small>Панель YaJarvis</small><span class={styles.zoomHint}>Развернуть ↗</span></span>
    <img bind:this={preview} src={slide.image} alt={slide.alt} width="2000" height="1125">
  </button>
  <div class={styles.caption}>
    <div class={styles.copy} bind:this={caption}>
      <span class={styles.number}>{String(index + 1).padStart(2, "0")} / {String(slides.length).padStart(2, "0")}</span>
      <h3>{slide.title}</h3>
      <p>{slide.text}</p>
    </div>
    <div class={styles.controls}>
      <button class={styles.iconButton} type="button" onclick={() => selectSlide(index - 1)} aria-label="Предыдущий скриншот">←</button>
      <button class={styles.iconButton} type="button" onclick={() => (paused = !paused)} aria-label={paused ? "Продолжить показ" : "Приостановить показ"} aria-pressed={paused}>
        <svg viewBox="0 0 24 24" aria-hidden="true">
          <path d={paused ? "m9 5 10 7-10 7Z" : "M9 5v14M15 5v14"}/>
        </svg>
      </button>
      <button class={styles.iconButton} type="button" onclick={() => selectSlide(index + 1)} aria-label="Следующий скриншот">→</button>
    </div>
  </div>
  <div class={styles.dots} aria-label="Скриншоты панели">
    {#each slides as item, position (item.image)}
      <button type="button" onclick={() => selectSlide(position)} aria-label={item.title} aria-current={position === index}></button>
    {/each}
  </div>
  <div class={styles.featureStrip}><span><i></i>Активация на колонке</span><span><i></i>AI на ваш выбор</span><span><i></i>Без своего сервера</span></div>
  <p class={styles.demoNote}>Пример настроенной панели. Свои аккаунты вы подключите после установки.</p>

  <dialog bind:this={dialog} class={styles.dialog} aria-labelledby="large-title">
    <header>
      <strong id="large-title">{enlarged?.title}</strong>
      <button class={styles.iconButton} type="button" onclick={() => dialog.close()} aria-label="Закрыть скриншот">×</button>
    </header>
    <img src={enlarged?.image} alt={enlarged?.alt ?? ""}>
    <p>Esc — закрыть</p>
  </dialog>
</aside>
