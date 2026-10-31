import {desktopAPI} from "./api";
import {errorCode} from "./locales";

// Применение внешнего вида. Базовые цвета совпадают с theme.css.
// Встроенные темы только подменяют палитру. Своя тема дополнительно
// задаёт шрифт и подложку сцены. Производные роли пересчитываются в CSS.

export const builtinThemes = ["light", "neutral", "dark", "beige", "brown", "purple"] as const;
export type BuiltinTheme = (typeof builtinThemes)[number];

export const themeModes = ["light", "neutral", "dark", "beige", "brown", "purple", "custom"] as const;
export type ThemeMode = (typeof themeModes)[number];

export const stageBackdrops = ["mountains", "pines", "clouds", "horizon", "city", "stars", "plain", "image"] as const;
export type StageBackdrop = (typeof stageBackdrops)[number];

export const stageBackdropOptions: {value: StageBackdrop; label: string}[] = [
    {value: "mountains", label: "Горы"},
    {value: "pines", label: "Сосны"},
    {value: "clouds", label: "Облака"},
    {value: "horizon", label: "Горизонт"},
    {value: "city", label: "Город"},
    {value: "stars", label: "Звёзды"},
    {value: "plain", label: "Однотонная"},
    {value: "image", label: "Своя картинка"},
];

