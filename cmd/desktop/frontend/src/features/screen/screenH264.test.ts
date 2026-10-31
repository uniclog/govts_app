import {afterEach, beforeEach, describe, expect, it, vi} from "vitest";
import {applyH264Formats, createH264Probe, h264CaptureFPS, h264Fits, receivingH264Formats, requiredH264Level, sendingH264Format, supportedH264Options} from "./screenH264";

const format = (id: string, asymmetry = 1) => `level-asymmetry-allowed=${asymmetry};packetization-mode=1;profile-level-id=${id}`;
const codec = (id: string, asymmetry = 1) => ({mimeType: "video/H264", clockRate: 90000, sdpFmtpLine: format(id, asymmetry)});
const local = codec("42e01f"), remote = codec("42e034");
const encodingInfo = vi.fn(), decodingInfo = vi.fn();
const supported = {supported: true, smooth: true, powerEfficient: false};

beforeEach(() => {
    vi.useFakeTimers();
    vi.resetAllMocks();
    encodingInfo.mockResolvedValue(supported);
    decodingInfo.mockResolvedValue(supported);
    vi.stubGlobal("navigator", {mediaCapabilities: {encodingInfo, decodingInfo}});
    vi.stubGlobal("RTCRtpSender", {getCapabilities: () => ({codecs: [local]})});
});
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); });

