import {afterEach, describe, expect, it, vi} from "vitest";
import {CaptureReleases} from "./captureReleases";

afterEach(() => vi.useRealTimers());

describe("recording release recovery", () => {
    it("retains an unacknowledged ID and retries after the editor is gone", async () => {
        vi.useFakeTimers();
        const end = vi.fn().mockRejectedValueOnce(new Error("bridge unavailable")).mockResolvedValue(undefined);
        const releases = new CaptureReleases(end);
        const recovered = vi.fn();
        releases.onRecovered(recovered);
        await expect(releases.release(7)).rejects.toThrow("bridge unavailable");
        expect(recovered).not.toHaveBeenCalled();
        await vi.advanceTimersByTimeAsync(500);
        expect(end.mock.calls).toEqual([[7], [7]]);
        expect(recovered).toHaveBeenCalledOnce();
        await vi.advanceTimersByTimeAsync(2000);
        expect(end).toHaveBeenCalledTimes(2);
    });

    it("does not lose an older release when another recording finishes", async () => {
        vi.useFakeTimers();
        let failed = true;
        const end = vi.fn(async (token: number) => {
            if (token === 3 && failed) throw new Error("temporary");
        });
        const releases = new CaptureReleases(end);
        await expect(releases.release(3)).rejects.toThrow("temporary");
        await releases.release(4);
        failed = false;
        await vi.advanceTimersByTimeAsync(500);
        expect(end.mock.calls).toEqual([[3], [4], [3]]);
    });

    it("coalesces duplicate releases while acknowledgement is pending", async () => {
        let acknowledge!: () => void;
        const end = vi.fn(() => new Promise<void>((resolve) => { acknowledge = resolve; }));
        const releases = new CaptureReleases(end);
        const first = releases.release(9);
        const second = releases.release(9);
        await Promise.resolve();
        expect(end).toHaveBeenCalledOnce();
        acknowledge();
        await Promise.all([first, second]);
    });
});
