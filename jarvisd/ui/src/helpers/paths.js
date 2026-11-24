const LEGACY_PAGES = new Set(["characters", "charts", "logs", "memory", "player", "security", "speaker", "training"]);

export function profilePath(id) {
  return "/profiles/" + encodeURIComponent(id);
}

export function legacyHashPath(hash) {
  if (!hash || hash[0] !== "#") return "";
  const route = hash.slice(1).replace(/^\/+/, "");
  if (!route) return "";
  if (route === "home") return "/";
  if (route === "plugins") return "/connections/plugins";
  if (route === "connections" || route.startsWith("connections/")) return "/" + route;
  if (route.startsWith("p/")) {
    const raw = route.slice(2);
    if (!raw) return "";
    let id = raw;
    try { id = decodeURIComponent(raw); } catch { /* keep raw */ }
    return profilePath(id);
  }
  return LEGACY_PAGES.has(route) ? "/" + route : "";
}
