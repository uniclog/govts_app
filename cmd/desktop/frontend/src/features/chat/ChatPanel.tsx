import {useEffect, useLayoutEffect, useReducer, useRef, useState} from "react";
import {createPortal} from "react-dom";
import {Icon} from "../../components/Icon";
import {avatarStyle} from "../../components/avatar";
import {errorText} from "../../locales";
import {useLocale} from "../../locales/Locale";
import {chatLinkParts, openConfirmedLink} from "./chatLinks";
import {ChatStore, type ChatTarget} from "./chatStore";
import {desktopAPI, type FileCapabilities} from "../../api";
import {AttachmentCard, stageFiles, TransferQueue, useTransfers} from "./files";

function ignoreMiddleClick(event: {button: number; preventDefault: () => void}) {
    if (event.button === 1) event.preventDefault();
}

function ChatMessageText({text}: {text: string}) {
    return chatLinkParts(text).map((part, index) => part.href
        ? <a key={index} href={part.href}
            onPointerDown={ignoreMiddleClick}
            onMouseDown={ignoreMiddleClick}
            onAuxClick={ignoreMiddleClick}
            onClick={(event) => { event.preventDefault(); if (event.button !== 0) return; openConfirmedLink(part.href!); }}>{part.text}</a>
        : <span key={index}>{part.text}</span>);
}

const resizeComposer = (input: HTMLTextAreaElement) => {
    input.style.height = "0px";
    const contentHeight = input.scrollHeight + input.offsetHeight - input.clientHeight;
    input.style.height = `${Math.max(32, Math.min(68, contentHeight))}px`;
    input.style.overflowY = contentHeight > 68 ? "auto" : "hidden";
};

