export type ScreenStats = {
    codec?: string;
    implementation?: string;
    selected?: string;
    bitrate: number;
    fps: number;
    width: number;
    height: number;
    lossPercent: number;
    jitterMs: number;
    rttMs: number;
    packetsLost: number;
    lossSampleAvailable: boolean;
    frames: number;
    framesDropped: number;
    freezeCount: number;
    freezeDurationMs: number;
    keyFrames: number;
    qualityLimitation: string;
};

type Baseline = {timestamp: number; bytes: number; packets: number; lost: number; frames: number};

export class ScreenStatsCollector {
    private baseline = new Map<string, Baseline>();

    reset() { this.baseline.clear(); }

    collect(report: RTCStatsReport, direction: "inbound-rtp" | "outbound-rtp"): ScreenStats | null {
        let media: any;
        let rttMs = 0;
        report.forEach((stat: any) => {
            if (stat.type === direction && stat.kind === "video") {
                const codec: any = stat.codecId ? report.get(stat.codecId) : undefined;
                if (!/\/(rtx|red|ulpfec|flexfec-03)$/i.test(codec?.mimeType ?? "")) media = stat;
            }
            if ((stat.type === "candidate-pair" && (stat.nominated || stat.selected)) || stat.type === "remote-inbound-rtp") {
                if (typeof stat.currentRoundTripTime === "number") rttMs = Math.max(rttMs, stat.currentRoundTripTime * 1000);
                if (typeof stat.roundTripTime === "number") rttMs = Math.max(rttMs, stat.roundTripTime * 1000);
            }
        });
        if (!media) return null;
        const key = `${direction}:${media.ssrc ?? media.id}`;
        const now: Baseline = {
            timestamp: media.timestamp ?? performance.now(),
            bytes: direction === "inbound-rtp" ? media.bytesReceived ?? 0 : media.bytesSent ?? 0,
            packets: direction === "inbound-rtp" ? media.packetsReceived ?? 0 : media.packetsSent ?? 0,
            lost: media.packetsLost ?? 0,
            frames: direction === "inbound-rtp" ? media.framesDecoded ?? 0 : media.framesEncoded ?? 0,
        };
        const previous = this.baseline.get(key);
        this.baseline.clear();
        this.baseline.set(key, now);
        const elapsed = previous ? (now.timestamp - previous.timestamp) / 1000 : 0;
        const packetDelta = previous ? Math.max(0, now.packets - previous.packets) : 0;
        const lostDelta = previous ? Math.max(0, now.lost - previous.lost) : 0;
        const codec: any = media.codecId ? report.get(media.codecId) : undefined;
        return {
            codec: codec?.mimeType ?? "",
            implementation: media.encoderImplementation ?? media.decoderImplementation ?? "",
            bitrate: elapsed > 0 ? Math.max(0, now.bytes - previous!.bytes) * 8 / elapsed : 0,
            fps: elapsed > 0 ? Math.max(0, now.frames - previous!.frames) / elapsed : 0,
            width: media.frameWidth ?? 0,
            height: media.frameHeight ?? 0,
            lossPercent: packetDelta + lostDelta > 0 ? lostDelta * 100 / (packetDelta + lostDelta) : 0,
            jitterMs: (media.jitter ?? 0) * 1000,
            rttMs,
            packetsLost: media.packetsLost ?? 0,
            lossSampleAvailable: Boolean(previous && packetDelta + lostDelta > 0),
            frames: now.frames,
            framesDropped: media.framesDropped ?? 0,
            freezeCount: media.freezeCount ?? 0,
            freezeDurationMs: (media.totalFreezesDuration ?? 0) * 1000,
            keyFrames: direction === "inbound-rtp" ? media.keyFramesDecoded ?? 0 : media.keyFramesEncoded ?? 0,
            qualityLimitation: media.qualityLimitationReason ?? "",
        };
    }
}
