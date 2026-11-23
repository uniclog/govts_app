import {desktopAPI, type ChatDialogDTO, type ChatMessageDTO, type ChatPageDTO, type ChatRequestDTO} from "../../api";
import {errorCode} from "../../locales";
import {REACTION_EMOJI} from "./emoji";

export type ChatTarget = {kind: "channel" | "direct"; id: string};
export type PendingMessage = {clientId: string; text: string; attachmentIds?: string[]; sentAtMS: number; state: "sending" | "error"; error?: string};
export type Conversation = {
    messages: ChatMessageDTO[]; pending: PendingMessage[]; draft: string;
    loaded: boolean; loading: boolean; error: string; hasOlder: boolean;
    cursor: string; changeCursor: string; readId: string; unread: number; olderWindow: boolean; newMessages: boolean;
    // Pinned message IDs, the latest pin first.
    pinned: string[];
};
const keyOf = (t: ChatTarget) => `${t.kind}:${t.id}`;
const compare = (a: string, b: string) => BigInt(a) < BigInt(b) ? -1 : BigInt(a) > BigInt(b) ? 1 : 0;
const maxID = (a: string, b: string) => compare(a, b) > 0 ? a : b;
const encoder = new TextEncoder();
const limitDraft = (text: string): string => {
    if (encoder.encode(text).length <= 1000) return text;
    let result = "", bytes = 0;
    for (const character of text) {
        const size = encoder.encode(character).length;
        if (bytes + size > 1000) break;
        result += character; bytes += size;
    }
    return result;
};
// The server sends reactions in table order; local changes keep to it too.
const withReaction = (message: ChatMessageDTO, emoji: string, mine: boolean): ChatMessageDTO => {
    const others = (message.reactions ?? []).filter((r) => r.emoji !== emoji);
    const current = message.reactions?.find((r) => r.emoji === emoji);
    const count = (current?.count ?? 0) + (mine === !!current?.mine ? 0 : mine ? 1 : -1);
    const reactions = count > 0 ? [...others, {emoji, count, mine}] : others;
    return {...message, reactions: reactions.sort((a, b) => REACTION_EMOJI.indexOf(a.emoji) - REACTION_EMOJI.indexOf(b.emoji))};
};
const visibleState = (c: Conversation) => [c.messages.length, c.messages[c.messages.length - 1]?.id, c.messages[c.messages.length - 1]?.revision, c.pending.length, c.error, c.hasOlder, c.unread, c.readId, c.newMessages].join("|");
export const chatError = (e: unknown) => (e instanceof Error ? e.message : String(e)).replace(/^Error:\s*/, "");

export class ChatStore {
    readonly context: string;
    readonly userId: string;
    dialogs: ChatDialogDTO[] = [];
    dialogError = "";
    dialogHasMore = false;
    private dialogCursor = "0";
    private listeners = new Set<() => void>();
    private contentListeners = new Set<() => void>();
    private conversations = new Map<string, Conversation>();
    private busy = new Set<string>();
    private reading = new Set<string>();
    private reacting = new Map<string, {conversation: Conversation; id: string; emoji: string; add: boolean}>();
    // Conversations whose pin list must be read again; a change to any of their messages may be a pin.
    private pinsStale = new Set<string>();
    private pinsLoading = new Set<string>();
    // Message IDs are unique on a server, so pinned texts share one cache. Messages are never
    // edited, and a deleted message leaves the pin list, so a cached entry stays valid.
    private pinTexts = new Map<string, ChatMessageDTO>();
    // A server that predates pins rejects the request; the feature then stays hidden.
    pinsSupported = true;
    private reactionTail: Promise<unknown> = Promise.resolve();
    private lastReactionAt = 0;
    private listing = false;
    private disposed = false;
    private connected = false;
    private epoch = 0;
    private lifecycle = new AbortController();
    private requestTail: Promise<unknown> = Promise.resolve();
    private lastRequestAt = 0;
    private pausedUntil = 0;
    private nextSyncAt = 0;
    private scheduled = false;
    private watched = new Map<string, {target: ChatTarget; count: number}>();
    private readQueue = new Map<string, {target: ChatTarget; id: string}>();
    private dismissedDialogs = new Map<string, string>();
    private timer: number;
    private changeTimer = 0;
    private lastChangeSyncAt = 0;
    private restoredDrafts: Record<string, string> = {};
    private onMessageReceived: () => void = () => undefined;
    readonly confirmedAttachments = new Set<string>();
    private attachmentsToFinish = new Set<string>();
    private finishingAttachments = false;
    private finishRetryAt = 0;

