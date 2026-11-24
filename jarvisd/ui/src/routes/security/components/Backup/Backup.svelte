<script>
  import { icon, toast, upload, waitRestart } from "#helpers/core.js";
  import { showGate } from "#state/panelState.js";
  import styles from "./Backup.module.css";

  const controller = new AbortController();
  const { signal } = controller;
  let busy = $state("");

  function filenameFrom(response) {
    const header = response.headers.get("Content-Disposition") || "";
    const match = header.match(/filename="?([^";\s]+)"?/i);
    if (match?.[1]) return match[1];
    return `yajarvis-backup-${new Date().toISOString().slice(0, 10)}.zip`;
  }

  async function downloadBackup() {
    if (busy) return;
    busy = "download";
    try {
      const response = await fetch("/api/backup", { credentials: "same-origin", signal });
      if (response.status === 401 || response.status === 428) {
        const d = await response.json().catch(() => ({}));
        showGate(d.setup);
        throw new Error(d.error || "Нужно войти");
      }
      if (!response.ok) {
        const d = await response.json().catch(() => ({}));
        throw new Error(d.error || `Ошибка ${response.status}`);
      }
      const blob = await response.blob();
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = filenameFrom(response);
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
      toast("Бэкап скачан");
    } catch (error) {
      if (!signal.aborted) toast(error.message, true);
    } finally {
      busy = "";
    }
  }

  async function restoreBackup(event) {
    const input = event.currentTarget;
    const file = input.files?.[0];
    input.value = "";
    if (!file || busy) return;
    if (!confirm("Восстановить настройки, память и данные плагинов из бэкапа? Текущие настройки будут заменены, а Джарвис перезапустится.")) {
      return;
    }
    busy = "restore";
    try {
      toast("Проверяю и загружаю бэкап…");
      const res = await upload("POST", "/api/backup/restore", file, { signal });
      const msg = res.pending_plugins?.length
        ? "Бэкап восстановлен — Джарвис перезапускается и доустановит плагины"
        : "Бэкап восстановлен — Джарвис перезапускается";
      await waitRestart(msg);
    } catch (error) {
      if (!signal.aborted) toast(error.message, true);
    } finally {
      busy = "";
    }
  }
</script>

<div class={styles.backup}>
  <div class={["row", styles.actions]}>
    <button class="btn sm primary" type="button" disabled={!!busy} onclick={downloadBackup}>
      {busy === "download" ? "Собираю…" : "Скачать бэкап"}
    </button>
    <label class={["btn sm ghost", styles.file]} data-tip="Восстановить из .zip архива резервной копии">
      {@html icon("upload")}
      {busy === "restore" ? "Восстанавливаю…" : "Восстановить из файла…"}
      <input type="file" accept=".zip,application/zip" disabled={!!busy} onchange={restoreBackup}>
    </label>
  </div>
  <small class="muted">
    В архиве сохраняются настройки, ключи, ассистенты, память и данные плагинов (без аудиозаписей обучения). Храните файл в надёжном месте.
  </small>
</div>
