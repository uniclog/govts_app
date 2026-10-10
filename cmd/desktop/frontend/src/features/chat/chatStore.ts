import {desktopAPI, type ChatDialogDTO, type ChatMessageDTO, type ChatPageDTO, type ChatRequestDTO} from "../../api";

export type ChatTarget = {kind: "channel" | "direct"; id: string};
export type PendingMessage = {clientId: string; text: string; sentAtMS: number; state: "sending" | "error"; error?: string};
export type Conversation = {
    messages: ChatMessageDTO[]; pending: PendingMessage[]; draft: string;
    loaded: boolean; loading: boolean; error: string; hasOlder: boolean;
    cursor: string; readId: string; unread: number; olderWindow: boolean; newMessages: boolean;
};
const keyOf = (t: ChatTarget) => `${t.kind}:${t.id}`;
const compare = (a: string, b: string) => BigInt(a) < BigInt(b) ? -1 : BigInt(a) > BigInt(b) ? 1 : 0;
const maxID = (a: string, b: string) => compare(a, b) > 0 ? a : b;
const limitDraft = (text: string): string => {
    const encoder = new TextEncoder();
    let result = "", bytes = 0;
    for (const character of text) {
        const size = encoder.encode(character).length;
        if (bytes + size > 1000) break;
        result += character; bytes += size;
    }
    return result;
};
export const chatError = (e: unknown) => (e instanceof Error ? e.message : String(e)).replace(/^Error:\s*/, "");

