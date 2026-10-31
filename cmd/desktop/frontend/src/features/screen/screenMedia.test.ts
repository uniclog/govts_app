import {afterEach, beforeEach, describe, expect, it, vi} from "vitest";
import {desktopAPI} from "../../api";
import {ScreenMediaController} from "./screenMedia";
import {defaultScreenSettings} from "./screenProfiles";

vi.mock("../../api", () => ({
    logDiagnostic: vi.fn(),
    desktopAPI: {
        mediaServerIdentity: vi.fn(), trustMediaServer: vi.fn(), screenCapabilities: vi.fn(),
        publishScreen: vi.fn(), stopScreen: vi.fn(), subscribeScreen: vi.fn(), unsubscribeScreen: vi.fn(),
    },
}));

const answerSDP = "v=0\r\nm=video 9 UDP/TLS/RTP/SAVPF 96\r\na=rtpmap:96 VP8/90000\r\n";
let offerSDP = answerSDP;
const pcs: FakePC[] = [];
const preferences = vi.fn();
let constructorError = false;
let addTransceiverError = false;
let initialConnectionState = "connected";
let closeEmitsEvent = true;
let sendEncodings: RTCRtpEncodingParameters[] | undefined;
const track = {
    kind: "video", readyState: "live", contentHint: "", onended: null as (() => void) | null,
    stop: vi.fn(), applyConstraints: vi.fn(),
    getSettings: vi.fn(() => ({width: 1920, height: 1080, frameRate: 15})),
};
const capture = {getTracks: () => [track], getVideoTracks: () => [track]};
const sender = {getParameters: () => ({encodings: [{}]}), setParameters: vi.fn(async () => undefined)};
const getDisplayMedia = vi.fn();

class FakePC extends EventTarget {
    connectionState = initialConnectionState;
    iceGatheringState = "complete";
    iceConnectionState = "connected";
    localDescription = {type: "offer", sdp: offerSDP};
    constructor() {
        super();
        if (constructorError) throw new Error("PeerConnection allocation failed");
        pcs.push(this);
    }
    addTransceiver(_track?: unknown, options?: RTCRtpTransceiverInit) {
        if (addTransceiverError) throw new Error("unsupported receiver preferences");
        sendEncodings = options?.sendEncodings;
        return {sender, setCodecPreferences: preferences};
    }
    async createOffer() { return this.localDescription; }
    async setLocalDescription(offer: typeof this.localDescription) { this.localDescription = offer; }
    async setRemoteDescription() {
        const event = new Event("track");
        Object.assign(event, {track, streams: [capture]});
        this.dispatchEvent(event);
    }
    close = vi.fn(() => { this.connectionState = "closed"; if (closeEmitsEvent) this.dispatchEvent(new Event("connectionstatechange")); });
}

function deferred<T>() {
    let resolve!: (value: T) => void;
    const promise = new Promise<T>((done) => { resolve = done; });
    return {promise, resolve};
}

beforeEach(() => {
    vi.useFakeTimers(); vi.clearAllMocks();
    pcs.length = 0; constructorError = false; addTransceiverError = false; initialConnectionState = "connected";
    closeEmitsEvent = true;
    sendEncodings = undefined; offerSDP = answerSDP;
    track.readyState = "live"; track.onended = null;
    track.applyConstraints.mockResolvedValue(undefined);
    track.getSettings.mockReturnValue({width: 1920, height: 1080, frameRate: 15});
    getDisplayMedia.mockResolvedValue(capture);
    vi.stubGlobal("window", {setTimeout: globalThis.setTimeout, clearTimeout: globalThis.clearTimeout, setInterval: globalThis.setInterval, clearInterval: globalThis.clearInterval});
    vi.stubGlobal("navigator", {mediaDevices: {getDisplayMedia}});
    vi.stubGlobal("RTCPeerConnection", FakePC);
    const getCapabilities = () => ({codecs: [{mimeType: "video/VP8", clockRate: 90000}, {mimeType: "video/VP9", clockRate: 90000, sdpFmtpLine: "profile-id=0"}]});
    vi.stubGlobal("RTCRtpSender", {getCapabilities});
    vi.stubGlobal("RTCRtpReceiver", {getCapabilities});
    vi.mocked(desktopAPI.mediaServerIdentity).mockResolvedValue({trusted: true} as any);
    vi.mocked(desktopAPI.screenCapabilities).mockResolvedValue({legacy: true});
    vi.mocked(desktopAPI.publishScreen).mockResolvedValue({streamId: "1", answer: {type: "answer", sdp: answerSDP}} as any);
    vi.mocked(desktopAPI.subscribeScreen).mockResolvedValue({answer: {type: "answer", sdp: answerSDP}} as any);
    vi.mocked(desktopAPI.stopScreen).mockResolvedValue(undefined);
    vi.mocked(desktopAPI.unsubscribeScreen).mockResolvedValue(undefined);
});
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