export const fontFamilies = [
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
export const maxStageImageBytes = 1_200_000;
export const maxSavedThemes = 24;
export const maxThemeFileBytes = 2_000_000;
const themeFileFormat = "sonoryx-theme";

export const paletteTokens = [
    "window",
    "card",
    "cardStrong",
    "line",
    "text",
    "muted",
    "accent",
    "accentBlue",
    "danger",
    "warning",
    "onAccent",
] as const;
export type PaletteToken = (typeof paletteTokens)[number];
export type ThemePalette = Record<PaletteToken, string>;

const paletteVariables: Record<PaletteToken, string> = {
    window: "--window",
    card: "--card",
    cardStrong: "--card-strong",
    line: "--line",
    text: "--text",
    muted: "--muted",
    accent: "--accent",
    accentBlue: "--accent-blue",
    danger: "--danger",
    warning: "--warning",
    onAccent: "--on-accent",
};

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

export type SavedTheme = {
    id: string;
    name: string;
    palette: ThemePalette;
    fontFamily: FontFamily;
    fontSize: FontSize;
    stageBackdrop: StageBackdrop;
    stageImage: string;
};

export type Appearance = {
    theme: ThemeMode;
    hasCustom: boolean;
    hasDraft: boolean;
    customName: string;
    activeSavedId: string;
    savedThemes: SavedTheme[];
    custom: ThemePalette;
    fontFamily: FontFamily;
    fontSize: FontSize;
    stageBackdrop: StageBackdrop;
    stageImage: string;
};

const storageKey = "sonoryx-appearance";
const legacyThemeKey = "sonoryx-theme";

export function defaultAppearance(): Appearance {
    return {
        theme: "dark",
        hasCustom: false,
        hasDraft: false,
        customName: "",
        activeSavedId: "",
        savedThemes: [],
        custom: {...darkPalette},
        fontFamily: "Noto Sans",
        fontSize: defaultFontSize,
        stageBackdrop: "mountains",
        stageImage: "",
    };
}

export function normalizeTheme(value: string): ThemeMode {
    return themeModes.find((mode) => mode === value) ?? "dark";
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
    return stageBackdrops.find((backdrop) => backdrop === name) ?? "mountains";
}

export function fontStack(family: string): string {
    return `"${family}", "Noto Sans", sans-serif`;
}

export function normalizeAppearance(value: unknown, fallbackTheme = "dark"): Appearance {
    const base = defaultAppearance();
    const source = value && typeof value === "object" ? value as Partial<Appearance> : {};
    const custom = {...darkPalette};
    const rawCustom = source.custom && typeof source.custom === "object" ? source.custom as Partial<ThemePalette> : {};
    for (const token of paletteTokens) {
        const color = rawCustom[token];
        if (typeof color === "string" && /^#[0-9a-f]{6}$/i.test(color)) custom[token] = color.toLowerCase();
    }
    const requestedSize = Number(source.fontSize);
    const fontFamily = fontFamilies.find((family) => family === source.fontFamily) ?? base.fontFamily;
    const stageBackdrop = normalizeStageBackdrop(source.stageBackdrop);
    const theme = normalizeTheme(typeof source.theme === "string" ? source.theme : fallbackTheme);
    const fontSize = Number.isFinite(requestedSize) ? nearestFontSize(Math.round(requestedSize)) : base.fontSize;
    const stageImage = typeof source.stageImage === "string" ? source.stageImage : "";
    const savedThemes = normalizeSavedThemes(source.savedThemes);
    const activeSavedId = savedThemes.some((item) => item.id === source.activeSavedId) ? source.activeSavedId ?? "" : "";
    return {
        theme,
        hasCustom: source.hasCustom === true || theme === "custom" || savedThemes.length > 0,
        hasDraft: source.hasDraft === true || (theme === "custom" && !activeSavedId),
        customName: cleanThemeName(source.customName, ""),
        activeSavedId,
        savedThemes,
        custom,
        fontFamily,
        fontSize,
        stageBackdrop,
        stageImage,
    };
}

export function cleanThemeName(value: unknown, fallback = "Своя тема"): string {
    const name = typeof value === "string" ? value.replace(/\s+/g, " ").trim().slice(0, 40) : "";
    return name || fallback;
}

function normalizeSavedThemes(value: unknown): SavedTheme[] {
    if (!Array.isArray(value)) return [];
    const themes: SavedTheme[] = [];
    for (const item of value) {
        const theme = normalizeSavedTheme(item);
        if (!theme || themes.some((saved) => saved.id === theme.id)) continue;
        themes.push(theme);
        if (themes.length >= maxSavedThemes) break;
    }
    return themes;
}

function normalizeSavedTheme(value: unknown): SavedTheme | null {
    if (!value || typeof value !== "object") return null;
    const source = value as Partial<SavedTheme>;
    if (typeof source.id !== "string" || !/^[\w-]{8,80}$/.test(source.id)) return null;
    const appearance = normalizeAppearance({
        theme: "custom",
        custom: source.palette,
        fontFamily: source.fontFamily,
        fontSize: source.fontSize,
        stageBackdrop: source.stageBackdrop,
        stageImage: source.stageImage,
        savedThemes: [],
    });
    return {
        id: source.id,
        name: cleanThemeName(source.name),
        palette: appearance.custom,
        fontFamily: appearance.fontFamily,
        fontSize: appearance.fontSize,
        stageBackdrop: appearance.stageBackdrop,
        stageImage: appearance.stageBackdrop === "image" ? appearance.stageImage : "",
    };
}

export function snapshotTheme(appearance: Appearance, name = appearance.customName): SavedTheme {
    return {
        id: appearance.activeSavedId || crypto.randomUUID(),
        name: cleanThemeName(name),
        palette: {...appearance.custom},
        fontFamily: appearance.fontFamily,
        fontSize: appearance.fontSize,
        stageBackdrop: appearance.stageBackdrop,
        stageImage: appearance.stageBackdrop === "image" ? appearance.stageImage : "",
    };
}

export function createCustomAppearance(current: Appearance): Appearance {
    const palette = current.theme === "custom" ? current.custom : themePalettes[current.theme];
    return normalizeAppearance({
        ...current,
        theme: "custom",
        hasCustom: true,
        hasDraft: true,
        customName: "",
        activeSavedId: "",
        custom: {...palette},
    });
}

export function downloadThemeFile(theme: SavedTheme): void {
    const payload = {
        format: themeFileFormat,
        version: 1,
        name: theme.name,
        colors: theme.palette,
        fontFamily: theme.fontFamily,
        fontSize: theme.fontSize,
        stageBackdrop: theme.stageBackdrop,
        stageImage: theme.stageImage,
    };
    const blob = new Blob([JSON.stringify(payload, null, 2)], {type: "application/json"});
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    const safe = theme.name.replace(/[^\p{L}\p{N} _-]+/gu, "").trim() || "sonoryx-theme";
    link.href = url;
    link.download = `${safe}.json`;
    link.click();
    URL.revokeObjectURL(url);
}

export function readThemeFile(file: File): Promise<SavedTheme> {
    if (file.size > maxThemeFileBytes) return Promise.reject(new Error(errorCode("theme-file-too-big")));
    return file.text().then((text) => {
        let parsed: unknown;
        try {
            parsed = JSON.parse(text);
        } catch {
            throw new Error(errorCode("theme-file-corrupt"));
        }
        if (!parsed || typeof parsed !== "object") throw new Error(errorCode("theme-not-sonoryx"));
        const source = parsed as {format?: unknown; version?: unknown; name?: unknown; colors?: unknown; fontFamily?: unknown; fontSize?: unknown; stageBackdrop?: unknown; stageImage?: unknown};
        if (source.format !== themeFileFormat || source.version !== 1) throw new Error(errorCode("theme-not-sonoryx"));
        const theme = normalizeSavedTheme({
            id: crypto.randomUUID(),
            name: source.name,
            palette: source.colors,
            fontFamily: source.fontFamily,
            fontSize: source.fontSize,
            stageBackdrop: source.stageBackdrop,
            stageImage: source.stageImage,
        });
        if (!theme) throw new Error(errorCode("theme-no-colors"));
        return theme;
    });
}

export function loadAppearance(): Appearance {
    const legacy = localStorage.getItem(legacyThemeKey) ?? "dark";
    try {
        const raw = localStorage.getItem(storageKey);
        if (!raw) return normalizeAppearance({theme: legacy}, legacy);
        return normalizeAppearance(JSON.parse(raw), legacy);
    } catch {
        return normalizeAppearance({theme: legacy}, legacy);
    }
}

export function saveAppearance(appearance: Appearance): Appearance {
    const normalized = normalizeAppearance(appearance);
    try {
        localStorage.setItem(storageKey, JSON.stringify(normalized));
    } catch {
        throw new Error(errorCode("theme-storage"));
    }
    if (normalized.theme !== "custom") localStorage.setItem(legacyThemeKey, normalized.theme);
    return normalized;
}

export function themeColor(variable: string, alpha = 1): string {
    const raw = getComputedStyle(document.documentElement).getPropertyValue(variable).trim();
    const hex = raw.match(/^#([0-9a-f]{6})$/i);
    if (!hex) return raw || "#888888";
    const value = parseInt(hex[1], 16);
    return `rgba(${(value >> 16) & 255}, ${(value >> 8) & 255}, ${value & 255}, ${alpha})`;
}

function stageImageValue(value: string): string | null {
    const trimmed = value.trim();
    if (/^https:\/\/[^\s"'\\()]+$/.test(trimmed)) return `url("${trimmed}")`;
    const data = trimmed.match(/^data:image\/(png|jpeg|webp);base64,([a-z0-9+/=\s]+)$/i);
    if (!data) return null;
    return `url("data:image/${data[1].toLowerCase()};base64,${data[2].replace(/\s/g, "")}")`;
}

export function stageImageBackground(value: string): string | null {
    return stageImageValue(value);
}

export function readStageImage(file: File): Promise<string> {
    const extension = file.name.toLowerCase();
    const allowedType = file.type === "image/png" || file.type === "image/jpeg" || file.type === "image/webp";
    const allowedName = /\.(png|jpe?g|webp)$/.test(extension);
    if (!allowedType && !(file.type === "" && allowedName)) return Promise.reject(new Error(errorCode("image-type")));
    if (file.size > maxStageImageBytes) return Promise.reject(new Error(errorCode("image-too-big")));
    return new Promise((resolve, reject) => {
        const reader = new FileReader();
        reader.onload = () => {
            const result = typeof reader.result === "string" ? reader.result : "";
            if (!stageImageValue(result)) {
                reject(new Error(errorCode("image-read")));
                return;
            }
            resolve(result);
        };
        reader.onerror = () => reject(new Error(errorCode("image-read")));
        reader.readAsDataURL(file);
    });
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

function isLight(hex: string): boolean {
    const value = parseInt(hex.slice(1), 16);
    const red = (value >> 16) & 255;
    const green = (value >> 8) & 255;
    const blue = value & 255;
    return (0.2126 * red + 0.7152 * green + 0.0722 * blue) / 255 > 0.62;
}

export function applyAppearance(appearance: Appearance): void {
    const root = document.documentElement;
    const normalized = normalizeAppearance(appearance);
    const custom = normalized.theme === "custom";
    root.dataset.theme = normalized.theme;
    if (custom) {
        root.style.setProperty("--font-family", fontStack(normalized.fontFamily));
        root.style.setProperty("--font-size", `${normalized.fontSize}px`);
        root.dataset.stageBackdrop = normalized.stageBackdrop;
    } else {
        root.style.removeProperty("--font-family");
        root.style.removeProperty("--font-size");
        root.dataset.stageBackdrop = "mountains";
    }

    for (const token of paletteTokens) {
        const variable = paletteVariables[token];
        if (custom) root.style.setProperty(variable, normalized.custom[token]);
        else root.style.removeProperty(variable);
    }
    if (custom) root.style.colorScheme = isLight(normalized.custom.window) ? "light" : "dark";
    else root.style.removeProperty("color-scheme");

    const image = custom && normalized.stageBackdrop === "image" ? stageImageBackground(normalized.stageImage) : null;
    if (image) root.style.setProperty("--stage-image", image);
    else root.style.removeProperty("--stage-image");

    const computed = getComputedStyle(root);
    root.style.setProperty("--field-chevron", chevron(computed.getPropertyValue("--muted")));
    root.style.setProperty("--field-chevron-active", chevron(computed.getPropertyValue("--text")));

    const palette = custom ? normalized.custom : themePalettes[normalized.theme as BuiltinTheme];
    void desktopAPI.setPopupLook({
        card: palette.card,
        line: palette.line,
        text: palette.text,
        muted: palette.muted,
    }).catch(() => undefined);
}