export class ChatStore {
    readonly context: string;
    readonly userId: string;
    dialogs: ChatDialogDTO[] = [];
    dialogError = "";
    dialogHasMore = false;
    private dialogCursor = "0";
    private listeners = new Set<() => void>();
    private conversations = new Map<string, Conversation>();
    private busy = new Set<string>();
    private reading = new Set<string>();
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
    private restoredDrafts: Record<string, string> = {};

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
    dispose() { this.disposed = true; this.lifecycle.abort(); window.clearInterval(this.timer); this.listeners.clear(); }
    prepareUpdate() {
        if ([...this.conversations.values()].some((c) => c.pending.length > 0)) throw new Error("В чате есть неподтверждённые сообщения. Отправьте или удалите их перед обновлением.");
        const drafts: Record<string,string> = {...this.restoredDrafts};
        for (const [key,c] of this.conversations) { if (c.draft) drafts[key]=c.draft; else delete drafts[key]; }
        localStorage.setItem(`sonoryx.updateDrafts:${this.context}`, JSON.stringify(drafts));
    }
    setConnected(connected: boolean) {
        if (connected === this.connected) return;
        this.connected = connected; this.epoch++; this.lifecycle.abort(); this.lifecycle = new AbortController();
        if (!connected) this.readQueue.clear();
        this.nextSyncAt = 0;
    }
    invalidate() { this.nextSyncAt = Math.min(this.nextSyncAt, Date.now() + 3000); }
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
    private async reconcile() {
        if (!this.connected || this.disposed || this.scheduled || Date.now() < Math.max(this.nextSyncAt, this.pausedUntil)) return;
        this.scheduled = true;
        const epoch = this.epoch;
        this.nextSyncAt = Date.now() + 5000;
        try {
            await this.flushReads();
            if (epoch !== this.epoch || !this.connected) return;
            await Promise.all([...this.watched.values()].map(({target}) => this.sync(target)));
            if (epoch !== this.epoch || !this.connected) return;
            await this.refreshDialogs();
        } finally { this.scheduled = false; }
    }
    subscribe = (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener); }; };
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
            c = {messages: [], pending: [], draft: "", loaded: false, loading: false, error: "", hasOlder: false, cursor: "0", readId: "0", unread: 0, olderWindow: false, newMessages: false};
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
        if (!c.draft && text && [...this.conversations.values()].filter((v) => v.draft).length >= 32) c.error = "Сохранено 32 черновика; отправьте или очистите один из них";
        else c.draft = limitDraft(text);
        this.notify();
    }

    private async request(operation: string, target?: ChatTarget, extra: Partial<ChatRequestDTO> = {}): Promise<ChatPageDTO> {
        const epoch = this.epoch, signal = this.lifecycle.signal;
        const check = () => {
            if (this.disposed || !this.connected || epoch !== this.epoch || signal.aborted) throw new Error("Сессия чата завершена");
        };
        const run = async () => {
            check();
            if (Date.now() < this.pausedUntil) throw new Error("Лимит запросов чата; повторите позже");
            const delay = Math.max(0, this.lastRequestAt + 1000 - Date.now());
            if (delay) await new Promise<void>((resolve, reject) => {
                const aborted = () => { window.clearTimeout(timer); reject(new Error("Сессия чата завершена")); };
                const timer = window.setTimeout(() => { signal.removeEventListener("abort", aborted); resolve(); }, delay);
                signal.addEventListener("abort", aborted, {once: true});
            });
            check(); this.lastRequestAt = Date.now();
            try {
                const page = await desktopAPI.chat({context: this.context, operation, kind: target?.kind ?? "", targetId: target?.id ?? "0", cursor: "0", forward: false, clientId: "", text: "", ...extra});
                check();
                if (page.userId !== this.userId) throw new Error("Учётная запись чата изменилась");
                return page;
            } catch (e) {
                if (/лимит запросов чата/i.test(chatError(e))) this.pausedUntil = Date.now() + 60000;
                throw e;
            }
        };
        const result = this.requestTail.then(run, run);
        this.requestTail = result.catch(() => undefined);
        return result;
    }
    private merge(c: Conversation, page: ChatPageDTO, history = false, older = false) {
        const messages = new Map(c.messages.map((m) => [m.id, m]));
        const confirmed = new Set((page.messages ?? []).filter((m) => m.senderId === this.userId).map((m) => m.clientId));
        c.pending = c.pending.filter((p) => !confirmed.has(p.clientId));
        const frozen = c.olderWindow && !older;
        for (const m of page.messages ?? []) {
            if (!frozen || messages.has(m.id)) messages.set(m.id, m);
            else c.newMessages = true;
        }
        const sorted = [...messages.values()].sort((a, b) => compare(a.id, b.id));
        if (older && sorted.length > 500) { c.messages = sorted.slice(0, 500); c.olderWindow = true; }
        else c.messages = sorted.slice(-500);
        c.readId = maxID(c.readId, page.readId);
        if (history) c.unread = page.unread;
    }

    async sync(target: ChatTarget, older = false) {
        if (target.id === "0" || this.disposed || !this.connected) return;
        const key = keyOf(target);
        if (this.busy.has(key)) return;
        const c = this.conversation(target);
        const epoch = this.epoch;
        this.busy.add(key); c.loading = true; this.notify();
        try {
            if (!c.loaded || older) {
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
        } catch (e) { if (!this.disposed && epoch === this.epoch) c.error = chatError(e); }
        finally { c.loading = false; this.busy.delete(key); this.notify(); }
    }

    async refreshDialogs(more = false) {
        if (this.listing || this.disposed || !this.connected) return;
        this.listing = true;
        const epoch = this.epoch;
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
        } catch (e) { if (!this.disposed && epoch === this.epoch) this.dialogError = chatError(e); }
        finally { this.listing = false; this.notify(); }
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

    async send(target: ChatTarget, text: string, retry?: PendingMessage) {
        const c = this.conversation(target);
        if (retry?.state === "sending") return;
        if (!retry && [...this.conversations.values()].reduce((sum, v) => sum + v.pending.length, 0) >= 32) {
            c.error = "Слишком много неподтверждённых сообщений"; this.notify(); return;
        }
        const pending: PendingMessage = retry ?? {clientId: crypto.randomUUID().replace(/-/g, ""), text, sentAtMS: Date.now(), state: "sending"};
        pending.state = "sending"; pending.error = undefined;
        if (!retry) { c.pending.push(pending); c.draft = ""; }
        this.notify();
        try { this.merge(c, await this.request("send", target, {clientId: pending.clientId, text: pending.text})); c.error = ""; }
        catch (e) { if (!this.disposed) { pending.state = "error"; pending.error = chatError(e); } }
        finally { this.notify(); }
        this.invalidate();
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
            } catch (e) {
                if (!this.disposed && this.connected && epoch === this.epoch && this.watched.has(key)) {
                    this.readQueue.set(key, {target, id: maxID(id, this.readQueue.get(key)?.id ?? "0")});
                    c.error = chatError(e); this.notify();
                }
            } finally { this.reading.delete(key); }
        }
    }
}