describe("screen publisher lifecycle", () => {
    let controller: ScreenMediaController;
    beforeEach(async () => {
        controller = new ScreenMediaController();
        await controller.preparePublishing();
        vi.mocked(desktopAPI.mediaServerIdentity).mockClear();
    });

    it("does not capture without explicit preparation", async () => {
        await expect(new ScreenMediaController().publish(vi.fn())).rejects.toThrow("media-key-confirmation-required");
        expect(getDisplayMedia).not.toHaveBeenCalled();
        expect(desktopAPI.publishScreen).not.toHaveBeenCalled();
    });

    it("opens the picker before the first asynchronous bridge call", async () => {
        const publishing = controller.publish(vi.fn());
        expect(getDisplayMedia).toHaveBeenCalledOnce();
        expect(desktopAPI.mediaServerIdentity).not.toHaveBeenCalled();
        await publishing;
        expect(preferences.mock.calls[0][0].map((c: any) => c.mimeType)).toEqual(["video/VP8"]);
        await controller.close();
    });

    it("stops capture if creating the PeerConnection throws", async () => {
        constructorError = true;
        await expect(controller.publish(vi.fn())).rejects.toThrow("PeerConnection allocation failed");
        expect(track.stop).toHaveBeenCalledOnce();
    });

    it("does not silently replace an unsupported explicit codec on an old server", async () => {
        await expect(controller.publish(vi.fn(), {...defaultScreenSettings, codec: "VP9"})).rejects.toThrow(/unavailable/);
        expect(desktopAPI.publishScreen).not.toHaveBeenCalled();
        expect(track.stop).toHaveBeenCalledOnce();
        expect(pcs[0].close).toHaveBeenCalledOnce();
    });

    it("cleans up an allocated server stream when the answer rejects its requested codec", async () => {
        vi.mocked(desktopAPI.screenCapabilities).mockResolvedValue({codecs: [{mimeType: "video/VP9", sdpFmtpLine: "profile-id=0", payloadType: 98}]});
        await expect(controller.publish(vi.fn(), {...defaultScreenSettings, codec: "VP9"})).rejects.toThrow(/negotiated VP8/);
        expect(desktopAPI.stopScreen).toHaveBeenCalledWith("1");
        expect(track.stop).toHaveBeenCalledOnce();
    });

    it("releases capture selected after cancellation", async () => {
        const selected = deferred<any>();
        getDisplayMedia.mockReturnValueOnce(selected.promise);
        const publishing = controller.publish(vi.fn());
        const assertion = expect(publishing).rejects.toMatchObject({name: "AbortError"});
        await controller.stopPublishing();
        selected.resolve(capture);
        await assertion;
        expect(track.stop).toHaveBeenCalledOnce();
        expect(pcs).toHaveLength(0);
    });

    it("checks the server H.264 level as well as the browser level", async () => {
        vi.stubGlobal("RTCRtpSender", {getCapabilities: () => ({codecs: [{mimeType: "video/H264", clockRate: 90000, sdpFmtpLine: "packetization-mode=1;profile-level-id=42e034"}]})});
        vi.mocked(desktopAPI.screenCapabilities).mockResolvedValue({codecs: [{mimeType: "video/H264", sdpFmtpLine: "packetization-mode=1;profile-level-id=42e01f", payloadType: 102}]});
        await expect(controller.publish(vi.fn(), {...defaultScreenSettings, codec: "H264"})).rejects.toThrow(/profile\/level/);
        expect(desktopAPI.publishScreen).not.toHaveBeenCalled();
    });

    it("does not negotiate a different AV1 profile by matching only MIME", async () => {
        vi.stubGlobal("RTCRtpSender", {getCapabilities: () => ({codecs: [{mimeType: "video/AV1", clockRate: 90000, sdpFmtpLine: "profile=0"}]})});
        vi.mocked(desktopAPI.screenCapabilities).mockResolvedValue({codecs: [{mimeType: "video/AV1", sdpFmtpLine: "profile=1", payloadType: 104}]});
        await expect(controller.publish(vi.fn(), {...defaultScreenSettings, codec: "AV1"})).rejects.toThrow(/unavailable/);
        expect(desktopAPI.publishScreen).not.toHaveBeenCalled();
    });

    it("sets an encoder FPS ceiling when the capture backend rejects constraints", async () => {
        track.applyConstraints.mockRejectedValueOnce(new Error("capture backend refuses constraints"));
        await controller.publish(vi.fn(), {...defaultScreenSettings, fps: 10});
        expect(sendEncodings).toEqual([{maxBitrate: 2_000_000, maxFramerate: 10}]);
        await controller.close();
    });

    it("cancels the disconnect timer even if browser close emits no event", async () => {
        await controller.publish(vi.fn());
        const publishingTimers = vi.getTimerCount(); // includes the stats diagnostics interval
        pcs[0].connectionState = "disconnected";
        pcs[0].dispatchEvent(new Event("connectionstatechange"));
        expect(vi.getTimerCount()).toBe(publishingTimers + 1);
        closeEmitsEvent = false;
        await controller.stopPublishing();
        expect(vi.getTimerCount()).toBe(publishingTimers); // the disconnect timer is gone at once
        await vi.advanceTimersByTimeAsync(10_000); // the idle stats interval stops on its next tick
        expect(vi.getTimerCount()).toBe(0);
    });
});

