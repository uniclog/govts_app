import { fontFamilies, fontStack, type FontFamily } from "../../../../../shared-ui/font-options";

export { fontFamilies, fontStack };

export const themes = [
  ["dark", "Графит"], ["light", "Светлая"], ["navy", "Синяя"],
  ["beige", "Бежевая"], ["brown", "Коричневая"], ["purple", "Фиолетовая"],
] as const;
export const fontSizes = [14, 15, 16, 17, 18] as const;

const storageKey = "sonoryx.web.appearance";
type Appearance = { theme: string; fontSize: number; fontFamily: FontFamily };
const defaults: Appearance = { theme: "dark", fontSize: 16, fontFamily: "default" };

export function readAppearance(): Appearance {
  try {
    const value = JSON.parse(localStorage.getItem(storageKey) ?? "{}");
    return {
      theme: themes.some(([theme]) => theme === value?.theme) ? value.theme : defaults.theme,
      fontSize: fontSizes.some((size) => size === value?.fontSize) ? value.fontSize : defaults.fontSize,
      fontFamily: fontFamilies.find((font) => font === value?.fontFamily) ?? defaults.fontFamily,
    };
  } catch {
    return defaults;
  }
}

export function applyAppearance(value: Appearance) {
  const root = document.documentElement;
  root.dataset.theme = value.theme;
  root.dataset.webSize = String(value.fontSize);
  if (value.fontFamily === "default") root.style.removeProperty("--web-font-family");
  else root.style.setProperty("--web-font-family", fontStack(value.fontFamily));
  try { localStorage.setItem(storageKey, JSON.stringify(value)); } catch { /* Settings apply for this page. */ }
}
