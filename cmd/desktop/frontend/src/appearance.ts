import {desktopAPI} from "./api";
import {errorCode} from "./locales";

// Применение внешнего вида. Цвета задают только встроенные темы, они совпадают с theme.css.
// Шрифт, его размер и подложка сцены выбираются независимо от темы.

export const builtinThemes = ["light", "neutral", "dark", "beige", "brown", "purple"] as const;
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
    window: "#f2f0eb",
    card: "#ffffff",
    cardStrong: "#ece9e2",
    line: "#e4e1da",
    text: "#25272b",
    muted: "#858890",
    accent: "#2ba97c",
    accentBlue: "#e56f38",
    danger: "#db3653",
    warning: "#b88712",
    onAccent: "#ffffff",
};

export const themePalettes: Record<BuiltinTheme, ThemePalette> = {
    light: lightPalette,
    neutral: {
        window: "#141416",
        card: "#1e1e22",
        cardStrong: "#2a2a30",
        line: "#3e3e46",
        text: "#ececf1",
        muted: "#a1a1ab",
        accent: "#3dccb0",
        accentBlue: "#6eb6e0",
        danger: "#f07480",
        warning: "#e2b56c",
        onAccent: "#10211c",
    },
    dark: darkPalette,
    beige: {
        window: "#e7e0d4",
        card: "#f4efe6",
        cardStrong: "#e8dfd2",
        line: "#d0c4b2",
        text: "#3a3228",
        muted: "#74685a",
        accent: "#2f6f5c",
        accentBlue: "#3a6988",
        danger: "#b23e3e",
        warning: "#8f5a22",
        onAccent: "#f7f3ec",
    },
    brown: {
        window: "#1a1410",
        card: "#271c16",
        cardStrong: "#3a2a20",
        line: "#534033",
        text: "#f3eadf",
        muted: "#b7a08c",
        accent: "#e0a15c",
        accentBlue: "#7eb4cc",
        danger: "#e4847a",
        warning: "#e8c48a",
        onAccent: "#2a1a0e",
    },
    purple: {
        window: "#17141f",
        card: "#221e2c",
        cardStrong: "#302a40",
        line: "#463f5c",
        text: "#f2eef8",
        muted: "#b3a8c6",
        accent: "#b7a3ef",
        accentBlue: "#7dcec4",
        danger: "#ef8d98",
        warning: "#e4b86e",
        onAccent: "#241833",
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

export function normalizeTheme(value: unknown): BuiltinTheme {
    return builtinThemes.find((theme) => theme === value) ?? "dark";
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
        theme: builtinThemes.find((theme) => theme === source.theme) ?? normalizeTheme(fallbackTheme),
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