export function ChatPanel({store, target, title, active, connected, online, localName}: {
    store: ChatStore; target: ChatTarget; title: string; active: boolean; connected: boolean; online?: boolean;     localName: string;
}) {
    const {m, htmlLang} = useLocale();
    const tasks = useTransfers(store.context, connected, active);
    const pendingFiles = new Set([...store.confirmedAttachments, ...store.conversation(target).pending.flatMap((p) => p.attachmentIds ?? [])]);
    const uploads = tasks.filter((t) => t.direction === "upload" && t.kind === (target.kind === "channel" ? 1 : 2) && t.targetId === target.id && t.state !== "done" && t.state !== "cancelled" && !pendingFiles.has(t.attachmentId));
    const ready = uploads.filter((t) => t.state === "ready");
    const [caps, setCaps] = useState<FileCapabilities>();
    const [fileError, setFileError] = useState("");
    const [menu, setMenu] = useState<{id: string; x: number; y: number}>();
    const [messageError, setMessageError] = useState("");
    const menuRef = useRef<HTMLDivElement>(null);
    const menuOrigin = useRef<HTMLElement | null>(null);
    useEffect(() => {setMenu(undefined); setMessageError("");}, [target.kind, target.id, active, connected]);
    useEffect(() => {
        if (!menu) return;
        (menuRef.current?.querySelector<HTMLButtonElement>("button:not(:disabled)") ?? menuRef.current)?.focus({preventScroll: true});
        const outside = (event: PointerEvent) => {if (!menuRef.current?.contains(event.target as Node) && !menuOrigin.current?.contains(event.target as Node)) setMenu(undefined);};
        const close = () => setMenu(undefined);
        window.addEventListener("pointerdown", outside);
        window.addEventListener("resize", close);
        window.addEventListener("blur", close);
        document.addEventListener("scroll", close, true);
        return () => {window.removeEventListener("pointerdown", outside); window.removeEventListener("resize", close); window.removeEventListener("blur", close); document.removeEventListener("scroll", close, true);};
    }, [menu]);
    const [staging, setStaging] = useState(false);
    useEffect(() => {let disposed = false; setCaps(undefined); if (connected && active) void desktopAPI.fileCapabilities(store.context).then((c) => {if (!disposed) setCaps(c);}).catch(() => undefined); return () => {disposed = true;};}, [store.context, connected, active]);
    const addFiles = async (files: File[]) => {
        if (!caps?.uploadsEnabled || staging) return;
        if (uploads.length + files.length > caps.maxAttachments) {setFileError(m.files.limit); return;}
        setStaging(true); setFileError("");
        try {await stageFiles(store.context, target, files, caps.maxFileBytes);} catch(e) {setFileError(String(e));} finally {setStaging(false);}
    };
    const messageDate = (at: number) => new Date(at).toLocaleString(htmlLang, {day: "2-digit", month: "2-digit", year: "numeric", hour: "2-digit", minute: "2-digit"});
    const [, refresh] = useReducer((n: number) => n + 1, 0);
    const history = useRef<HTMLDivElement>(null);
    const historyContent = useRef<HTMLDivElement>(null);
    const composer = useRef<HTMLTextAreaElement | null>(null);
    const atBottom = useRef(true);
    const previousHeight = useRef<number | null>(null);
    const previousAnchor = useRef<{id: string; top: number} | null>(null);
    const [newMessages, setNewMessages] = useState(false);
    const [visible, setVisible] = useState(document.visibilityState === "visible");
    useEffect(() => store.subscribe(refresh), [store]);
    useEffect(() => {
        const changed = () => setVisible(document.visibilityState === "visible");
        document.addEventListener("visibilitychange", changed);
        return () => document.removeEventListener("visibilitychange", changed);
    }, []);
    const c = store.conversation(target);
    const menuMessage = menu ? c.messages.find((message) => message.id === menu.id && !message.deleted) : undefined;
    useEffect(() => {if (menu && !menuMessage) setMenu(undefined);}, [menu, menuMessage]);
    const copyText = async (text: string) => {
        setMenu(undefined); setMessageError(""); menuOrigin.current?.focus({preventScroll: true});
        try {await navigator.clipboard.writeText(text);} catch {setMessageError(m.chat.copyFailed);}
    };
    useLayoutEffect(() => {
        if (!active || !composer.current) return;
        const input = composer.current;
        const resize = () => {
            resizeComposer(input);
            if (atBottom.current && history.current) history.current.scrollTop = history.current.scrollHeight;
        };
        resize();
        let width = input.clientWidth;
        const observer = new ResizeObserver(() => {
            if (input.clientWidth === width) return;
            width = input.clientWidth;
            resize();
        });
        observer.observe(input);
        return () => observer.disconnect();
    }, [c.draft, active]);
    const lastID = c.messages[c.messages.length - 1]?.id;
    useLayoutEffect(() => {
        const node = history.current;
        const content = historyContent.current;
        if (!active || !node || !content) return;
        // Attachment metadata and decoded previews can resize existing messages
        // without changing their IDs. Keep following the end until the user scrolls up.
        const observer = new ResizeObserver(() => {
            if (atBottom.current && previousHeight.current === null) node.scrollTop = node.scrollHeight;
        });
        observer.observe(content);
        observer.observe(node);
        return () => observer.disconnect();
    }, [active]);
    useEffect(() => {
        if (!active || !connected || !visible) return;
        return store.watch(target);
    }, [store, target.kind, target.id, active, connected, visible]);
    useLayoutEffect(() => {
        if (!active || !history.current) return;
        if (previousHeight.current !== null && c.loading) return;
        if (previousHeight.current !== null) {
            const anchor = previousAnchor.current;
            const node = anchor ? history.current.querySelector<HTMLElement>(`[data-message-id="${anchor.id}"]`) : null;
            history.current.scrollTop += node && anchor ? node.getBoundingClientRect().top - anchor.top : history.current.scrollHeight - previousHeight.current;
            previousHeight.current = null;
            previousAnchor.current = null;
        } else if (atBottom.current) history.current.scrollTop = history.current.scrollHeight;
        else setNewMessages(true);
    }, [lastID, c.messages[0]?.id, c.messages.length, c.pending.length, active]);
    useEffect(() => {
        if (active && connected && visible && atBottom.current) void store.markRead(target);
    }, [lastID, c.loading, active, connected, visible, store, target.id, target.kind]);
    const bytes = new TextEncoder().encode(c.draft).length;
    const valid = (c.draft.trim().length > 0 || ready.length > 0) && bytes <= 1000 && !c.draft.includes("\0") && ready.length === uploads.length && ready.length <= 4 && !staging;
    const send = () => { if (connected && valid) void store.send(target, c.draft, undefined, ready.map((t) => t.attachmentId)); };
    const older = async () => {
        if (c.loading) return;
        const first = c.messages[0]?.id, last = c.messages[c.messages.length - 1]?.id;
        previousHeight.current = history.current?.scrollHeight ?? null;
        const top = history.current?.getBoundingClientRect().top ?? 0;
        const anchor = [...(history.current?.querySelectorAll<HTMLElement>("[data-message-id]") ?? [])].find((node) => node.getBoundingClientRect().bottom > top);
        previousAnchor.current = anchor ? {id: anchor.dataset.messageId!, top: anchor.getBoundingClientRect().top} : null;
        await store.sync(target, true);
        // A failed/no-op load must not affect the next incoming message's scroll.
        if (first === c.messages[0]?.id && last === c.messages[c.messages.length - 1]?.id) {
            previousHeight.current = null; previousAnchor.current = null;
        }
        refresh();
    };
    const canDropFiles = connected && !!caps?.uploadsEnabled;
    return <section className="chat-panel" aria-label={title}
        data-file-drop-target={canDropFiles ? "" : undefined} data-drop-hint={m.files.dropHint}
        onDragLeave={(e) => {if (!e.currentTarget.contains(e.relatedTarget as Node | null)) e.currentTarget.classList.remove("file-drop-target-active");}}
        onDragOver={(e) => {if (canDropFiles && e.dataTransfer.types.includes("Files")) e.preventDefault();}}
        onDrop={(e) => {e.preventDefault(); if (canDropFiles) void addFiles([...e.dataTransfer.files]);}}
        onPaste={(e) => {const files = [...e.clipboardData.files]; if(files.length && caps && connected) {e.preventDefault(); void addFiles(files);}}}>
        {target.kind === "direct" && <header className="chat-heading"><strong>{title}</strong><span>{online ? m.chat.online : m.chat.offline}</span></header>}
        {c.error && <div className="chat-error" role="alert">{errorText(c.error, m)}<button onClick={() => void store.sync(target)} disabled={!connected || c.loading}>{m.chat.retry}</button></div>}
        {messageError && <div className="chat-error" role="alert">{messageError}</div>}
        <div ref={history} className="chat-history" aria-label={m.chat.history} onScroll={() => {
            const node = history.current;
            if (!node) return;
            atBottom.current = node.scrollHeight - node.scrollTop - node.clientHeight < 40;
            if (atBottom.current) {
                setNewMessages(false);
                if (active && connected && visible) void store.markRead(target);
            }
            if (active && connected && node.scrollTop < 20 && node.scrollHeight > node.clientHeight && c.hasOlder && !c.loading) void older();
        }}>
            <div ref={historyContent} className="chat-history-content">
            {c.hasOlder && <button className="chat-older" disabled={!connected || c.loading} onClick={() => void older()}>{m.chat.older}</button>}
            {!c.loaded && <div className="chat-placeholder"><Icon name="chat"/><p>{c.loading ? m.chat.loading : connected ? m.chat.openToLoad : m.chat.waiting}</p></div>}
            {c.loaded && !c.messages.length && !c.pending.length && <div className="chat-placeholder">{m.chat.empty}</div>}
            {c.messages.map((message) => <article key={message.id} data-message-id={message.id} className={`chat-message ${message.senderId === store.userId ? "own" : ""}${message.deleted ? " is-deleted" : ""}`}>
                <span className="chat-author-avatar" style={avatarStyle(message.senderName)} aria-hidden="true">{message.senderName.slice(0, 1).toUpperCase()}</span>
                <div className="chat-message-body">
                    <div className="chat-message-meta"><strong className="chat-message-author">{message.senderName}</strong><time title={messageDate(message.sentAtMs)} dateTime={new Date(message.sentAtMs).toISOString()}>{new Date(message.sentAtMs).toLocaleTimeString(htmlLang, {hour: "2-digit", minute: "2-digit"})}</time>{message.deleted && <span className="chat-message-deleted">{m.chat.deleted}</span>}</div>
                    {!message.deleted && message.text && <div className="chat-message-text"><ChatMessageText text={message.text}/></div>}
                    {active && !message.deleted && message.attachmentIds?.map((id) => <AttachmentCard key={id} context={store.context} id={id}/>)}
                </div>
                {!message.deleted && <button className="chat-message-menu-button" type="button" title={m.chat.actions} aria-label={m.chat.actions} aria-haspopup="menu" aria-expanded={menu?.id === message.id} aria-controls={menu?.id === message.id ? "chat-message-actions" : undefined} onClick={(event) => {
                    menuOrigin.current = event.currentTarget;
                    const bounds = event.currentTarget.getBoundingClientRect();
                    setMenu((current) => current?.id === message.id ? undefined : {id: message.id, x: Math.max(8, Math.min(bounds.right - 228, window.innerWidth - 236)), y: Math.max(8, bounds.bottom + 96 > window.innerHeight ? bounds.top - 88 : bounds.bottom + 4)});
                }}>⋯</button>}
            </article>)}
            {c.pending.map((p) => <article key={p.clientId} className="chat-message own pending">
                <time dateTime={new Date(p.sentAtMS).toISOString()}>{messageDate(p.sentAtMS)}</time>{" "}<strong>{localName}</strong>{": "}<ChatMessageText text={p.text}/>{" "}
                {p.attachmentIds?.length ? <span>📎 {p.attachmentIds.length}</span> : null}
                {p.state === "sending" ? <span className="chat-send-status" role="status" aria-label={m.chat.sending} title={m.chat.sending}>…</span> : <button disabled={!connected} title={errorText(p.error ?? "", m)} aria-label={m.chat.retrySend(errorText(p.error ?? "chat-send-failed", m))} onClick={() => void store.send(target, p.text, p)}>{m.chat.retry}</button>}
            </article>)}
            </div>
        </div>
        {(newMessages || c.olderWindow || c.newMessages) && <button className="chat-new" disabled={!connected || c.loading} onClick={async () => {
            atBottom.current = true;
            if (c.olderWindow) await store.latest(target);
            if (history.current) history.current.scrollTop = history.current.scrollHeight;
            atBottom.current = true; setNewMessages(false);
            if (active && connected && visible) void store.markRead(target);
        }}>{m.chat.jump}</button>}
        <div className="chat-compose-area">
        {active && <TransferQueue context={store.context} tasks={tasks.filter((task) => task.direction !== "upload" || !pendingFiles.has(task.attachmentId))} report={setFileError}/>}
        {staging && <small className="chat-file-staging" role="status">{m.files.preparing}</small>}
        {fileError && <div className="chat-error" role="alert">{fileError}</div>}
        <div className={`chat-composer${caps?.uploadsEnabled ? " has-attachments" : ""}`}>
            {caps?.uploadsEnabled && <button className="chat-attach-button" type="button" aria-label={m.files.attach} title={`${m.files.attach}\n${m.files.originalMetadata}`} disabled={!connected || staging || uploads.length >= caps.maxAttachments} onClick={() => {setFileError(""); void desktopAPI.filePick(store.context, target.kind, target.id).catch((e) => setFileError(String(e)));}}><Icon name="attachment"/></button>}
            <div className={`chat-input${bytes >= 800 ? " has-counter" : ""}`}>
            <textarea ref={composer} rows={1} maxLength={1000} aria-label={m.chat.message} title={m.chat.composerHint} value={c.draft}
                onChange={(event) => store.setDraft(target, event.target.value)}
                onKeyDown={(event) => { if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing && event.keyCode !== 229) { event.preventDefault(); send(); } }}/>
            {/* Native placeholders lose their ellipsis while the field holds focus. */}
            {!c.draft && <span className="chat-input-placeholder" aria-hidden="true">{connected ? m.chat.placeholder : m.chat.draftWaiting}</span>}
            {bytes >= 800 && <small className={bytes > 1000 ? "is-over-limit" : ""} title={m.chat.bytes}>{bytes}/1000</small>}
            </div>
            <button className="chat-send-button" disabled={!connected || !valid} type="button" title={m.chat.send} aria-label={m.chat.send} onClick={send}><Icon name="send"/></button>
        </div>
        </div>
        {menu && menuMessage && active && createPortal(<div ref={menuRef} id="chat-message-actions" tabIndex={-1} className="chat-context-menu" role="menu" aria-label={m.chat.actions} style={{left: menu.x, top: menu.y}} onKeyDown={(event) => {
            if (event.key === "Escape" || event.key === "Tab") {event.preventDefault(); setMenu(undefined); menuOrigin.current?.focus();}
            if (event.key === "ArrowDown" || event.key === "ArrowUp") {event.preventDefault(); const buttons = [...(menuRef.current?.querySelectorAll<HTMLButtonElement>("button:not(:disabled)") ?? [])]; const current = buttons.indexOf(document.activeElement as HTMLButtonElement); buttons[(current + (event.key === "ArrowDown" ? 1 : buttons.length - 1)) % buttons.length]?.focus();}
        }}>
            <button type="button" role="menuitem" disabled={!menuMessage.text.trim()} onClick={() => void copyText(menuMessage.text)}>{m.chat.copyText}</button>
            {menuMessage.senderId === store.userId && <button className="is-danger" type="button" role="menuitem" disabled={!connected} onClick={() => {void store.remove(target, menu.id); setMenu(undefined); menuOrigin.current?.focus();}}>{m.chat.deleteMessage}</button>}
        </div>, document.body)}
    </section>;
}
