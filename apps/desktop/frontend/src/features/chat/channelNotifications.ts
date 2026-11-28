import {useCallback, useSyncExternalStore} from "react";
import type {ChatMessageDTO} from "../../api";
import {safeChatText} from "./chatLinks";

// Channels whose chat raises no sound and no popup, per server and account:
// {"<server identity>|<user id>": ["<channel id>", ...]}. The setting stays on
// this computer.
const storageKey = "sonoryx.mutedChannels";
// Per server; the oldest choice gives way first.
const maxMutedChannels = 512;
const listeners = new Set<() => void>();
let cache: Record<string, string[]> | undefined;

function read(): Record<string, string[]> {
    if (cache) return cache;
    cache = {};
    try {
        const saved = JSON.parse(localStorage.getItem(storageKey) ?? "{}");
        if (saved && typeof saved === "object") for (const [server, ids] of Object.entries(saved)) {
            if (Array.isArray(ids)) cache[server] = ids.filter((id): id is string => typeof id === "string").slice(-maxMutedChannels);
        }
    } catch { /* A corrupt value only loses the muted list. */ }
    return cache;
}

// The chat context is "endpoint|server identity|client identity|user id"; the
// endpoint is left out so a new address of the same server keeps the setting.
export function channelNotificationScope(chatContext: string): string {
    const parts = chatContext.split("|");
    return parts.length >= 4 && parts[1] ? `${parts[1]}|${parts[parts.length - 1]}` : chatContext;
}

export function channelMuted(scope: string, channelId: string): boolean {
    return read()[scope]?.includes(channelId) ?? false;
}

export function setChannelMuted(scope: string, channelId: string, muted: boolean): void {
    const all = {...read()};
    const ids = (all[scope] ?? []).filter((id) => id !== channelId);
    if (muted) ids.push(channelId);
    if (ids.length) all[scope] = ids.slice(-maxMutedChannels); else delete all[scope];
    cache = all;
    try { localStorage.setItem(storageKey, JSON.stringify(all)); } catch { /* The choice holds for this session. */ }
    for (const listener of listeners) listener();
}

// One line for the message popup: the text without line breaks, cut to 120
// characters, or the attachment label for a message with files only.
export function messagePreview(message: Pick<ChatMessageDTO, "text">, attachment: string): string {
    const text = safeChatText(message.text).replace(/\s+/g, " ").trim();
    if (!text) return `📎 ${attachment}`;
    const characters = [...text];
    return characters.length > 120 ? `${characters.slice(0, 119).join("")}…` : text;
}

const subscribe = (listener: () => void) => { listeners.add(listener); return () => { listeners.delete(listener); }; };

// The muted channel IDs of one server, as a comma-joined string so the
// snapshot compares by value.
export function useMutedChannels(scope: string): Set<string> {
    const joined = useSyncExternalStore(subscribe, useCallback(() => (read()[scope] ?? []).join(","), [scope]));
    return new Set(joined ? joined.split(",") : []);
}
