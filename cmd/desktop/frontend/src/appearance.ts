import {desktopAPI} from "./api";
import {errorCode} from "./locales";

// Применение внешнего вида. Цвета задают только встроенные темы, они совпадают с theme.css.
// Шрифт, его размер и подложка сцены выбираются независимо от темы.

export const builtinThemes = ["light", "dark", "navy", "beige", "brown", "purple"] as const;
export type BuiltinTheme = (typeof builtinThemes)[number];

export const stageBackdrops = ["rings", "mountains", "pines", "clouds", "horizon", "city", "stars", "plain"] as const;
export type StageBackdrop = (typeof stageBackdrops)[number];

// "default" — Arial в основных окнах и Noto Sans в остальных, см. :root[data-font="default"].
export const fontFamilies = [
    "default",
    "Noto Sans",
    "Golos Text",
    "Manrope",
    "Nunito",
    "Rubik",
    "Montserrat",
    "Noto Serif",
    "Literata",
] as const;
export type FontFamily = (typeof fontFamilies)[number];

export const fontSizes = [14, 15, 16, 17, 18] as const;
export type FontSize = (typeof fontSizes)[number];
export const defaultFontSize: FontSize = 16;
export type PaletteToken =
    "window" | "card" | "cardStrong" | "line" | "text" | "muted" | "accent" | "accentBlue" | "danger" | "warning" | "onAccent";
export type ThemePalette = Record<PaletteToken, string>;

export const darkPalette: ThemePalette = {
    window: "#0d0f12",
    card: "#15181e",
    cardStrong: "#1e222a",
    line: "#292e38",
    text: "#f1f2f3",
    muted: "#9399a5",
    accent: "#4bc49a",
    accentBlue: "#ff7942",
    danger: "#ff5672",
    warning: "#f2c35d",
    onAccent: "#10261f",
};

export const lightPalette: ThemePalette = {
    window: "#d1d7dd",
    card: "#f2f4f7",
    cardStrong: "#e1e6ec",
    line: "#d4dae2",
    text: "#1f2630",
    muted: "#586373",
    accent: "#21785e",
    accentBlue: "#3d62a0",
    danger: "#b8394b",
    warning: "#875f0f",
    onAccent: "#ffffff",
};

export const themePalettes: Record<BuiltinTheme, ThemePalette> = {
    light: lightPalette,
    dark: darkPalette,
    navy: {
        window: "#0b1426",
        card: "#121e36",
        cardStrong: "#1b2a48",
        line: "#2a3d62",
        text: "#e8eefb",
        muted: "#94a3c0",
        accent: "#4cc9f0",
        accentBlue: "#5b9bff",
        danger: "#ff6b81",
        warning: "#f5d06f",
        onAccent: "#06202b",
    },
    beige: {
        window: "#d8cfc1",
        card: "#f5f0e8",
        cardStrong: "#e7dfd2",
        line: "#d8cdbd",
        text: "#2d261e",
        muted: "#6a5e4f",
        accent: "#2f7a55",
        accentBlue: "#a1572c",
        danger: "#b4383f",
        warning: "#87590b",
        onAccent: "#ffffff",
    },
    brown: {
        window: "#15100c",
        card: "#1e1712",
        cardStrong: "#2a211a",
        line: "#3a2e25",
        text: "#f3eae0",
        muted: "#ae9d8c",
        accent: "#8fc48a",
        accentBlue: "#e4a35f",
        danger: "#f07f73",
        warning: "#f2d06b",
        onAccent: "#1f1409",
    },
    purple: {
        window: "#110e19",
        card: "#191523",
        cardStrong: "#241e32",
        line: "#342c46",
        text: "#f0ecf8",
        muted: "#a69db9",
        accent: "#5fd4a8",
        accentBlue: "#a98bff",
        danger: "#ff6f8d",
        warning: "#f3cc6b",
        onAccent: "#170f2b",
    },
};

export type Appearance = {
    theme: BuiltinTheme;
    fontFamily: FontFamily;
    fontSize: FontSize;
    stageBackdrop: StageBackdrop;
};
export type AppearanceOptions = Partial<Omit<Appearance, "theme">>;

const storageKey = "sonoryx-appearance";
const storageVersion = 2;
const legacyThemeKey = "sonoryx-theme";

export function defaultAppearance(): Appearance {
    return {
        theme: "dark",
        fontFamily: "default",
        fontSize: defaultFontSize,
        stageBackdrop: "rings",
    };
}

// "neutral" was the grey dark theme; its place is taken by the navy one.
const legacyThemes: Record<string, BuiltinTheme> = {neutral: "navy"};

function knownTheme(value: unknown): BuiltinTheme | undefined {
    return builtinThemes.find((theme) => theme === value) ?? (typeof value === "string" ? legacyThemes[value] : undefined);
}

export function normalizeTheme(value: unknown): BuiltinTheme {
    return knownTheme(value) ?? "dark";
}

