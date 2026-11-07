import {describe, expect, it} from "vitest";
import {hotkeyLabel, isMacPlatform, macReservedHotkey} from "./hotkeyLabels";

describe("hotkeyLabel", () => {
    it("keeps the Windows names elsewhere", () => {
        expect(hotkeyLabel("Ctrl+Alt+M", false)).toBe("Ctrl + Alt + M");
        expect(hotkeyLabel("F5", false)).toBe("F5");
    });

    it("shows Mac modifier symbols in the stored order", () => {
        expect(hotkeyLabel("Ctrl+Alt+M", true)).toBe("⌃⌥M");
        expect(hotkeyLabel("Ctrl+Alt+Shift+Win+0", true)).toBe("⌃⌥⇧⌘0");
        expect(hotkeyLabel("Win+F12", true)).toBe("⌘F12");
        expect(hotkeyLabel("F1", true)).toBe("F1");
    });
});

describe("isMacPlatform", () => {
    it("detects WKWebView on macOS", () => {
        expect(isMacPlatform("MacIntel", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15")).toBe(true);
    });

    it("does not treat WebView2 as a Mac", () => {
        expect(isMacPlatform("Win32", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Edg/140.0")).toBe(false);
    });
});

describe("macReservedHotkey", () => {
    it("rejects the Command chords macOS menus own", () => {
        for (const key of ["Win+Q", "Win+W", "Win+H", "Win+M", "Win+Shift+Q", "Win+Shift+W"]) {
            expect(macReservedHotkey(key)).toBe(true);
        }
    });

    it("allows other chords, including Command with more modifiers", () => {
        for (const key of ["Ctrl+Alt+M", "Win+Shift+M", "Ctrl+Win+Q", "Win+E", "M"]) {
            expect(macReservedHotkey(key)).toBe(false);
        }
    });
});
