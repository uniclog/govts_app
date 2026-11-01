const forceMotionKey = "sonoryx.forceMotion";

// Windows "animation effects" reach the app as prefers-reduced-motion. This preference
// only makes the app's CSS ignore that signal; the Windows setting itself stays untouched.
export function loadForceMotion(): boolean {
    try { return localStorage.getItem(forceMotionKey) !== "false"; }
    catch { return true; }
}

export function applyForceMotion(enabled: boolean): void {
    if (enabled) document.documentElement.dataset.forceMotion = "true";
    else delete document.documentElement.dataset.forceMotion;
}

export function saveForceMotion(enabled: boolean): void {
    try { localStorage.setItem(forceMotionKey, String(enabled)); } catch { /* Session choice survives. */ }
    applyForceMotion(enabled);
}
