const reduceMotionKey = "sonoryx.reduceMotion";

// The app always animates, whatever Windows "animation effects" say
// (prefers-reduced-motion is ignored). This preference turns animations off in
// the app only; the CSS reads it as :root[data-reduce-motion].
export function loadReduceMotion(): boolean {
    try { return localStorage.getItem(reduceMotionKey) === "true"; }
    catch { return false; }
}

export function applyReduceMotion(enabled: boolean): void {
    if (enabled) document.documentElement.dataset.reduceMotion = "true";
    else delete document.documentElement.dataset.reduceMotion;
}

export function saveReduceMotion(enabled: boolean): void {
    try { localStorage.setItem(reduceMotionKey, String(enabled)); } catch { /* Session choice survives. */ }
    applyReduceMotion(enabled);
}
