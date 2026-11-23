import {afterEach, beforeEach, describe, expect, it, vi} from "vitest";
import {desktopAPI, type ChatMessageDTO, type ChatPageDTO, type ChatRequestDTO} from "../../api";
import {ChatStore, type ChatTarget} from "./chatStore";

vi.mock("../../api", () => ({desktopAPI: {chat: vi.fn(), fileQueue: vi.fn(), fileAttached: vi.fn().mockResolvedValue(undefined)}}));
const channel: ChatTarget = {kind: "channel", id: "1"};
const direct: ChatTarget = {kind: "direct", id: "2"};
const message = (id: number, senderId = "2", clientId = `client-${id}`): ChatMessageDTO => ({
    deleted: false, revision: "0", id: String(id), senderId, recipientId: "1", channelId: "0", clientId, senderName: "bob", text: `message ${id}`, sentAtMs: id,
});
const page = (extra: Partial<ChatPageDTO> = {}): ChatPageDTO => ({userId: "1", latestId: "0", readId: "0", cursor: "0", unread: 0, hasMore: false, messages: [], dialogs: [], ...extra});
let store: ChatStore;
// Store only awaits the bridge promise; cancellation is owned by the Go session.
const chat = vi.mocked(desktopAPI.chat as (request: ChatRequestDTO) => Promise<ChatPageDTO>);
const tick = () => (vi.mocked(window.setInterval).mock.calls[0][0] as () => void)();

beforeEach(() => {
    vi.useFakeTimers(); vi.setSystemTime(new Date("2026-10-02T12:00:00Z"));
    vi.stubGlobal("window", {setInterval: vi.fn(() => 1), clearInterval: vi.fn(), setTimeout: globalThis.setTimeout, clearTimeout: globalThis.clearTimeout});
    chat.mockReset(); chat.mockResolvedValue(page());
    vi.mocked(desktopAPI.fileAttached).mockReset().mockResolvedValue(undefined);
    vi.mocked(desktopAPI.fileQueue).mockReset().mockResolvedValue(["17", "18"].map((id) => ({id, context: "context", direction: "upload", kind: 2, targetId: "2", name: `${id}.png`, size: 1, offset: 1, attachmentId: id, state: "ready", error: ""})));
    store = new ChatStore("context", "1"); store.setConnected(true);
});
afterEach(() => { store.dispose(); vi.useRealTimers(); vi.unstubAllGlobals(); });