describe("H.264 capability and level negotiation", () => {
    it("permits 1080p60 above advertised Level 3.1 only with a positive encoder query", async () => {
        const result = await sendingH264Format(local, remote, 1920, 1080, 60, 6_000_000);
        expect(result).toEqual({original: format("42e01f"), negotiated: format("42e02a")});
        expect(encodingInfo).toHaveBeenCalledWith({type: "webrtc", video: {
            contentType: `video/H264;${format("42e02a")}`, width: 1920, height: 1080, framerate: 60, bitrate: 6_000_000,
        }});
        expect(local.sdpFmtpLine).toBe(format("42e01f"));
    });

    it("rejects an explicit negative encoder result", async () => {
        encodingInfo.mockResolvedValue({...supported, supported: false});
        expect(await sendingH264Format(local, remote, 1920, 1080, 60, 6_000_000)).toBeNull();
    });

    it.each([0, 1])("requires both peers to allow asymmetry (peer %i)", async (side) => {
        expect(await sendingH264Format(side ? local : codec("42e01f", 0), side ? codec("42e034", 0) : remote,
            1920, 1080, 30, 6_000_000)).toBeNull();
        expect(encodingInfo).not.toHaveBeenCalled();
    });

    it("does not exceed server limits or match a different profile or packetization mode", async () => {
        for (const peer of [codec("42e01f"), codec("640034"), {...remote, sdpFmtpLine: format("42e034").replace("mode=1", "mode=0")}]) {
            expect(await sendingH264Format(local, peer, 1920, 1080, 60, 6_000_000)).toBeNull();
        }
        expect(encodingInfo).not.toHaveBeenCalled();
    });

    it.each(["missing", "rejected"])("falls back conservatively when the query is %s", async (reason) => {
        if (reason === "missing") vi.stubGlobal("navigator", {});
        else encodingInfo.mockRejectedValue(new TypeError("webrtc unsupported"));
        expect(await sendingH264Format(local, remote, 1280, 720, 30, 2_000_000)).not.toBeNull();
        expect(await sendingH264Format(local, remote, 1920, 1080, 30, 2_000_000)).toBeNull();
    });

    it("bounds a hung query and stops issuing queries in the same probe", async () => {
        encodingInfo.mockReturnValue(new Promise(() => undefined));
        const probe = createH264Probe();
        const pending = sendingH264Format(local, remote, 1920, 1080, 60, 6_000_000, probe);
        await vi.advanceTimersByTimeAsync(1_500);
        expect(await pending).toBeNull();
        expect(await sendingH264Format(local, remote, 1280, 720, 30, 2_000_000, probe)).not.toBeNull();
        expect(encodingInfo).toHaveBeenCalledOnce();
        expect(vi.getTimerCount()).toBe(0);
    });

    it("aborts a pending options query immediately and removes its timer", async () => {
        encodingInfo.mockReturnValue(new Promise(() => undefined));
        const abort = new AbortController();
        const pending = supportedH264Options({codecs: [remote]}, 6_000_000, abort.signal);
        const rejected = expect(pending).rejects.toMatchObject({name: "AbortError"});
        abort.abort();
        await rejected;
        expect(encodingInfo).toHaveBeenCalledOnce();
        expect(vi.getTimerCount()).toBe(0);
    });

    it("bounds the total options-query budget, even when individual calls respond", async () => {
        encodingInfo.mockImplementation(() => new Promise((resolve) => setTimeout(() => resolve(supported), 1_000)));
        const pending = supportedH264Options({codecs: [remote]}, 6_000_000);
        await vi.advanceTimersByTimeAsync(6_000);
        const options = await pending;
        expect(encodingInfo.mock.calls.length).toBeLessThanOrEqual(5);
        expect(options).toContainEqual({resolution: "720p", fps: 30});
        expect(options).not.toContainEqual({resolution: "2160p", fps: 60});
        expect(vi.getTimerCount()).toBe(0);
    });

    it("keeps exact supported FPS choices, including lower FPS, rather than inferring them", async () => {
        encodingInfo.mockImplementation(async ({video}) => ({...supported,
            supported: video.width <= 1920 && video.framerate !== 30}));
        const options = await supportedH264Options({codecs: [remote]}, 4_000_000);
        expect(options).toContainEqual({resolution: "1080p", fps: 60});
        expect(options).toContainEqual({resolution: "1080p", fps: 15});
        expect(options).not.toContainEqual({resolution: "1080p", fps: 30});
        expect(options.some((option) => option.resolution === "2160p")).toBe(false);
        expect(vi.getTimerCount()).toBe(0);
    });

    it("advertises only the highest receive level positively accepted by the decoder", async () => {
        decodingInfo.mockImplementation(async ({video}) => ({...supported, supported: video.width <= 1920}));
        expect(await receivingH264Formats([local], {codecs: [remote]})).toEqual([
            {original: format("42e01f"), negotiated: format("42e02a")},
        ]);
        expect(encodingInfo).not.toHaveBeenCalled();
    });

    it("leaves receive capabilities unchanged when decoding support is unknown", async () => {
        decodingInfo.mockRejectedValue(new TypeError("unsupported"));
        expect(await receivingH264Formats([local], {codecs: [remote]})).toEqual([]);
    });

    it("handles macroblock rounding and does not treat zero capture FPS as zero load", () => {
        expect(requiredH264Level(1920, 1080, 30)).toBe(40);
        expect(requiredH264Level(1920, 1080, 60)).toBe(42);
        expect(requiredH264Level(3840, 2160, 60)).toBe(52);
        expect(requiredH264Level(1280, 721, 30)).toBe(32);
        expect(h264CaptureFPS({frameRate: 0}, 60)).toBe(60);
        expect(h264Fits(31, {width: 1280, height: 720, frameRate: 0}, 60)).toBe(false);
    });

    it("changes only matching H.264 video levels, preserving audio, RTX, and extensions", () => {
        const sdp = ["v=0", "a=ice-ufrag:am=marker", "m=audio 9 UDP/TLS/RTP/SAVPF 111", "a=fmtp:111 profile-level-id=42e01f",
            "m=video 9 UDP/TLS/RTP/SAVPF 102 103 104", "a=rtpmap:102 H264/90000", "a=rtpmap:103 rtx/90000",
            "a=rtpmap:104 H264/90000", `a=fmtp:102 x-profile-level-id=42e01f;${format("42e01f")};x-google-start-bitrate=2000`,
            "a=fmtp:103 apt=102", `a=fmtp:104 ${format("64001f")}`, ""].join("\r\n");
        const changed = applyH264Formats(sdp, [{original: format("42e01f"), negotiated: format("42e02a")}]);
        expect(changed).toBe(sdp.replace(`;${format("42e01f")};`, `;${format("42e02a")};`));
    });
});
