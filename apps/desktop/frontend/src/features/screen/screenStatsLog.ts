// Periodic diagnostic line for one direction of screen media. Counters are
// reported as deltas over the interval since the previous sample; the first
// sample only sets the baseline. A value the browser does not report, or a
// counter that went backwards, is written as "na" so it is not mistaken for 0.
type Direction = "inbound-rtp" | "outbound-rtp";

const counters: Record<Direction, string[]> = {
    "inbound-rtp": ["bytesReceived", "packetsReceived", "packetsLost", "framesDecoded", "framesDropped", "keyFramesDecoded",
        "freezeCount", "totalFreezesDuration", "nackCount", "pliCount", "firCount", "jitterBufferDelay", "jitterBufferEmittedCount"],
    "outbound-rtp": ["bytesSent", "packetsSent", "retransmittedPacketsSent", "retransmittedBytesSent", "framesEncoded",
        "keyFramesEncoded", "nackCount", "pliCount", "firCount", "totalEncodeTime"],
};

type Sample = {key: string; timestamp: number; values: Record<string, number | undefined>};

const num = (value: unknown) => typeof value === "number" && Number.isFinite(value) ? value : undefined;
const fmt = (value: number | undefined, digits = 0) => value === undefined ? "na" : value.toFixed(digits);

export class ScreenStatsLog {
    private previous?: Sample;

    reset() { this.previous = undefined; }

    sample(report: RTCStatsReport, direction: Direction): string | null {
        let media: any;
        let remote: any;
        let pair: any;
        let fallbackPair: any;
        report.forEach((stat: any) => {
            if (stat.type === direction && stat.kind === "video") {
                const codec: any = stat.codecId ? report.get(stat.codecId) : undefined;
                if (!/\/(rtx|red|ulpfec|flexfec-03)$/i.test(codec?.mimeType ?? "")) media = stat;
            }
            // The transport names the pair in use; several pairs can be nominated.
            if (stat.type === "transport" && stat.selectedCandidatePairId) pair = report.get(stat.selectedCandidatePairId);
            if (stat.type === "candidate-pair" && (stat.selected || (stat.nominated && stat.state === "succeeded"))) fallbackPair ??= stat;
        });
        pair ??= fallbackPair;
        if (!media) return null;
        if (direction === "outbound-rtp") {
            report.forEach((stat: any) => { if (stat.type === "remote-inbound-rtp" && stat.ssrc === media.ssrc) remote = stat; });
        }
        const current: Sample = {key: String(media.ssrc ?? media.id), timestamp: num(media.timestamp) ?? performance.now(), values: {}};
        for (const name of counters[direction]) current.values[name] = num(media[name]);
        const previous = this.previous?.key === current.key ? this.previous : undefined;
        this.previous = current;
        const elapsed = previous ? (current.timestamp - previous.timestamp) / 1000 : 0;
        const delta = (name: string) => {
            const now = current.values[name];
            const before = previous?.values[name];
            return elapsed > 0 && now !== undefined && before !== undefined && now >= before ? now - before : undefined;
        };
        const rate = (name: string, scale = 1) => {
            const value = delta(name);
            return value === undefined ? undefined : value * scale / elapsed;
        };
        const codec: any = media.codecId ? report.get(media.codecId) : undefined;
        const rttSeconds = num(remote?.roundTripTime) ?? num(pair?.currentRoundTripTime);
        const common = [
            `interval_s=${previous ? elapsed.toFixed(1) : "baseline"}`,
            `codec=${codec?.mimeType ?? "na"}`,
            `size=${num(media.frameWidth) ?? "na"}x${num(media.frameHeight) ?? "na"}`,
            `rtt_ms=${fmt(rttSeconds === undefined ? undefined : rttSeconds * 1000)}`,
            `nack=${fmt(delta("nackCount"))}`,
            `pli=${fmt(delta("pliCount"))}`,
            `fir=${fmt(delta("firCount"))}`,
        ];
        if (direction === "outbound-rtp") {
            const frames = delta("framesEncoded");
            const encodeTime = delta("totalEncodeTime");
            return [
                "dir=out", ...common,
                `encoder=${media.encoderImplementation ?? "na"}`,
                `power_efficient=${media.powerEfficientEncoder ?? "na"}`,
                `kbps=${fmt(rate("bytesSent", 8 / 1000))}`,
                `target_kbps=${fmt(num(media.targetBitrate) === undefined ? undefined : media.targetBitrate / 1000)}`,
                `fps=${fmt(rate("framesEncoded"), 1)}`,
                `keyframes=${fmt(delta("keyFramesEncoded"))}`,
                `packets=${fmt(delta("packetsSent"))}`,
                `retransmitted_packets=${fmt(delta("retransmittedPacketsSent"))}`,
                `encode_ms_per_frame=${fmt(frames && encodeTime !== undefined ? encodeTime * 1000 / frames : undefined, 1)}`,
                `quality_limitation=${media.qualityLimitationReason ?? "na"}`,
                `remote_fraction_lost=${fmt(num(remote?.fractionLost), 3)}`,
            ].join(" ");
        }
        const received = delta("packetsReceived");
        const lost = delta("packetsLost");
        const emitted = delta("jitterBufferEmittedCount");
        const bufferDelay = delta("jitterBufferDelay");
        const freezeSeconds = delta("totalFreezesDuration");
        return [
            "dir=in", ...common,
            `decoder=${media.decoderImplementation ?? "na"}`,
            `power_efficient=${media.powerEfficientDecoder ?? "na"}`,
            `kbps=${fmt(rate("bytesReceived", 8 / 1000))}`,
            `fps=${fmt(rate("framesDecoded"), 1)}`,
            `keyframes=${fmt(delta("keyFramesDecoded"))}`,
            `frames_dropped=${fmt(delta("framesDropped"))}`,
            `freezes=${fmt(delta("freezeCount"))}`,
            `freeze_ms=${fmt(freezeSeconds === undefined ? undefined : freezeSeconds * 1000)}`,
            `packets=${fmt(received)}`,
            `lost=${fmt(lost)}`,
            `loss_percent=${fmt(received !== undefined && lost !== undefined && received + lost > 0 ? lost * 100 / (received + lost) : undefined, 2)}`,
            `jitter_ms=${fmt(num(media.jitter) === undefined ? undefined : media.jitter * 1000, 1)}`,
            `jitter_buffer_ms=${fmt(emitted && bufferDelay !== undefined ? bufferDelay * 1000 / emitted : undefined, 1)}`,
        ].join(" ");
    }
}
