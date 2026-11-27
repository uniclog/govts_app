import {useEffect, useRef, useState} from "react";
import {Icon} from "../../components/Icon";
import {useLocale} from "../../locales/Locale";
import type {ChatMessageDTO} from "../../api";
import {safeChatText} from "./chatLinks";
import type {ChatStore, ChatTarget} from "./chatStore";

const copyable = (message?: ChatMessageDTO) => !!message && !!message.text.trim();

// The bar shows the latest pin; the list below it holds every pin, the latest first.
export function PinnedMessages({store, target, pinned, connected, onReveal, onCopy}: {
    store: ChatStore; target: ChatTarget; pinned: string[]; connected: boolean;
    onReveal: (id: string) => void; onCopy: (text: string) => Promise<boolean>;
}) {
    const {m, htmlLang} = useLocale();
    const [open, setOpen] = useState(false);
    const [copied, setCopied] = useState<string>();
    const root = useRef<HTMLDivElement>(null);
    const toggle = useRef<HTMLButtonElement>(null);
    const pinList = pinned.join();
    useEffect(() => {setOpen(false); setCopied(undefined);}, [target.kind, target.id]);
    useEffect(() => {if (!pinned.length) setOpen(false);}, [pinned.length]);
    // The bar needs one text; the open list needs all of them.
    useEffect(() => {if (connected && pinned[0] && !store.pinnedMessage(target, pinned[0])) void store.loadPinTexts(target, 1);}, [connected, pinned[0], store, target.kind, target.id]);
    useEffect(() => {if (open && connected) void store.loadPinTexts(target);}, [open, connected, pinList, store, target.kind, target.id]);
    useEffect(() => {
        if (!copied) return;
        const timer = window.setTimeout(() => setCopied(undefined), 1500);
        return () => window.clearTimeout(timer);
    }, [copied]);
    useEffect(() => {
        if (!open) return;
        const outside = (event: PointerEvent) => {if (!root.current?.contains(event.target as Node)) setOpen(false);};
        window.addEventListener("pointerdown", outside);
        return () => window.removeEventListener("pointerdown", outside);
    }, [open]);
    if (!pinned.length) return null;
    const preview = (message?: ChatMessageDTO) => {
        if (!message) return m.chat.loading;
        const text = safeChatText(message.text).replace(/\s+/g, " ").trim();
        return text || `📎 ${m.chat.pinAttachment}`;
    };
    const copy = (id: string) => {
        const message = store.pinnedMessage(target, id);
        if (copyable(message)) void onCopy(safeChatText(message!.text)).then((done) => {if (done) setCopied(id);});
    };
    const reveal = (id: string) => {setOpen(false); onReveal(id);};
    const copyButton = (id: string) => copyable(store.pinnedMessage(target, id)) && <button className={`chat-pin-icon-button${copied === id ? " is-copied" : ""}`} type="button" title={copied === id ? m.chat.copied : m.chat.copyText} aria-label={copied === id ? m.chat.copied : m.chat.copyText} onClick={() => copy(id)}><Icon name={copied === id ? "check" : "copy"}/></button>;
    const latest = pinned[0], message = store.pinnedMessage(target, latest);
    return <div ref={root} className={`chat-pins${open ? " is-open" : ""}`} onKeyDown={(event) => {
        if (event.key === "Escape" && open) {event.stopPropagation(); setOpen(false); toggle.current?.focus();}
    }}>
        <div className="chat-pin-bar">
            <button className="chat-pin-current" type="button" title={m.chat.pinJump} disabled={!connected} onClick={() => reveal(latest)}>
                <Icon name="pin"/>
                <span className="chat-pin-summary">
                    <strong>{pinned.length > 1 ? m.chat.pinnedCount(pinned.length) : m.chat.pinned}</strong>
                    <span className="chat-pin-preview">{message && <b>{message.senderName}: </b>}{preview(message)}</span>
                </span>
            </button>
            {copyButton(latest)}
            <button ref={toggle} className="chat-pin-icon-button chat-pin-toggle" type="button" title={open ? m.chat.hidePins : m.chat.showPins} aria-label={open ? m.chat.hidePins : m.chat.showPins} aria-expanded={open} aria-controls={open ? "chat-pin-list" : undefined} onClick={() => setOpen((value) => !value)}><Icon name="chevronDown"/></button>
        </div>
        {open && <ul id="chat-pin-list" className="chat-pin-list" aria-label={m.chat.pinnedList}>
            {pinned.map((id) => {
                const entry = store.pinnedMessage(target, id);
                return <li key={id} className="chat-pin-item">
                    <button className="chat-pin-item-main" type="button" title={m.chat.pinJump} disabled={!connected} onClick={() => reveal(id)}>
                        <span className="chat-pin-item-meta">{entry && <><strong>{entry.senderName}</strong><time dateTime={new Date(entry.sentAtMs).toISOString()}>{new Date(entry.sentAtMs).toLocaleString(htmlLang, {day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit"})}</time></>}</span>
                        <span className="chat-pin-item-text">{preview(entry)}</span>
                    </button>
                    {copyButton(id)}
                    <button className="chat-pin-icon-button" type="button" title={m.chat.unpin} aria-label={m.chat.unpin} disabled={!connected} onClick={() => void store.pin(target, id)}><Icon name="close"/></button>
                </li>;
            })}
        </ul>}
    </div>;
}
