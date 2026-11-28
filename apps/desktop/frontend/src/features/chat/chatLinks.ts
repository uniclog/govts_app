import {Browser} from "@wailsio/runtime";
import {confirmExternalLink} from "./LinkConfirm";

export type ChatTextPart = {text: string; href?: string};

const closers: Record<string, string> = {")": "(", "]": "[", "}": "{", "»": "«", "”": "“"};
const fileExtensions = new Set([
    "png", "jpg", "jpeg", "gif", "webp", "bmp", "svg", "ico", "pdf", "zip", "rar", "7z", "tar", "gz",
    "mp3", "mp4", "wav", "ogg", "webm", "json", "xml", "csv", "txt", "md", "js", "ts", "jsx", "tsx",
    "css", "go", "rs", "py", "exe", "dll", "dmg", "apk", "doc", "docx", "xls", "xlsx", "ppt", "pptx",
]);

// A scheme, a www host, or a dotted name. The lookbehind keeps the tail of an email address as text.
const linkPattern = /(?:https?:\/\/|www\.)[^\s<>"'`]+|(?<![\p{L}\p{N}_@./-])(?:[\p{L}\p{N}](?:[\p{L}\p{N}-]{0,62}[\p{L}\p{N}])?\.)+[\p{L}]{2,24}(?::\d{2,5})?(?:\/[^\s<>"'`]*)?/giu;

function countChar(value: string, char: string): number {
    let count = 0;
    for (const item of value) if (item === char) count++;
    return count;
}

function peelTrailing(raw: string): string {
    let end = raw.length;
    while (end > 0) {
        const char = raw[end - 1];
        const open = closers[char];
        if (open) {
            const body = raw.slice(0, end);
            if (countChar(body, char) > countChar(body, open)) {
                end--;
                continue;
            }
            break;
        }
        if (".,;:!?".includes(char)) {
            end--;
            continue;
        }
        break;
    }
    return raw.slice(0, end);
}

function linkHref(body: string): string | null {
    const explicit = /^https?:\/\//i.test(body);
    const candidate = explicit ? body : `https://${body}`;
    let url: URL;
    try {
        url = new URL(candidate);
    } catch {
        return null;
    }
    if (url.protocol !== "http:" && url.protocol !== "https:") return null;
    // https://bank.example@evil.example reads as one site and opens another.
    if (url.username || url.password) return null;
    if (!explicit) {
        const labels = url.hostname.split(".");
        if (labels.length < 2 || labels.some((label) => label.length === 0)) return null;
        if (labels.every((label) => /^\d+$/.test(label))) return null;
        const bareFile = (url.pathname === "/" || url.pathname === "") && !url.search && !url.hash;
        if (bareFile && fileExtensions.has(labels[labels.length - 1].toLowerCase())) return null;
    }
    return url.href;
}

// Direction overrides can make text and file names read as something else, and
// control characters have no visible form. Emoji joiners and tabs stay.
const unsafeText = /[\u0000-\u0008\u000B-\u001F\u007F-\u009F\u202A-\u202E\u2066-\u2069]/g;
export const safeChatText = (text: string) => text.replace(unsafeText, "");

export function chatLinkParts(text: string): ChatTextPart[] {
    const parts: ChatTextPart[] = [];
    let last = 0;
    for (const match of text.matchAll(linkPattern)) {
        const start = match.index ?? 0;
        const body = peelTrailing(match[0]);
        const href = body ? linkHref(body) : null;
        if (!href || start < last) continue;
        if (start > last) parts.push({text: text.slice(last, start)});
        parts.push({text: body, href});
        last = start + body.length;
    }
    if (last < text.length) parts.push({text: text.slice(last)});
    return parts;
}

export function openConfirmedLink(href: string): void {
    let url: URL;
    try {
        url = new URL(href);
    } catch {
        return;
    }
    if (url.protocol !== "http:" && url.protocol !== "https:") return;
    void confirmExternalLink(url.href).then((accepted) => {
        if (!accepted) return;
        void Browser.OpenURL(url.href).catch(() => {
            window.open(url.href, "_blank", "noopener,noreferrer");
        });
    });
}