describe("chat drafts and confirmations", () => {
    it("deletes only own messages and merges the server tombstone", async () => {
        const c = store.conversation(direct);
        c.messages = [message(1, "1"), message(2, "2")];
        await store.remove(direct, "2");
        expect(chat).not.toHaveBeenCalled();
        chat.mockResolvedValueOnce(page({messages: [{...message(1, "1"), text: "", deleted: true, revision: "5"}]}));
        await store.remove(direct, "1");
        expect(chat.mock.calls[0][0]).toMatchObject({operation: "delete", cursor: "1", kind: "direct", targetId: "2"});
        expect(c.messages[0].deleted).toBe(true);
        expect(c.messages[0].text).toBe("");
        await store.remove(direct, "1");
        expect(chat).toHaveBeenCalledTimes(1);
    });
    it("deletes a selected attachment without removing the message text", async () => {
        const c = store.conversation(direct);
        c.messages = [{...message(1, "1"), attachmentIds: ["17", "18"]}];
        chat.mockResolvedValueOnce(page({messages: [{...message(1, "1"), attachmentIds: ["18"], revision: "6"}]}));
        await store.remove(direct, "1", "17");
        expect(chat.mock.calls[0][0]).toMatchObject({operation: "delete-file", cursor: "1", attachmentIds: ["17"]});
        expect(c.messages[0].attachmentIds).toEqual(["18"]);
        expect(c.messages[0].text).toBe("message 1");
    });
    it("finishes attachments when history confirms a send whose acknowledgement was lost", async () => {
        chat.mockRejectedValueOnce(new Error("lost acknowledgement"));
        await store.send(direct, "photo", undefined, ["17"]);
        const pending = store.conversation(direct).pending[0];
        chat.mockResolvedValueOnce(page()); // Change watermark precedes the initial history.
        chat.mockResolvedValueOnce(page({messages: [{...message(1, "1", pending.clientId), attachmentIds: ["17"]}], cursor: "1"}));
        const sync = store.sync(direct);
        await vi.advanceTimersByTimeAsync(2000); await sync;
        expect(store.conversation(direct).pending).toEqual([]);
        expect(store.confirmedAttachments.has("17")).toBe(true);
        expect(desktopAPI.fileAttached).toHaveBeenCalledWith("context", ["17"]);
        expect(chat.mock.calls.filter(([request]) => request.operation === "send")).toHaveLength(1);
    });
    it("retries local queue completion without resending the delivered message", async () => {
        vi.mocked(desktopAPI.fileAttached).mockRejectedValueOnce(new Error("disk full"));
        chat.mockImplementationOnce(async (request) => page({messages: [{...message(1, "1", request.clientId), attachmentIds: ["17"]}]}));
        await store.send(direct, "photo", undefined, ["17"]);
        await vi.advanceTimersByTimeAsync(4000);
        tick(); await vi.advanceTimersByTimeAsync(1000);
        expect(store.conversation(direct).pending).toEqual([]);
        expect(store.conversation(direct).error).toBe("");
        expect(desktopAPI.fileAttached).toHaveBeenCalledTimes(2);
        expect(chat.mock.calls.filter(([request]) => request.operation === "send")).toHaveLength(1);
    });
    it("keeps attachment IDs and clientId when retrying a lost send acknowledgement", async () => {
        chat.mockRejectedValueOnce(new Error("lost acknowledgement"));
        await store.send(direct, "", undefined, ["17", "18"]);
        const pending = store.conversation(direct).pending[0];
        expect(pending.attachmentIds).toEqual(["17", "18"]);
        expect(desktopAPI.fileAttached).not.toHaveBeenCalled();
        chat.mockResolvedValueOnce(page({messages: [{...message(1, "1", pending.clientId), text: "", attachmentIds: ["17", "18"]}]}));
        const retry = store.send(direct, pending.text, pending);
        await vi.advanceTimersByTimeAsync(1000); await retry;
        expect(chat.mock.calls[1][0].attachmentIds).toEqual(["17", "18"]);
        expect(chat.mock.calls[1][0].clientId).toBe(chat.mock.calls[0][0].clientId);
        expect(desktopAPI.fileAttached).toHaveBeenCalledWith("context", ["17", "18"]);
        expect(store.conversation(direct).pending).toEqual([]);
    });
    it("limits UTF-8 bytes without cutting emoji and preserves newlines", () => {
        store.setDraft(channel, "я".repeat(600));
        expect(store.conversation(channel).draft).toBe("я".repeat(500));
        store.setDraft(channel, "🙂".repeat(251));
        expect(store.conversation(channel).draft).toBe("🙂".repeat(250));
        store.setDraft(channel, "first\nsecond");
        expect(store.conversation(channel).draft).toBe("first\nsecond");
    });
    it("retries with the original clientId and removes confirmed pending messages", async () => {
        chat.mockRejectedValueOnce(new Error("lost acknowledgement"));
        await store.send(direct, "hello");
        const pending = store.conversation(direct).pending[0];
        expect(pending.state).toBe("error");
        chat.mockResolvedValueOnce(page({messages: [message(1, "1", pending.clientId)], cursor: "1"}));
        const retry = store.send(direct, pending.text, pending);
        await vi.advanceTimersByTimeAsync(1000); await retry;
        expect(chat.mock.calls[0][0].clientId).toBe(chat.mock.calls[1][0].clientId);
        expect(store.conversation(direct).pending).toEqual([]);
        expect(store.conversation(direct).messages).toHaveLength(1);
    });
});