describe("screen viewer lifecycle", () => {
    it("cancels even when disposed before the first awaited cleanup resumes", async () => {
        const controller = new ScreenMediaController();
        const subscribing = controller.subscribe("1");
        const assertion = expect(subscribing).rejects.toMatchObject({name: "AbortError"});
        controller.dispose();
        await assertion;
        expect(pcs).toHaveLength(0);
        expect(desktopAPI.subscribeScreen).not.toHaveBeenCalled();
    });

    it("closes pending connections and timers when receiver setup throws", async () => {
        addTransceiverError = true;
        await expect(new ScreenMediaController().subscribe("1")).rejects.toThrow("unsupported receiver preferences");
        expect(pcs[0].close).toHaveBeenCalledOnce();
        expect(vi.getTimerCount()).toBe(0);
    });

    it("handles a closed connection immediately rather than waiting for timeout", async () => {
        initialConnectionState = "closed";
        const subscribing = new ScreenMediaController().subscribe("1");
        const assertion = expect(subscribing).rejects.toThrow();
        await vi.advanceTimersByTimeAsync(1);
        expect(vi.getTimerCount()).toBe(0);
        await assertion;
    });

    it("removes a server subscription when signaling fails", async () => {
        vi.mocked(desktopAPI.subscribeScreen).mockRejectedValueOnce(new Error("SDP mismatch"));
        await expect(new ScreenMediaController().subscribe("1")).rejects.toThrow("SDP mismatch");
        expect(desktopAPI.unsubscribeScreen).toHaveBeenCalledWith("1", expect.any(String));
        expect(pcs[0].close).toHaveBeenCalledOnce();
        expect(vi.getTimerCount()).toBe(0);
    });
});
