const key = "sonoryx.web.notify";

export function notificationsEnabled() {
  try { return typeof Notification !== "undefined" && Notification.permission === "granted" && localStorage.getItem(key) === "1"; }
  catch { return false; }
}

export async function setNotifications(enabled: boolean) {
  if (!enabled) {
    localStorage.removeItem(key);
    return false;
  }
  if (typeof Notification === "undefined" || await Notification.requestPermission() !== "granted") return false;
  localStorage.setItem(key, "1");
  return true;
}

export function notifyWhenHidden(title: string, body: string, tag: string) {
  if (document.visibilityState === "visible" || !notificationsEnabled()) return;
  try { new Notification(title, { body, tag, icon: "/sonoryx.svg" }); }
  catch { /* The browser can deny delivery while preserving chat. */ }
}
