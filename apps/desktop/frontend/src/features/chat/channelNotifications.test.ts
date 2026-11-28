import {afterEach, beforeEach, describe, expect, it, vi} from "vitest";

// The module keeps the parsed list in memory, so each test loads it afresh.
const load = () => import("./channelNotifications");
let storage: Map<string, string>;

beforeEach(() => {
    vi.resetModules();
    storage = new Map();
    vi.stubGlobal("localStorage", {
        getItem: (key: string) => storage.get(key) ?? null,
        setItem: (key: string, value: string) => { storage.set(key, value); },
        removeItem: (key: string) => { storage.delete(key); },
    });
});
afterEach(() => { vi.unstubAllGlobals(); });

describe("channel notification settings", () => {
    it("keys the setting by server identity and account, not by address", async () => {
        const {channelNotificationScope} = await load();
        expect(channelNotificationScope("10.0.0.1:9000|server-key|client-key|42")).toBe("server-key|42");
        expect(channelNotificationScope("10.0.0.2:9000|server-key|client-key|42")).toBe("server-key|42");
        expect(channelNotificationScope("odd-context")).toBe("odd-context");
    });
    it("mutes and unmutes one channel of one server and keeps it across loads", async () => {
        const first = await load();
        first.setChannelMuted("server|1", "7", true);
        first.setChannelMuted("server|1", "7", true);
        expect(first.channelMuted("server|1", "7")).toBe(true);
        expect(first.channelMuted("server|1", "8")).toBe(false);
        expect(first.channelMuted("other|1", "7")).toBe(false);
        expect(JSON.parse(storage.get("sonoryx.mutedChannels")!)).toEqual({"server|1": ["7"]});

        vi.resetModules();
        const second = await load();
        expect(second.channelMuted("server|1", "7")).toBe(true);
        second.setChannelMuted("server|1", "7", false);
        expect(second.channelMuted("server|1", "7")).toBe(false);
        expect(JSON.parse(storage.get("sonoryx.mutedChannels")!)).toEqual({});
    });
    it("ignores a corrupt saved value", async () => {
        storage.set("sonoryx.mutedChannels", "{not json");
        const {channelMuted, setChannelMuted} = await load();
        expect(channelMuted("server|1", "7")).toBe(false);
        setChannelMuted("server|1", "7", true);
        expect(channelMuted("server|1", "7")).toBe(true);
    });
    it("keeps at most 512 channels per server, dropping the oldest", async () => {
        storage.set("sonoryx.mutedChannels", JSON.stringify({"server|1": Array.from({length: 512}, (_, i) => String(i)), "x": [1, "2"]}));
        const {channelMuted, setChannelMuted} = await load();
        expect(channelMuted("x", "2")).toBe(true);
        setChannelMuted("server|1", "1000", true);
        expect(channelMuted("server|1", "1000")).toBe(true);
        expect(channelMuted("server|1", "0")).toBe(false);
        expect(JSON.parse(storage.get("sonoryx.mutedChannels")!)["server|1"]).toHaveLength(512);
    });
});

describe("message popup preview", () => {
    it("puts the text on one line and cuts it to 120 characters", async () => {
        const {messagePreview} = await load();
        expect(messagePreview({text: "  hello\n\tworld  "}, "Attachment")).toBe("hello world");
        const long = messagePreview({text: "😀".repeat(130)}, "Attachment");
        expect([...long]).toHaveLength(120);
        expect(long.endsWith("…")).toBe(true);
        expect(messagePreview({text: "x".repeat(120)}, "Attachment")).toBe("x".repeat(120));
    });
    it("labels a message without text and drops direction overrides", async () => {
        const {messagePreview} = await load();
        expect(messagePreview({text: ""}, "Attachment")).toBe("📎 Attachment");
        expect(messagePreview({text: "‮ \n"}, "Attachment")).toBe("📎 Attachment");
        expect(messagePreview({text: "a‮b"}, "Attachment")).toBe("ab");
    });
});
