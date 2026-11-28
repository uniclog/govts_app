import type {CSSProperties} from "react";

const colours = ["#f3ce78", "#f2a4bd", "#94d8f6", "#c8aff8", "#ffb58b", "#a5edc5"];

export function avatarStyle(name: string): CSSProperties {
    const initial = name.trim().toUpperCase().codePointAt(0) ?? 0;
    return {"--avatar-colour": colours[initial % colours.length]} as CSSProperties;
}