describe("history windows", () => {
    it("keeps a continuous old window and reloads latest instead of inserting across a gap", async () => {
        const c = store.conversation(direct);
        c.loaded = true; c.cursor = "1000"; c.messages = Array.from({length: 500}, (_, i) => message(501 + i));
        chat.mockResolvedValueOnce(page({messages: [500, 499, 498, 497].map((id) => message(id)), cursor: "497"}));
        await store.sync(direct, true);
        expect(c.olderWindow).toBe(true); expect(c.messages[0].id).toBe("497");
        expect(c.messages[c.messages.length - 1]?.id).toBe("996");
        chat.mockResolvedValueOnce(page({messages: [message(1001)], cursor: "1001", latestId: "1001"}));
        const sync = store.sync(direct); await vi.advanceTimersByTimeAsync(1000); await sync;
        expect(c.cursor).toBe("1001"); expect(c.newMessages).toBe(true);
        expect(c.messages[c.messages.length - 1]?.id).toBe("996");
        chat.mockResolvedValueOnce(page());
        chat.mockResolvedValueOnce(page({messages: [message(1002), message(1001)], cursor: "1001", latestId: "1002"}));
        const latest = store.latest(direct); await vi.advanceTimersByTimeAsync(2000); await latest;
        expect(c.messages.map((m) => m.id)).toEqual(["1001", "1002"]);
        expect(c.olderWindow).toBe(false); expect(c.newMessages).toBe(false);
    });
    it("confirms sends in the frozen window without displaying or marking unseen messages read", async () => {
        const c = store.conversation(direct);
        c.loaded = true; c.cursor = "500"; c.messages = [message(10)]; c.olderWindow = true;
        chat.mockImplementation(async (r: ChatRequestDTO) => page({messages: [message(501, "1", r.clientId)], cursor: "501"}));
        await store.send(direct, "hello");
        expect(c.pending).toHaveLength(0); expect(c.messages.map((m) => m.id)).toEqual(["10"]);
        expect(c.newMessages).toBe(true);
        await store.markRead(direct); tick(); await vi.advanceTimersByTimeAsync(1000);
        expect(chat.mock.calls.some(([r]) => r.operation === "read")).toBe(false);
    });
});

describe("session lifecycle", () => {
    it("cancels a queued send before reconnect and preserves its original text and clientId", async () => {
        const initial = store.sync(channel); await vi.advanceTimersByTimeAsync(1000); await initial;
        const send = store.send(direct, "keep me");
        await vi.advanceTimersByTimeAsync(0);
        store.setConnected(false); store.setConnected(true);
        await vi.advanceTimersByTimeAsync(1000); await send;
        expect(chat).toHaveBeenCalledTimes(2);
        expect(store.conversation(direct).pending[0]).toMatchObject({text: "keep me", state: "error"});
    });
    it("ignores an in-flight history response from the previous session", async () => {
        let resolve!: (p: ChatPageDTO) => void;
        chat.mockImplementationOnce(() => new Promise((done) => { resolve = done; }));
        const sync = store.sync(direct); await vi.advanceTimersByTimeAsync(0);
        store.setConnected(false); store.setConnected(true);
        resolve(page({messages: [message(1)], cursor: "1"})); await sync;
        expect(store.conversation(direct).messages).toEqual([]);
        expect(store.conversation(direct).loading).toBe(false);
    });
    it("does not reopen dismissed tabs on remount or reconnect, but opens for a newer message", () => {
        const d = {userId: "2", displayName: "bob", latestId: "9", readId: "0", unread: 2};
        store.dialogs = [d]; store.dismissDialog("2");
        const unwatch = store.watch(direct); unwatch(); store.watch(direct);
        store.setConnected(false); store.setConnected(true);
        expect(store.shouldOpenDialog({...d})).toBe(false);
        expect(store.shouldOpenDialog({...d, latestId: "10"})).toBe(true);
    });
    it("does not reopen a dismissed tab for a stale catalog response", () => {
        store.conversation(direct).messages = [message(10)];
        store.dismissDialog("2");
        expect(store.shouldOpenDialog({userId: "2", displayName: "bob", latestId: "9", readId: "0", unread: 1})).toBe(false);
    });
});

