import {afterEach, beforeEach, expect, it, vi} from "vitest";
import {desktopAPI} from "../../api";
import {attachmentMetadata} from "./files";

vi.mock("../../api", () => ({desktopAPI: {fileMetadata: vi.fn()}}));
const metadata = vi.mocked(desktopAPI.fileMetadata);
beforeEach(() => {metadata.mockReset();});
afterEach(() => {vi.restoreAllMocks();});
const file = (id: string) => ({id, name: `${id}.txt`, size: 5, sha256: "hash", deleted: false});

it("batches history metadata into requests of at most 64 IDs", async () => {
    metadata.mockImplementation(async (_context, ids) => ids.map(file));
    const files = await Promise.all(Array.from({length: 67}, (_, i) => attachmentMetadata("batch-context", String(i + 1), false)));
    expect(files).toHaveLength(67);
    expect(metadata.mock.calls.map((call) => call[1].length)).toEqual([64, 3]);
});
it("shares an in-flight request for the same attachment", async () => {
    metadata.mockImplementation(async (_context, ids) => ids.map(file));
    const one = attachmentMetadata("same-context", "1", false);
    const two = attachmentMetadata("same-context", "1", false);
    expect(one).toBe(two);
    await one;
    expect(metadata).toHaveBeenCalledTimes(1);
});
it("keeps readable files visible when another attachment is unavailable", async () => {
    metadata.mockResolvedValue([file("1")]);
    const results = await Promise.allSettled([attachmentMetadata("partial-context", "1", false), attachmentMetadata("partial-context", "2", false)]);
    expect(results[0].status).toBe("fulfilled");
    expect(results[1].status).toBe("rejected");
});

it("keeps cached metadata without timed refreshes and permits an explicit retry", async () => {
    const now = vi.spyOn(Date, "now").mockReturnValue(1000);
    metadata.mockResolvedValueOnce([file("1")]).mockResolvedValueOnce([{...file("1"), deleted: true}]);
    expect((await attachmentMetadata("expiry-context", "1", false)).deleted).toBe(false);
    now.mockReturnValue(30_999);
    expect((await attachmentMetadata("expiry-context", "1", false)).deleted).toBe(false);
    expect(metadata).toHaveBeenCalledTimes(1);
    now.mockReturnValue(3_600_000);
    expect((await attachmentMetadata("expiry-context", "1", false)).deleted).toBe(false);
    expect(metadata).toHaveBeenCalledTimes(1);
    expect((await attachmentMetadata("expiry-context", "1", true)).deleted).toBe(true);
    expect(metadata).toHaveBeenCalledTimes(2);
});