    private confirmAttachments(ids: string[]) {
        for (const id of ids) if (!this.confirmedAttachments.has(id)) {this.confirmedAttachments.add(id); this.attachmentsToFinish.add(id);}
        while (this.confirmedAttachments.size > 4096) {
            const oldest = [...this.confirmedAttachments].find((id) => !this.attachmentsToFinish.has(id));
            if (!oldest) break;
            this.confirmedAttachments.delete(oldest);
        }
        void this.finishAttachments();
    }
    private async finishAttachments() {
        if (this.disposed || !this.connected || this.finishingAttachments || Date.now() < this.finishRetryAt || !this.attachmentsToFinish.size) return;
        this.finishingAttachments = true;
        const ids = [...this.attachmentsToFinish].slice(0, 64);
        try {
            const queued = await desktopAPI.fileQueue(this.context);
            const ready = new Set((queued ?? []).filter((task) => task.direction === "upload" && task.state === "ready").map((task) => task.attachmentId));
            const existing = ids.filter((id) => ready.has(id));
            if (existing.length) await desktopAPI.fileAttached(this.context, existing);
            for (const id of ids) this.attachmentsToFinish.delete(id);
            this.finishRetryAt = 0;
        } catch { this.finishRetryAt = Date.now() + 3000; }
        finally {this.finishingAttachments = false;}
    }

