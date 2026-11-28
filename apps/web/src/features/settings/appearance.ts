export const themes = [
  ["dark", "Графит"], ["light", "Светлая"], ["navy", "Синяя"],
  ["beige", "Бежевая"], ["brown", "Коричневая"], ["purple", "Фиолетовая"],
] as const;
export const fontSizes = [14, 15, 16, 17, 18] as const;

const storageKey = "sonoryx.web.appearance";
type Appearance = { theme: string; fontSize: number };

export function readAppearance(): Appearance {
  try {
    const value = JSON.parse(localStorage.getItem(storageKey) ?? "{}");
    return {
      theme: themes.some(([theme]) => theme === value?.theme) ? value.theme : "dark",
      fontSize: fontSizes.some((size) => size === value?.fontSize) ? value.fontSize : 16,
    };
  } catch {
    return { theme: "dark", fontSize: 16 };
  }
}

export function applyAppearance(value: Appearance) {
  document.documentElement.dataset.theme = value.theme;
  document.documentElement.dataset.webSize = String(value.fontSize);
  try { localStorage.setItem(storageKey, JSON.stringify(value)); } catch { /* Settings apply for this page. */ }
}
