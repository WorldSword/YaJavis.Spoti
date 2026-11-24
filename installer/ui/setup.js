import "/token.js";
import { mount } from "svelte";
import Installer from "./Installer/Installer.svelte";
import "./setup.css";

async function api(method, url, body, signal) {
  const response = await fetch(url, {
    method,
    headers: { "Content-Type": "application/json", "X-Token": window.TOKEN },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal,
  });
  const result = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(result.error || `Ошибка ${response.status}`);
  return result;
}

async function upload(url, file, signal) {
  const response = await fetch(url, {
    method: "POST",
    headers: { "Content-Type": "application/octet-stream", "X-Token": window.TOKEN },
    body: file,
    signal,
  });
  const result = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(result.error || `Ошибка ${response.status}`);
  return result;
}

mount(Installer, {
  target: document.getElementById("installer"),
  props: { api, upload, version: window.VERSION || "" },
});