function nearestFontSize(value: number): FontSize {
    let best: FontSize = defaultFontSize;
    for (const size of fontSizes) {
        if (Math.abs(size - value) < Math.abs(best - value)) best = size;
    }
    return best;
}

function normalizeStageBackdrop(value: unknown): StageBackdrop {
    const name = value === "dunes" ? "pines" : value === "waves" ? "clouds" : value === "aurora" ? "city" : value;
    return stageBackdrops.find((backdrop) => backdrop === name) ?? "rings";
}

export function fontStack(family: FontFamily): string {
    return family === "default" ? "Arial, sans-serif" : `"${family}", "Noto Sans", sans-serif`;
}

export function normalizeAppearance(value: unknown, fallbackTheme = "dark"): Appearance {
    const base = defaultAppearance();
    const source = value && typeof value === "object" ? value as Partial<Appearance> : {};
    const requestedSize = Number(source.fontSize);
    return {
        theme: knownTheme(source.theme) ?? normalizeTheme(fallbackTheme),
        fontFamily: fontFamilies.find((family) => family === source.fontFamily) ?? base.fontFamily,
        fontSize: Number.isFinite(requestedSize) ? nearestFontSize(Math.round(requestedSize)) : base.fontSize,
        stageBackdrop: normalizeStageBackdrop(source.stageBackdrop),
    };
}

// До версии 2 шрифт, размер и подложка хранились всегда, а действовали только в своей теме.
// У встроенной темы их не было видно, поэтому они не переносятся. У своей темы переносятся,
// а вместо её палитры берётся последняя выбранная встроенная тема.
function readStored(stored: unknown, fallbackTheme: string): Appearance {
    const source = stored && typeof stored === "object" ? stored as {version?: unknown; theme?: unknown} : {};
    const keepOptions = source.version === storageVersion || source.theme === "custom";
    return normalizeAppearance(keepOptions ? source : {theme: source.theme}, fallbackTheme);
}

export function loadAppearance(): Appearance {
    const legacy = localStorage.getItem(legacyThemeKey) ?? "dark";
    try {
        return readStored(JSON.parse(localStorage.getItem(storageKey) ?? "null"), legacy);
    } catch {
        return readStored(null, legacy);
    }
}

export function saveAppearance(appearance: Appearance): Appearance {
    const normalized = normalizeAppearance(appearance);
    try {
        localStorage.setItem(storageKey, JSON.stringify({version: storageVersion, ...normalized}));
        localStorage.setItem(legacyThemeKey, normalized.theme);
    } catch {
        throw new Error(errorCode("theme-storage"));
    }
    return normalized;
}

export function themeColor(variable: string, alpha = 1): string {
    const raw = getComputedStyle(document.documentElement).getPropertyValue(variable).trim();
    const hex = raw.match(/^#([0-9a-f]{6})$/i);
    if (!hex) return raw || "#888888";
    const value = parseInt(hex[1], 16);
    return `rgba(${(value >> 16) & 255}, ${(value >> 8) & 255}, ${value & 255}, ${alpha})`;
}

function encodeColor(color: string): string | null {
    const hex = color.trim().match(/^#([0-9a-f]{6})$/i);
    if (hex) return `%23${hex[1]}`;
    const rgb = color.trim().match(/^rgba?\(\s*(\d{1,3})[,\s]+(\d{1,3})[,\s]+(\d{1,3})/i);
    if (!rgb) return null;
    const channel = (part: string) => {
        const number = Number(part);
        if (number > 255) return null;
        return number.toString(16).padStart(2, "0");
    };
    const red = channel(rgb[1]);
    const green = channel(rgb[2]);
    const blue = channel(rgb[3]);
    if (!red || !green || !blue) return null;
    return `%23${red}${green}${blue}`;
}

function chevron(color: string): string {
    const stroke = encodeColor(color) ?? "%2389a1b9";
    return `url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='12' height='8' viewBox='0 0 12 8'%3E%3Cpath d='M1.2 1.5 6 6.3 10.8 1.5' fill='none' stroke='${stroke}' stroke-width='1.7' stroke-linecap='round' stroke-linejoin='round'/%3E%3C/svg%3E")`;
}

export function applyAppearance(appearance: Appearance): void {
    const root = document.documentElement;
    const {theme, fontFamily, fontSize} = normalizeAppearance(appearance);
    root.dataset.theme = theme;
    root.dataset.font = fontFamily === "default" ? "default" : "selected";
    if (fontFamily === "default") root.style.removeProperty("--font-family");
    else root.style.setProperty("--font-family", fontStack(fontFamily));
    root.style.setProperty("--font-size", `${fontSize}px`);

    const computed = getComputedStyle(root);
    root.style.setProperty("--field-chevron", chevron(computed.getPropertyValue("--muted")));
    root.style.setProperty("--field-chevron-active", chevron(computed.getPropertyValue("--text")));

    const palette = themePalettes[theme];
    void desktopAPI.setPopupLook({
        card: palette.card,
        line: palette.line,
        text: palette.text,
        muted: palette.muted,
    }).catch(() => undefined);
}
