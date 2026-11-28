import {describe, expect, it} from "vitest";
import {ScreenStatsCollector} from "./screenStats";

function report(...values: any[]): RTCStatsReport {
    return new Map(values.map((value, index) => [value.id ?? String(index), value])) as unknown as RTCStatsReport;
}

describe("ScreenStatsCollector", () => {
    it("reports actual codec, implementation and quality limit from the browser", () => {
        const value = new ScreenStatsCollector().collect(report(
            {id: "codec", type: "codec", mimeType: "video/AV1"},
            {id: "video", type: "outbound-rtp", kind: "video", codecId: "codec", encoderImplementation: "hardware", qualityLimitationReason: "cpu", frameWidth: 1280, frameHeight: 720},
        ), "outbound-rtp");
        expect(value).toMatchObject({codec: "video/AV1", implementation: "hardware", qualityLimitation: "cpu", width: 1280, height: 720});
    });

    it("does not replace the primary stream with auxiliary RTX stats", () => {
        const value = new ScreenStatsCollector().collect(report(
            {id: "vp9", type: "codec", mimeType: "video/VP9"},
            {id: "rtx-codec", type: "codec", mimeType: "video/rtx"},
            {id: "primary", type: "inbound-rtp", kind: "video", codecId: "vp9", framesDecoded: 50, frameWidth: 1920, frameHeight: 1080},
            {id: "repair", type: "inbound-rtp", kind: "video", codecId: "rtx-codec", packetsReceived: 3},
        ), "inbound-rtp");
        expect(value).toMatchObject({codec: "video/VP9", frames: 50, width: 1920, height: 1080});
    });
    it("calculates delta bitrate, fps and loss", () => {
        const collector = new ScreenStatsCollector();
        collector.collect(report({id: "v", type: "inbound-rtp", kind: "video", ssrc: 1, timestamp: 1000, bytesReceived: 1000, packetsReceived: 10, packetsLost: 1, framesDecoded: 5}), "inbound-rtp");
        const value = collector.collect(report({id: "v", type: "inbound-rtp", kind: "video", ssrc: 1, timestamp: 2000, bytesReceived: 251000, packetsReceived: 109, packetsLost: 2, framesDecoded: 35, frameWidth: 1920, frameHeight: 1080, jitter: .008}), "inbound-rtp");
        expect(value?.bitrate).toBe(2_000_000);
        expect(value?.fps).toBe(30);
        expect(value?.lossPercent).toBe(1);
        expect(value?.jitterMs).toBe(8);
    });

    it("resets baseline when SSRC changes", () => {
        const collector = new ScreenStatsCollector();
        collector.collect(report({id: "a", type: "inbound-rtp", kind: "video", ssrc: 1, timestamp: 1000, bytesReceived: 100}), "inbound-rtp");
        expect(collector.collect(report({id: "b", type: "inbound-rtp", kind: "video", ssrc: 2, timestamp: 2000, bytesReceived: 1000}), "inbound-rtp")?.bitrate).toBe(0);
    });

    it("uses safe defaults when optional browser stats are absent", () => {
        const value = new ScreenStatsCollector().collect(report({
            id: "v", type: "outbound-rtp", kind: "video", timestamp: 1000,
        }), "outbound-rtp");
        expect(value).toMatchObject({
            bitrate: 0, fps: 0, width: 0, height: 0, packetsLost: 0,
            lossSampleAvailable: false,
            frames: 0, framesDropped: 0, freezeCount: 0, freezeDurationMs: 0,
            keyFrames: 0, qualityLimitation: "",
        });
    });
});
