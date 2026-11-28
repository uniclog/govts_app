export const fontFamilies = [
  "default", "Noto Sans", "Golos Text", "Manrope", "Nunito", "Rubik", "Montserrat", "Noto Serif", "Literata",
] as const;

export type FontFamily = (typeof fontFamilies)[number];

export function fontStack(family: FontFamily): string {
  return family === "default" ? "Arial, sans-serif" : `"${family}", "Noto Sans", sans-serif`;
}