describe("request scheduler", () => {
    it("keeps loaded messages and attachments without errors when background polling fails", async () => {
        const c = store.conversation(channel);
        c.loaded = true; c.cursor = "1";
        c.messages = [{...message(1), attachmentIds: ["17"]}];
        store.watch(channel);
        chat.mockImplementation(async (request) => {
            if (request.operation !== "dialogs") throw new Error("request timed out");
            return page();
        });
        // A conversation loads its pin list once, after history and changes.
        tick(); await vi.advanceTimersByTimeAsync(3000);
        expect(c.error).toBe("");
        expect(c.messages).toHaveLength(1);
        expect(c.messages[0].attachmentIds).toEqual(["17"]);
        expect(chat.mock.calls.map(([request]) => request.operation)).toEqual(["history", "changes", "pins", "dialogs"]);
    });
    it("captures the change watermark before history and catches deletions concurrent with opening", async () => {
        store.watch(channel);
        chat.mockImplementation(async (request) => {
            if (request.operation === "changes" && request.forward) return page({cursor: "5000"});
            if (request.operation === "history") return page({cursor: "99", latestId: "99", messages: [message(99)]});
            if (request.operation === "changes") return page({cursor: "5001", messages: [{...message(99), text: "", deleted: true, revision: "5001"}]});
            return page();
        });
        tick(); await vi.advanceTimersByTimeAsync(3000);
        const requests = chat.mock.calls.map(([request]) => request);
        expect(requests[0]).toMatchObject({operation: "changes", forward: true, cursor: "0"});
        expect(requests[1].operation).toBe("history");
        expect(requests[2]).toMatchObject({operation: "changes", forward: false, cursor: "5000"});
        const c = store.conversation(channel);
        expect(c.messages[0].deleted).toBe(true);
        expect(c.changeCursor).toBe("5001");
    });
    it("recovers deletion of an old message without adding unseen messages or accepting stale history", async () => {
        const c = store.conversation(channel);
        c.loaded = true; c.cursor = "100"; c.messages = [message(1)];
        store.watch(channel);
        chat.mockImplementation(async (request) => request.operation === "changes"
            ? page({cursor: "9", messages: [{...message(1), deleted: true, text: "", revision: "9"}, {...message(99), deleted: true, text: "", revision: "8"}]})
            : page({cursor: "100", latestId: "100"}));
        tick(); await vi.advanceTimersByTimeAsync(3000);
        expect(c.changeCursor).toBe("9"); expect(c.cursor).toBe("100");
        expect(c.messages.map((entry) => entry.id)).toEqual(["1"]);
        expect(c.messages[0].deleted).toBe(true);
        chat.mockResolvedValueOnce(page({messages: [message(1)], cursor: "100"}));
        const sync = store.sync(channel); await vi.advanceTimersByTimeAsync(1000); await sync;
        expect(c.messages[0].deleted).toBe(true);
    });
    it("coalesces notifications and duplicate watchers into one sync pass", async () => {
        store.watch(channel); store.watch(channel);
        for (let i = 0; i < 100; i++) store.invalidate();
        tick(); await vi.advanceTimersByTimeAsync(4000);
        expect(chat.mock.calls.map(([r]) => r.operation)).toEqual(["changes", "history", "changes", "pins", "dialogs"]);
        for (let i = 0; i < 100; i++) store.invalidate();
        tick(); await vi.advanceTimersByTimeAsync(0);
        expect(chat).toHaveBeenCalledTimes(5);
    });
    it("coalesces read markers to the greatest visible ID", async () => {
        store.watch(channel);
        const c = store.conversation(channel); c.loaded = true;
        for (const id of [1, 3, 2]) { c.messages = [message(id)]; c.cursor = String(id); await store.markRead(channel); }
        tick(); await vi.advanceTimersByTimeAsync(2000);
        const reads = chat.mock.calls.filter(([r]) => r.operation === "read");
        expect(reads).toHaveLength(1); expect(reads[0][0].cursor).toBe("3");
    });
    it("spaces requests by a second and pauses after a rate-limit rejection", async () => {
        chat.mockRejectedValueOnce(new Error("server error: лимит запросов чата; повторите позже"));
        await store.sync(channel);
        await store.refreshDialogs(); expect(chat).toHaveBeenCalledTimes(1);
        await vi.advanceTimersByTimeAsync(59999); await store.sync(channel);
        expect(chat).toHaveBeenCalledTimes(1);
        await vi.advanceTimersByTimeAsync(1);
        const resumed = store.sync(channel); await vi.advanceTimersByTimeAsync(1000); await resumed;
        expect(chat).toHaveBeenCalledTimes(3);
        const dialogs = store.refreshDialogs(); await vi.advanceTimersByTimeAsync(999);
        expect(chat).toHaveBeenCalledTimes(3);
        await vi.advanceTimersByTimeAsync(1); await dialogs; expect(chat).toHaveBeenCalledTimes(4);
    });
});
