// Shortcuts are stored as Ctrl/Alt/Shift/Win on every platform; a Mac
// keyboard has Control, Option, Shift and Command in those places.
export const isMacPlatform = (platform: string, userAgent: string): boolean =>
    /Mac/.test(platform) || /Macintosh/.test(userAgent);

const macSymbols: Record<string, string> = {Ctrl: "⌃", Alt: "⌥", Shift: "⇧", Win: "⌘"};

// hotkeyLabel shows "Ctrl + Alt + M" on Windows and "⌃⌥M" on a Mac.
export function hotkeyLabel(key: string, mac: boolean): string {
    const parts = key.split("+");
    if (!mac) return parts.join(" + ");
    const last = parts.pop() ?? "";
    return parts.map((part) => macSymbols[part] ?? part).join("") + last;
}

// macOS menus own these Command chords: Quit, Close, Hide and Minimize.
// AppKit runs them before the page sees the key, and a global hot key on
// them would take them away from every other app.
export function macReservedHotkey(shortcut: string): boolean {
    return /^Win\+[QWHM]$/.test(shortcut) || /^Win\+Shift\+[QW]$/.test(shortcut);
}