    constructor(context: string, userId: string) {
        this.context = context; this.userId = userId;
        try {
            const saved = JSON.parse(localStorage.getItem(`sonoryx.updateDrafts:${context}`) ?? "{}");
            if (saved && typeof saved === "object") for (const [key,text] of Object.entries(saved).slice(0,32)) {
                if (typeof text === "string") this.restoredDrafts[key] = limitDraft(text);
            }
            localStorage.removeItem(`sonoryx.updateDrafts:${context}`);
        } catch { /* A corrupt backup does not block chat startup. */ }
        this.timer = window.setInterval(() => void this.reconcile(), 1000);
    }
    setMessageReceivedHandler(handler: () => void) { this.onMessageReceived = handler; }
    dispose() { this.disposed = true; this.lifecycle.abort(); window.clearInterval(this.timer); window.clearTimeout(this.changeTimer); this.listeners.clear(); this.contentListeners.clear(); }
    prepareUpdate() {
        if ([...this.conversations.values()].some((c) => c.pending.length > 0)) throw new Error(errorCode("chat-pending-refresh"));
        const drafts: Record<string,string> = {...this.restoredDrafts};
        for (const [key,c] of this.conversations) { if (c.draft) drafts[key]=c.draft; else delete drafts[key]; }
        localStorage.setItem(`sonoryx.updateDrafts:${this.context}`, JSON.stringify(drafts));
    }
    setConnected(connected: boolean) {
        if (connected === this.connected) return;
        this.connected = connected; this.epoch++; this.lifecycle.abort(); this.lifecycle = new AbortController();
        if (!connected) this.readQueue.clear();
        else {this.pinsSupported = true; for (const key of this.conversations.keys()) this.pinsStale.add(key);}
        this.nextSyncAt = 0;
    }
    invalidate() { this.nextSyncAt = Math.min(this.nextSyncAt, Date.now() + 3000); }
    // The server announced a change. Every participant reacts to it, so a busy
    // channel costs one conversation sync per two seconds, whatever the rate.
    changed() {
        if (this.changeTimer) return;
        this.changeTimer = window.setTimeout(() => {
            this.changeTimer = 0;
            this.lastChangeSyncAt = Date.now();
            void this.reconcile(true);
        }, Math.max(100, this.lastChangeSyncAt + 2000 - Date.now()));
    }
    watch(target: ChatTarget) {
        const key = keyOf(target), entry = this.watched.get(key);
        this.watched.set(key, {target, count: (entry?.count ?? 0) + 1});
        this.nextSyncAt = 0;
        return () => {
            const entry = this.watched.get(key);
            if (entry && entry.count > 1) entry.count--;
            else this.watched.delete(key);
        };
    }
    dismissDialog(userId: string) {
        const c = this.conversation({kind: "direct", id: userId});
        const latest = maxID(this.dialogs.find((d) => d.userId === userId)?.latestId ?? "0", c.messages[c.messages.length - 1]?.id ?? c.cursor);
        this.dismissedDialogs.set(userId, latest);
    }
    shouldOpenDialog(dialog: ChatDialogDTO) { return dialog.unread > 0 && compare(dialog.latestId, this.dismissedDialogs.get(dialog.userId) ?? "0") > 0; }
    // Dialogs stay on the five-second cycle; an announced change refreshes conversations only.
    private async reconcile(conversationsOnly = false) {
        void this.finishAttachments();
        if (!this.connected || this.disposed || Date.now() < this.pausedUntil) return;
        if (this.scheduled) {
            if (conversationsOnly) this.changed();
            return;
        }
        // A steady stream of changes must not starve the cycle, so a due one joins in.
        const due = Date.now() >= this.nextSyncAt;
        if (!conversationsOnly && !due) return;
        this.scheduled = true;
        const epoch = this.epoch;
        if (due) this.nextSyncAt = Date.now() + 5000;
        try {
            await this.flushReads();
            if (epoch !== this.epoch || !this.connected) return;
            await Promise.all([...this.watched.values()].map(({target}) => this.sync(target, false, true)));
            if (epoch !== this.epoch || !this.connected) return;
            await Promise.all([...this.watched.values()].map(({target}) => this.syncChanges(target)));
            if (epoch !== this.epoch || !this.connected) return;
            await Promise.all([...this.watched.values()].map(({target}) => this.syncPins(target)));
            if (!due || epoch !== this.epoch || !this.connected) return;
            await this.refreshDialogs(false, true);
        } finally { this.scheduled = false; }
    }
    subscribe = (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener); }; };
    // Typing and reactions reach only the chat panels, not every view of the store.
    subscribeContent = (listener: () => void) => { this.contentListeners.add(listener); return () => { this.contentListeners.delete(listener); }; };
    private notifyContent() { if (!this.disposed) for (const listener of this.contentListeners) listener(); }
    private notify() { if (!this.disposed) for (const listener of this.listeners) listener(); }

    conversation(target: ChatTarget): Conversation {
        const key = keyOf(target);
        let c = this.conversations.get(key);
        if (!c) {
            while (this.conversations.size >= 64) {
                const evict = [...this.conversations.entries()].find(([id, value]) => !this.busy.has(id) && !value.pending.length && !value.draft);
                if (evict) this.conversations.delete(evict[0]);
                else break; // Only pinned drafts, sends and in-flight requests remain.
            }
            c = {messages: [], pending: [], draft: "", loaded: false, loading: false, error: "", hasOlder: false, cursor: "0", changeCursor: "0", readId: "0", unread: 0, olderWindow: false, newMessages: false, pinned: []};
            this.pinsStale.add(key);
            this.conversations.set(key, c);
            c.draft = this.restoredDrafts[key] ?? "";
            delete this.restoredDrafts[key];
        }
        // Touch entries so old channel histories are evicted before visible tabs.
        this.conversations.delete(key); this.conversations.set(key, c);
        return c;
    }
    setDraft(target: ChatTarget, text: string) {
        const c = this.conversation(target);
        if (!c.draft && text && [...this.conversations.values()].filter((v) => v.draft).length >= 32) {
            c.error = errorCode("chat-draft-limit");
            this.notify();
            return;
        }
        c.draft = limitDraft(text);
        this.notifyContent();
    }

    // Reactions have their own lane, so a queue of background reads cannot delay them.
    private async request(operation: string, target?: ChatTarget, extra: Partial<ChatRequestDTO> = {}): Promise<ChatPageDTO> {
        // A pin is a user action like a reaction and shares its lane.
        const reaction = operation === "react" || operation === "pin", userAction = reaction || operation === "send";
        const epoch = this.epoch, signal = this.lifecycle.signal;
        const check = () => {
            if (this.disposed || !this.connected || epoch !== this.epoch || signal.aborted) throw new Error(errorCode("chat-session-ended"));
        };
        const run = async () => {
            check();
            // The pause answers the read limit; messages and reactions have their own budgets.
            if (!userAction && Date.now() < this.pausedUntil) throw new Error(errorCode("chat-rate-limit"));
            const delay = Math.max(0, (reaction ? this.lastReactionAt : this.lastRequestAt) + 1000 - Date.now());
            if (delay) await new Promise<void>((resolve, reject) => {
                const aborted = () => { window.clearTimeout(timer); reject(new Error(errorCode("chat-session-ended"))); };
                const timer = window.setTimeout(() => { signal.removeEventListener("abort", aborted); resolve(); }, delay);
                signal.addEventListener("abort", aborted, {once: true});
            });
            check();
            if (reaction) this.lastReactionAt = Date.now(); else this.lastRequestAt = Date.now();
            let page: ChatPageDTO;
            try {
                page = await desktopAPI.chat({context: this.context, operation, kind: target?.kind ?? "", targetId: target?.id ?? "0", cursor: "0", forward: false, clientId: "", text: "", ...extra});
                check();
            } catch (e) {
                if (/лимит запросов чата/i.test(chatError(e))) this.pausedUntil = Date.now() + 60000;
                throw e;
            }
            if (page.userId !== this.userId) throw new Error(errorCode("chat-account-changed"));
            return page;
        };
        const result = (reaction ? this.reactionTail : this.requestTail).then(run, run);
        if (reaction) this.reactionTail = result.catch(() => undefined);
        else this.requestTail = result.catch(() => undefined);
        return result;
    }
    private merge(c: Conversation, page: ChatPageDTO, history = false, older = false, changes = false) {
        // Most polls bring nothing; keeping the array keeps the rows and the sort untouched.
        if (!page.messages?.length) {
            c.readId = maxID(c.readId, page.readId);
            if (history) c.unread = page.unread;
            return;
        }
        const messages = new Map(c.messages.map((m) => [m.id, m]));
        const confirmed = new Set((page.messages ?? []).filter((m) => m.senderId === this.userId).map((m) => m.clientId));
        for (const message of page.messages ?? []) if (message.senderId === this.userId && message.attachmentIds?.length) this.confirmAttachments(message.attachmentIds);
        for (const pending of c.pending) if (confirmed.has(pending.clientId) && pending.attachmentIds?.length) this.confirmAttachments(pending.attachmentIds);
        c.pending = c.pending.filter((p) => !confirmed.has(p.clientId));
        const frozen = c.olderWindow && !older;
        let received = false;
        for (const m of page.messages ?? []) {
            const existing = messages.get(m.id);
            if ((changes && !existing) || (existing && compare(existing.revision ?? "0", m.revision ?? "0") > 0)) continue;
            const fresh = !messages.has(m.id);
            if (fresh && c.loaded && !older && m.senderId !== this.userId && compare(m.id, c.cursor) > 0) received = true;
            if (!frozen || messages.has(m.id)) messages.set(m.id, m);
            else c.newMessages = true;
        }
        if (received) this.onMessageReceived();
        const sorted = [...messages.values()].sort((a, b) => compare(a.id, b.id));
        if (older && sorted.length > 500) { c.messages = sorted.slice(0, 500); c.olderWindow = true; }
        else c.messages = sorted.slice(-500);
        // A snapshot read before the server applied a reaction must not undo it on screen.
        for (const pending of this.reacting.values()) if (pending.conversation === c) c.messages = c.messages.map((entry) => entry.id === pending.id ? withReaction(entry, pending.emoji, pending.add) : entry);
        c.readId = maxID(c.readId, page.readId);
        if (history) c.unread = page.unread;
    }

    async sync(target: ChatTarget, older = false, background = false) {
        if (target.id === "0" || this.disposed || !this.connected) return;
        const key = keyOf(target);
        if (this.busy.has(key)) return;
        const c = this.conversation(target);
        const epoch = this.epoch;
        // A poll over loaded history is invisible unless it finds something.
        const quiet = background && c.loaded && !older;
        const before = quiet ? visibleState(c) : "";
        this.busy.add(key);
        if (!quiet) {c.loading = true; this.notify();}
        try {
            if (!c.loaded || older) {
                if (!c.loaded) {
                    // Capture changes before the history snapshot, so first open neither
                    // replays the entire deletion log nor misses a concurrent deletion.
                    const watermark = await this.request("changes", target, {forward: true});
                    c.changeCursor = watermark.cursor;
                }
                let cursor = older ? c.messages[0]?.id ?? "0" : "0";
                for (let i = 0; i < (older ? 2 : 1); i++) {
                    if (epoch !== this.epoch) break;
                    const page = await this.request("history", target, {cursor});
                    this.merge(c, page, true, older);
                    c.hasOlder = page.hasMore;
                    if (!c.loaded) c.cursor = (page.messages ?? []).reduce((id, m) => maxID(id, m.id), c.cursor);
                    c.loaded = true;
                    if (!page.hasMore) break;
                    cursor = page.cursor;
                }
            } else {
                const page = await this.request("history", target, {cursor: c.cursor, forward: true});
                this.merge(c, page, true);
                c.cursor = maxID(c.cursor, page.cursor);
                if (page.hasMore) this.invalidate();
            }
            c.error = "";
        } catch (e) {
            if (!this.disposed && epoch === this.epoch) {
                if (!background || !c.loaded) c.error = chatError(e);
                else this.invalidate();
            }
        }
        finally {
            c.loading = false; this.busy.delete(key);
            if (!quiet || before !== visibleState(c)) this.notify();
        }
    }

    private async syncChanges(target: ChatTarget) {
        const c = this.conversation(target);
        if (!c.loaded || target.id === "0") return;
        const epoch = this.epoch;
        try {
            const changes = await this.request("changes", target, {cursor: c.changeCursor});
            this.merge(c, changes, false, false, true);
            c.changeCursor = maxID(c.changeCursor, changes.cursor);
            if (changes.hasMore) this.invalidate();
            if (changes.messages?.length) {this.pinsStale.add(keyOf(target)); this.notify();}
        } catch {
            // Keep the loaded history and retry; this is a background read,
            // not a failed user action. Old-session errors must stay silent.
            if (!this.disposed && this.connected && epoch === this.epoch) this.invalidate();
        }
    }

    async refreshDialogs(more = false, background = false) {
        if (this.listing || this.disposed || !this.connected) return;
        this.listing = true;
        const epoch = this.epoch;
        const before = JSON.stringify([this.dialogs, this.dialogError, this.dialogHasMore]);
        try {
            const result = new Map<string, ChatDialogDTO>(this.dialogs.map((d) => [d.userId, d]));
            const cursor = more || this.dialogHasMore ? this.dialogCursor : "0";
            const page = await this.request("dialogs", undefined, {cursor});
            for (const d of page.dialogs ?? []) result.set(d.userId, d);
            this.dialogHasMore = page.hasMore;
            this.dialogCursor = page.cursor;
            if (page.hasMore) this.invalidate();
            this.dialogs = [...result.values()].sort((a, b) => compare(b.latestId, a.latestId)).slice(0, 256);
            this.dialogError = "";
        } catch (e) {
            if (!this.disposed && this.connected && epoch === this.epoch) {
                if (!background) this.dialogError = chatError(e);
                else this.invalidate();
            }
        }
        finally {
            this.listing = false;
            if (before !== JSON.stringify([this.dialogs, this.dialogError, this.dialogHasMore])) this.notify();
        }
    }

    async latest(target: ChatTarget) {
        const c = this.conversation(target);
        if (this.busy.has(keyOf(target))) return;
        c.messages = []; c.loaded = false; c.olderWindow = false; c.newMessages = false; c.hasOlder = false; c.cursor = "0";
        await this.sync(target);
    }

    async dialogPage(cursor: string) {
        const dialogs: ChatDialogDTO[] = [];
        let hasMore = false;
        for (let i = 0; i < 4; i++) {
            const page = await this.request("dialogs", undefined, {cursor});
            dialogs.push(...(page.dialogs ?? []));
            cursor = page.cursor; hasMore = page.hasMore;
            if (!hasMore) break;
        }
        return {dialogs, cursor, hasMore};
    }

    async send(target: ChatTarget, text: string, retry?: PendingMessage, attachmentIds: string[] = []) {
        const c = this.conversation(target);
        if (retry?.state === "sending") return;
        if (!retry && [...this.conversations.values()].reduce((sum, v) => sum + v.pending.length, 0) >= 32) {
            c.error = errorCode("chat-too-many-pending"); this.notify(); return;
        }
        const pending: PendingMessage = retry ?? {clientId: crypto.randomUUID().replace(/-/g, ""), text, attachmentIds, sentAtMS: Date.now(), state: "sending"};
        pending.state = "sending"; pending.error = undefined;
        if (!retry) { c.pending.push(pending); c.draft = ""; }
        this.notify();
        try {
            this.merge(c, await this.request("send", target, {clientId: pending.clientId, text: pending.text, ...(pending.attachmentIds?.length ? {attachmentIds: pending.attachmentIds} : {})}));
            if (pending.attachmentIds?.length) this.confirmAttachments(pending.attachmentIds);
            c.error = "";
        }
        catch (e) { if (!this.disposed) { pending.state = "error"; pending.error = chatError(e); } }
        finally { this.notify(); }
        this.invalidate();
    }

    async remove(target: ChatTarget, id: string, attachmentId?: string) {
        const c = this.conversation(target);
        const message = c.messages.find((entry) => entry.id === id);
        if (!message || message.senderId !== this.userId || message.deleted) return;
        try {
            const page = await this.request(attachmentId ? "delete-file" : "delete", target, {cursor: id, ...(attachmentId ? {attachmentIds: [attachmentId]} : {})});
            this.merge(c, page);
            c.error = "";
        } catch (e) {if (!this.disposed) c.error = chatError(e);}
        finally {this.notify(); this.invalidate();}
    }

    async react(target: ChatTarget, id: string, emoji: string) {
        const c = this.conversation(target), key = `${keyOf(target)}:${id}:${emoji}`;
        const message = c.messages.find((entry) => entry.id === id);
        if (!message || message.deleted || this.disposed || !this.connected || this.reacting.has(key)) return;
        const add = !message.reactions?.some((r) => r.emoji === emoji && r.mine);
        // Requests are paced one per second, so the chip must answer before the server does.
        const apply = (mine: boolean) => {c.messages = c.messages.map((entry) => entry.id === id ? withReaction(entry, emoji, mine) : entry);};
        this.reacting.set(key, {conversation: c, id, emoji, add});
        apply(add);
        this.notifyContent();
        try {
            const page = await this.request("react", target, {cursor: id, text: emoji, forward: add});
            this.reacting.delete(key);
            this.merge(c, page);
            c.error = "";
            this.notifyContent();
        } catch (e) {
            this.reacting.delete(key);
            if (!this.disposed) {apply(!add); c.error = chatError(e); this.notify();}
        }
    }

    pinnedMessage(target: ChatTarget, id: string): ChatMessageDTO | undefined {
        const loaded = this.conversation(target).messages.find((m) => m.id === id && !m.deleted);
        // Kept for when a jump or the history limit unloads the message.
        if (loaded && !this.pinTexts.has(id)) this.rememberPinned([loaded]);
        return loaded ?? this.pinTexts.get(id);
    }
    private rememberPinned(messages: ChatMessageDTO[] | null | undefined) {
        for (const m of messages ?? []) if (!m.deleted) {this.pinTexts.delete(m.id); this.pinTexts.set(m.id, m);}
        while (this.pinTexts.size > 1024) this.pinTexts.delete(this.pinTexts.keys().next().value!);
    }
    private pinsFailed(e: unknown) {
        if (!/некорректный запрос чата/i.test(chatError(e))) return false;
        this.pinsSupported = false;
        this.notify();
        return true;
    }
    // Background reads take the ID list first, then the text of the latest pin for the bar.
    private async syncPins(target: ChatTarget) {
        const key = keyOf(target), c = this.conversation(target);
        if (!this.pinsSupported || !c.loaded || target.id === "0" || !this.pinsStale.has(key) || this.pinsLoading.has(key)) return;
        const epoch = this.epoch;
        this.pinsLoading.add(key);
        this.pinsStale.delete(key);
        try {
            const page = await this.request("pins", target, {forward: true});
            if (epoch !== this.epoch) return;
            const pinned = page.pinnedIds ?? [];
            const changed = pinned.join() !== c.pinned.join();
            c.pinned = pinned;
            if (pinned[0] && !this.pinnedMessage(target, pinned[0])) await this.loadPinTexts(target, 1);
            if (changed) this.notify();
        } catch (e) {
            if (!this.pinsFailed(e) && !this.disposed && this.connected && epoch === this.epoch) {this.pinsStale.add(key); this.invalidate();}
        } finally {this.pinsLoading.delete(key);}
    }
    // Reads pinned texts in list order until the first `count` pins are known. A page starts
    // at the first unknown pin; the list holds at most 50 pins, so the loop is bounded.
    async loadPinTexts(target: ChatTarget, count = Infinity) {
        const c = this.conversation(target), epoch = this.epoch;
        let found = false;
        for (let page = 0; page < 50 && this.pinsSupported && epoch === this.epoch; page++) {
            const offset = c.pinned.slice(0, count).findIndex((id) => !this.pinnedMessage(target, id));
            if (offset < 0) break;
            try {
                const result = await this.request("pins", target, {cursor: String(offset)});
                this.rememberPinned(result.messages);
                found = found || !!result.messages?.length;
                if (!result.messages?.length) break;
            } catch (e) {this.pinsFailed(e); break;}
        }
        if (found) this.notifyContent();
    }
    async pin(target: ChatTarget, id: string) {
        const c = this.conversation(target), key = keyOf(target);
        // A pin from the list may name a message outside the loaded history.
        const message = this.pinnedMessage(target, id);
        const add = !c.pinned.includes(id), before = c.pinned;
        if ((add && !message) || this.disposed || !this.connected || !this.pinsSupported) return;
        // The list answers at once; the server's own list replaces it right after.
        if (message) this.rememberPinned([message]);
        c.pinned = add ? [id, ...before] : before.filter((entry) => entry !== id);
        this.notify();
        try {
            this.merge(c, await this.request("pin", target, {cursor: id, forward: add}));
            c.error = "";
        } catch (e) {
            if (!this.disposed) {c.pinned = before; if (!this.pinsFailed(e)) c.error = chatError(e);}
        } finally {
            this.pinsStale.add(key);
            this.notify();
            void this.syncPins(target);
        }
    }
    // Brings a message into the loaded history. A message outside it opens a window around it,
    // as reading far back does; the jump button then returns to the latest messages.
    async reveal(target: ChatTarget, id: string): Promise<boolean> {
        const c = this.conversation(target), key = keyOf(target);
        if (c.messages.some((m) => m.id === id)) return true;
        if (this.busy.has(key) || this.disposed || !this.connected) return false;
        const epoch = this.epoch;
        this.busy.add(key); c.loading = true; this.notify();
        try {
            const before = await this.request("history", target, {cursor: (BigInt(id) + 1n).toString()});
            if (epoch !== this.epoch || !before.messages?.some((m) => m.id === id)) return false;
            const after = await this.request("history", target, {cursor: id, forward: true});
            if (epoch !== this.epoch) return false;
            c.messages = [];
            this.merge(c, before, true, true);
            this.merge(c, after, true, true);
            c.hasOlder = before.hasMore;
            c.olderWindow = after.hasMore;
            c.newMessages = false;
            if (!after.hasMore) c.cursor = maxID(c.cursor, after.cursor);
            c.error = "";
            return true;
        } catch (e) {
            if (!this.disposed && epoch === this.epoch) c.error = chatError(e);
            return false;
        } finally {
            c.loading = false; this.busy.delete(key);
            this.notify();
        }
    }

    async markRead(target: ChatTarget) {
        const key = keyOf(target), c = this.conversation(target);
        const last = c.messages[c.messages.length - 1]?.id ?? "0";
        const id = compare(last, c.cursor) < 0 ? last : c.cursor;
        if (this.disposed || !this.connected || c.olderWindow || compare(id, c.readId) <= 0) return;
        this.readQueue.set(key, {target, id: maxID(id, this.readQueue.get(key)?.id ?? "0")});
        this.invalidate();
    }
    private async flushReads() {
        const epoch = this.epoch;
        for (const [key, {target, id}] of [...this.readQueue]) {
            if (epoch !== this.epoch || !this.connected) break;
            if (target.kind === "channel" && !this.watched.has(key)) { this.readQueue.delete(key); continue; }
            if (this.reading.has(key)) continue;
            this.readQueue.delete(key);
            const c = this.conversation(target);
            if (compare(id, c.readId) <= 0) continue;
            this.reading.add(key);
            try {
                await this.request("read", target, {cursor: id});
                c.readId = maxID(c.readId, id);
                this.notify();
            } catch {
                if (!this.disposed && this.connected && epoch === this.epoch && this.watched.has(key)) {
                    this.readQueue.set(key, {target, id: maxID(id, this.readQueue.get(key)?.id ?? "0")});
                    this.invalidate();
                }
            } finally { this.reading.delete(key); }
        }
    }
}
