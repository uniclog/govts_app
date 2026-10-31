import {desktopAPI, logDiagnostic} from "../../api";
import {errorCode} from "../../locales";
import {ScreenStatsCollector, type ScreenStats} from "./screenStats";
import {ScreenStatsLog} from "./screenStatsLog";
import {defaultScreenSettings, profileFromSettings, screenSizes, type ScreenSettings} from "./screenProfiles";
import {closeAudioContext, ScreenAudioBridge, type ScreenAudioChoice} from "./screenAudio";
import {confirmMediaServer} from "./MediaTrustConfirm";
import {applyH264Formats, createH264Probe, h264CaptureFPS, h264Fits, h264Limits, matchingH264, receivingH264Formats, requiredH264Level, sendingH264Format, supportedH264Options, type H264Format} from "./screenH264";

type ServerCodecs = {legacy?: boolean; codecs?: {mimeType: string; sdpFmtpLine: string; payloadType: number}[]; audio?: {mimeType: string}[]};
export class ScreenCodecLimitError extends Error {
    constructor(public readonly level: number) {
        super("H.264 profile/level cannot share this capture at the selected resolution and FPS.");
    }
}
type PublisherState = {streamID: string; pc: RTCPeerConnection; capture: MediaStream; settings: ScreenSettings; releaseSound: () => void; cleanup?: () => void};
const fmtp = (line = "") => Object.fromEntries(line.split(";").map((part) => part.trim().split("=")));
export function senderH264Level(server?: ServerCodecs): number {
    if (typeof RTCRtpSender === "undefined" || server?.legacy) return 0;
    const local = RTCRtpSender.getCapabilities?.("video")?.codecs ?? [];
    let best = 0;
    for (const codec of local.filter((c) => /h264$/i.test(c.mimeType))) {
        const parameters = fmtp(codec.sdpFmtpLine);
        const level = parseInt(parameters["profile-level-id"]?.slice(4) ?? "0", 16);
        if (!h264Limits[level]) continue;
        if (!server) { best = Math.max(best, level); continue; }
        for (const remote of server.codecs ?? []) {
            const peer = fmtp(remote.sdpFmtpLine);
            if (!/h264$/i.test(remote.mimeType) || parameters["packetization-mode"] !== peer["packetization-mode"] || parameters["profile-level-id"]?.slice(0,4).toLowerCase() !== peer["profile-level-id"]?.slice(0,4).toLowerCase()) continue;
            const combined = Math.min(level, parseInt(peer["profile-level-id"]?.slice(4) ?? "0", 16));
            if (h264Limits[combined]) best = Math.max(best, combined);
        }
    }
    return best;
}
export function h264SettingsFit(settings: ScreenSettings, level: number): boolean | null {
    if (settings.resolution === "native") return level ? null : false;
    const [width, height] = screenSizes[settings.resolution];
    return h264Fits(level, {width, height}, settings.fps);
}
export function h264SupportedSettings(level: number): {resolution: ScreenSettings["resolution"]; fps: ScreenSettings["fps"]}[] {
    return (["720p", "1080p", "1440p", "2160p"] as const).flatMap((resolution) => {
        const fps = ([60,30,15,10] as const).find((fps) => h264SettingsFit({...defaultScreenSettings, resolution, fps}, level));
        return fps ? [{resolution, fps}] : [];
    });
}
export function senderCodecAvailable(name: string): boolean {
    if (typeof RTCRtpSender === "undefined") return false;
    return Boolean(RTCRtpSender.getCapabilities?.("video")?.codecs.some((c) => c.mimeType.toLowerCase() === `video/${name.toLowerCase()}`));
}
async function preferCodecs(transceiver: RTCRtpTransceiver, settings: ScreenSettings, server: ServerCodecs, actual: MediaTrackSettings, checkCurrent: () => void): Promise<H264Format[]> {
    const capabilities = RTCRtpSender.getCapabilities?.("video");
    if (!capabilities || !transceiver.setCodecPreferences) {
        if (settings.codec !== "auto" && settings.codec !== "VP8") throw new Error(errorCode("screen-codec-selection-unavailable"));
        return [];
    }
    const supported = server.legacy ? [{mimeType: "video/VP8", sdpFmtpLine: ""}] : server.codecs ?? [];
    const compatible = capabilities.codecs.filter((c) => supported.some((remote) => {
        if (c.mimeType.toLowerCase() !== remote.mimeType.toLowerCase()) return false;
        const local = fmtp(c.sdpFmtpLine), peer = fmtp(remote.sdpFmtpLine);
        if (/h264$/i.test(c.mimeType)) return matchingH264(c, remote);
        if (/vp9$/i.test(c.mimeType)) return (local["profile-id"] ?? "0") === (peer["profile-id"] ?? "0");
        if (/av1$/i.test(c.mimeType)) return (local.profile ?? "0") === (peer.profile ?? "0");
        return !/rtx$/i.test(c.mimeType);
    }));
    const selected = compatible.filter((c) => settings.codec === "auto" || c.mimeType.toLowerCase() === `video/${settings.codec.toLowerCase()}`);
    // Keep VP8 first in auto until quality and CPU comparisons are available.
    selected.sort((a,b) => Number(!/vp8$/i.test(a.mimeType))-Number(!/vp8$/i.test(b.mimeType)));
    if (!selected.length) throw new Error(errorCode("screen-codec-unavailable"));
    const formats: H264Format[] = [];
    const probe = createH264Probe(undefined, checkCurrent);
    const valid: typeof selected = [];
    for (const codec of selected) {
        if (!/h264$/i.test(codec.mimeType)) { valid.push(codec); continue; }
        for (const remote of supported) {
            const format = await sendingH264Format(codec, remote, actual.width ?? 0, actual.height ?? 0,
                h264CaptureFPS(actual, settings.fps), settings.bitrate, probe);
            checkCurrent();
            if (format) { formats.push(format); valid.push(codec); break; }
        }
    }
    if (!valid.length) {
        const options = await supportedH264Options(server, settings.bitrate, undefined, checkCurrent);
        checkCurrent();
        const verifiedLevel = Math.max(0, ...options.map((option) => {
            const [width, height] = screenSizes[option.resolution];
            return requiredH264Level(width, height, option.fps);
        }));
        throw new ScreenCodecLimitError(verifiedLevel);
    }
    checkCurrent();
    selected.splice(0, selected.length, ...valid);
    transceiver.setCodecPreferences([...selected, ...capabilities.codecs.filter((c) => /rtx$/i.test(c.mimeType))]);
    return formats;
}
function verifyNegotiatedCodec(sdp: string, settings: ScreenSettings, actual: MediaTrackSettings) {
    const video = sdp.split(/(?=^m=)/m).find((section) => section.startsWith("m=video "));
    const payload = video?.split(/\r?\n/)[0]?.split(" ")[3];
    const codec = video?.match(new RegExp(`a=rtpmap:${payload} ([^/]+)/`, "i"))?.[1];
    if (!codec || video?.startsWith("m=video 0 ")) throw new Error(errorCode("screen-codec-rejected"));
    if (settings.codec !== "auto" && codec.toLowerCase() !== settings.codec.toLowerCase()) throw new Error(`Requested ${settings.codec}, negotiated ${codec}. Choose a supported codec.`);
    const parameters = fmtp(video?.match(new RegExp(`a=fmtp:${payload} ([^\r\n]+)`, "i"))?.[1]);
    logDiagnostic("screen_codec_answer", JSON.stringify({codec, payload, parameters, requested: settings.codec,
        width: actual.width, height: actual.height, fps: h264CaptureFPS(actual, settings.fps)}));
    if (codec.toLowerCase() === "h264" && !h264Fits(parseInt(parameters["profile-level-id"]?.slice(4) ?? "0", 16), actual, settings.fps)) {
        throw new ScreenCodecLimitError(parseInt(parameters["profile-level-id"]?.slice(4) ?? "0", 16));
    }
    logDiagnostic("screen_codec_negotiated", codec);
}

function waitForGathering(pc: RTCPeerConnection): Promise<void> {
    if (pc.connectionState === "closed") return Promise.reject(new DOMException(errorCode("screen-aborted"), "AbortError"));
    if (pc.iceGatheringState === "complete") return Promise.resolve();
    return new Promise((resolve, reject) => {
        const timeout = window.setTimeout(() => finish(new Error(errorCode("screen-offer-timeout"))), 10_000);
        const changed = () => {
            if (pc.iceGatheringState === "complete") finish();
            else if (pc.connectionState === "closed") finish(new DOMException(errorCode("screen-aborted"), "AbortError"));
        };
        const finish = (error?: Error) => {
            window.clearTimeout(timeout);
            pc.removeEventListener("icegatheringstatechange", changed);
            pc.removeEventListener("connectionstatechange", changed);
            if (error) reject(error); else resolve();
        };
        pc.addEventListener("icegatheringstatechange", changed);
        pc.addEventListener("connectionstatechange", changed);
    });
}

async function localOffer(pc: RTCPeerConnection, formats: H264Format[] = []): Promise<{type: string; sdp: string}> {
    const offer = await pc.createOffer();
    if (formats.length && offer.sdp) offer.sdp = applyH264Formats(offer.sdp, formats);
    await pc.setLocalDescription(offer);
    if (formats.length) logDiagnostic("screen_h264_offer", JSON.stringify(formats));
    await waitForGathering(pc);
    if (!pc.localDescription?.sdp) throw new Error(errorCode("screen-offer-failed"));
    return {type: "offer", sdp: pc.localDescription.sdp};
}

function waitForConnected(pc: RTCPeerConnection, timeoutMs = 20_000): Promise<void> {
    if (pc.connectionState === "connected") return Promise.resolve();
    if (pc.connectionState === "closed" || pc.connectionState === "failed") return Promise.reject(new Error(errorCode("screen-connect-closed")));
    return new Promise((resolve, reject) => {
        const timeout = window.setTimeout(() => finish(new Error(errorCode("screen-connect-failed"))), timeoutMs);
        const changed = () => {
            if (pc.connectionState === "connected") finish();
            if (pc.connectionState === "failed" || pc.connectionState === "closed") {
                finish(new Error(errorCode("screen-connect-closed")));
            }
        };
        const finish = (error?: Error) => {
            window.clearTimeout(timeout);
            pc.removeEventListener("connectionstatechange", changed);
            if (error) reject(error); else resolve();
        };
        pc.addEventListener("connectionstatechange", changed);
    });
}

function waitForRemoteVideo(pc: RTCPeerConnection, timeoutMs = 20_000): Promise<MediaStream> {
    if (pc.connectionState === "closed" || pc.connectionState === "failed") return Promise.reject(new Error(errorCode("screen-video-closed")));
    return new Promise((resolve, reject) => {
        const timeout = window.setTimeout(() => finish(undefined, new Error(errorCode("screen-video-timeout"))), timeoutMs);
        const onTrack = (event: RTCTrackEvent) => {
            if (event.track.kind !== "video") return;
            finish(event.streams[0] ?? new MediaStream([event.track]));
        };
        const onConnectionStateChanged = () => {
            if (pc.connectionState === "failed" || pc.connectionState === "closed") {
                finish(undefined, new Error(errorCode("screen-video-closed")));
            }
        };
        const finish = (stream?: MediaStream, error?: Error) => {
            window.clearTimeout(timeout);
            pc.removeEventListener("track", onTrack);
            pc.removeEventListener("connectionstatechange", onConnectionStateChanged);
            if (error) reject(error);
            else if (stream) resolve(stream);
        };
        pc.addEventListener("track", onTrack);
        pc.addEventListener("connectionstatechange", onConnectionStateChanged);
    });
}

function peerClosed(pc: RTCPeerConnection): boolean {
    return pc.signalingState === "closed" || pc.connectionState === "closed";
}

let trustInFlight: Promise<void> | undefined;

// First use and changed keys both require explicit confirmation. The backend
// rechecks the displayed key, previous pin and session before saving it.
// Concurrent publish and subscribe calls share one confirmation.
function ensureTrusted(): Promise<void> {
    trustInFlight ??= (async () => {
        const identity = await desktopAPI.mediaServerIdentity();
        if (identity.trusted) return;
        if (!await confirmMediaServer(identity)) throw new DOMException(errorCode("screen-aborted"), "AbortError");
        await desktopAPI.confirmMediaServerIdentity(identity);
    })().finally(() => { trustInFlight = undefined; });
    return trustInFlight;
}

export class ScreenMediaController {
    private activePublisher?: PublisherState;
    private previewListeners = new Set<() => void>();
    private get publisher(): PublisherState | undefined { return this.activePublisher; }
    // Every start or end of publishing notifies the local preview.
    private set publisher(value: PublisherState | undefined) {
        if (value === this.activePublisher) return;
        this.activePublisher = value;
        this.previewListeners.forEach((listener) => listener());
    }
    private pendingPublisher?: {pc: RTCPeerConnection; capture: MediaStream; releaseSound: () => void};
    private viewerGeneration = 0;
    private pendingViewer?: RTCPeerConnection;
    private viewer?: {streamID: string; subscriberID: string; pc: RTCPeerConnection};
    private publisherStats = new ScreenStatsCollector();
    private viewerStats = new ScreenStatsCollector();
    // Separate from the UI collectors: they keep their own baselines.
    private publisherLog = new ScreenStatsLog();
    private viewerLog = new ScreenStatsLog();
    private diagnosticsTimer?: number;
    private publishGeneration = 0;
    private picking = false;
    private publishReady = false;

    publishingPrepared(): boolean { return this.publishReady; }

    async preparePublishing(): Promise<void> {
        const generation = this.publishGeneration;
        this.publishReady = false;
        await ensureTrusted();
        if (generation !== this.publishGeneration) throw new DOMException(errorCode("screen-aborted"), "AbortError");
        this.publishReady = true;
        logDiagnostic("screen_trust_prepared", `generation=${generation}`);
    }

    async publish(onEnded: () => void, settings: ScreenSettings = defaultScreenSettings, isCurrent: () => boolean = () => true, sound: ScreenAudioChoice = {source: "off"}): Promise<string> {
        if (this.publisher) return this.publisher.streamID;
        if (this.picking || this.pendingPublisher) throw new Error(errorCode("screen-already-starting"));
        // Preparation is a separate UI step, before the Start click. This keeps
        // capture behind trust approval and the picker in the user gesture.
        if (!this.publishReady) throw new Error(errorCode("media-key-confirmation-required"));
        settings = {...settings};
        const profile = profileFromSettings(settings);
        const generation = ++this.publishGeneration;
        const checkCurrent = () => {
            if (generation !== this.publishGeneration || !isCurrent()) throw new DOMException(errorCode("screen-aborted"), "AbortError");
        };
        // Keep the picker in the direct click call chain: some WebViews require user activation.
        // The native sound context is created in the same chain so it may start playing.
        // pid 0 means "system without Sonoryx" in Go, so "app" needs a real pid.
        if (sound.source === "app" && !sound.pid) sound = {source: "off"};
        const soundContext = sound.source !== "off" ? new AudioContext({sampleRate: 48000, latencyHint: "interactive"}) : undefined;
        let bridge: ScreenAudioBridge | undefined;
        const releaseSound = () => {
            if (bridge) bridge.stop();
            else closeAudioContext(soundContext);
        };
        this.picking = true;
        let capture: MediaStream;
        try {
            logDiagnostic("screen_picker_open", `generation=${generation} sound=${sound.source}`);
            // Picker hints only: open on the "Window" tab and leave out the page's
            // own surface. The user can still choose any screen or window.
            const options: DisplayMediaStreamOptions & {selfBrowserSurface?: "include" | "exclude"} = {video: {displaySurface: "window"}, audio: false, selfBrowserSurface: "exclude"};
            capture = await navigator.mediaDevices.getDisplayMedia(options);
            logDiagnostic("screen_picker_selected", `generation=${generation}`);
        } catch (error) {
            logDiagnostic("screen_picker_closed", error);
            releaseSound();
            throw error;
        }
        finally { this.picking = false; }
        const releaseCapture = () => { capture.getTracks().forEach((track) => track.stop()); releaseSound(); };
        try { checkCurrent(); }
        catch (error) { releaseCapture(); throw error; }
        let pc: RTCPeerConnection;
        try { pc = new RTCPeerConnection({iceServers: []}); }
        catch (error) { releaseCapture(); throw error; }
        const pending = {pc, capture, releaseSound};
        this.pendingPublisher = pending;
        const requireCaptureTrack = (track: MediaStreamTrack | undefined): MediaStreamTrack => {
            if (!track) throw new Error(errorCode("screen-no-track"));
            return track;
        };
        const ensureStillPending = () => {
            if (this.pendingPublisher !== pending) throw new DOMException(errorCode("screen-aborted"), "AbortError");
        };
        let streamID = "";
        try {
            const captureTrack = requireCaptureTrack(capture.getVideoTracks()[0]);
            const checkCapture = () => {
                if (captureTrack.readyState === "ended") throw new DOMException(errorCode("screen-capture-ended"), "AbortError");
            };
            let captureEnded = false;
            captureTrack.onended = () => { captureEnded = true; };
            const ensureCaptureOpen = () => {
                if (captureEnded || captureTrack.readyState === "ended" || pc.signalingState === "closed" || pc.connectionState === "closed") {
                    throw new DOMException(errorCode("screen-aborted"), "AbortError");
                }
            };
            captureTrack.contentHint = profile.contentHint;
            const source = captureTrack.getSettings();
            const scale = profile.width && source.width && source.height ? Math.min(1, profile.width/source.width, profile.height/source.height) : 1;
            const constraints: MediaTrackConstraints = {frameRate: {ideal: profile.frameRate, max: profile.frameRate}};
            if (profile.width) {
                constraints.width = {max: Math.floor((source.width ?? profile.width)*scale)};
                constraints.height = {max: Math.floor((source.height ?? profile.height)*scale)};
            }
            await captureTrack.applyConstraints(constraints).catch((error) => logDiagnostic("screen_capture_constraints_fallback", error));
            logDiagnostic("screen_capture_actual", JSON.stringify(captureTrack.getSettings()));
            checkCurrent();
            ensureCaptureOpen();
            let transceiver: RTCRtpTransceiver;
            try {
                transceiver = pc.addTransceiver(captureTrack, {
                    direction: "sendonly",
                    streams: [capture],
                    sendEncodings: [{maxBitrate: profile.bitrate, maxFramerate: profile.frameRate}],
                });
            } catch (error) {
                if (peerClosed(pc) || captureEnded || captureTrack.readyState === "ended") {
                    throw new DOMException(errorCode("screen-aborted"), "AbortError");
                }
                throw error;
            }
            const senderParameters = transceiver.sender.getParameters();
            senderParameters.degradationPreference = profile.degradationPreference;
            await transceiver.sender.setParameters(senderParameters).catch((error) => { logDiagnostic("screen_sender_parameters", error); });
            checkCurrent();
            const server = await desktopAPI.screenCapabilities();
            checkCurrent();
            const h264Formats = await preferCodecs(transceiver, settings, server, captureTrack.getSettings(), () => {
                checkCurrent();
                ensureCaptureOpen();
            });
            checkCurrent();
            // Sound is optional: a server without it or a failed capture leaves
            // the share video-only rather than failing it.
            let soundTrack: MediaStreamTrack | undefined;
            if (soundContext && !server.legacy && server.audio?.some((codec) => /opus$/i.test(codec.mimeType))) {
                try {
                    bridge = await ScreenAudioBridge.start(soundContext, sound.source === "app" ? sound.pid ?? 0 : 0);
                    soundTrack = bridge.track;
                } catch (error) { logDiagnostic("screen_audio_failed", error); }
                checkCurrent();
            } else if (sound.source !== "off") logDiagnostic("screen_audio_unsupported", "server has no stream sound");
            if (!bridge) closeAudioContext(soundContext); // not kept open for the whole share
            if (soundTrack) {
                pc.addTransceiver(soundTrack, {direction: "sendonly", streams: [capture], sendEncodings: [{maxBitrate: 128_000}]});
                logDiagnostic("screen_audio_added", sound.source);
            }
            const offer = await localOffer(pc, h264Formats);
            checkCurrent();
            checkCapture();
            const result = await desktopAPI.publishScreen(offer);
            streamID = result.streamId;
            checkCurrent();
            await pc.setRemoteDescription({type: "answer", sdp: result.answer.sdp});
            verifyNegotiatedCodec(result.answer.sdp, settings, captureTrack.getSettings());
            await waitForConnected(pc);
            checkCurrent();
            checkCapture();
            ensureStillPending();
            this.pendingPublisher = undefined;
            const active: PublisherState = {streamID, pc, capture, settings: {...settings}, releaseSound};
            this.publisher = active;
            this.publisherLog.reset();
            this.startDiagnostics();
            let disconnectedTimer: number | undefined;
            let disconnectExpired = false;
            const connectionChanged = () => {
                logDiagnostic("screen_publisher_state", `stream=${streamID} connection=${pc.connectionState} ice=${pc.iceConnectionState}`);
                window.clearTimeout(disconnectedTimer);
                disconnectedTimer = undefined;
                if (pc.connectionState === "disconnected" && !disconnectExpired) {
                    // A short ICE interruption can recover. Do not tear down a
                    // healthy capture unless the disconnect persists.
                    disconnectedTimer = window.setTimeout(() => {
                        disconnectExpired = true;
                        connectionChanged();
                    }, 5_000);
                    return;
                }
                if (pc.connectionState !== "failed" && pc.connectionState !== "closed" &&
                    !(disconnectExpired && pc.connectionState === "disconnected")) return;
                if (this.publisher !== active) return;
                this.publisher = undefined;
                this.publisherStats.reset();
                releaseCapture();
                active.cleanup?.();
                if (pc.connectionState !== "closed") pc.close();
                void desktopAPI.stopScreen(streamID).catch((error) => { logDiagnostic("screen_stop_cleanup", error); });
                onEnded();
            };
            active.cleanup = () => {
                window.clearTimeout(disconnectedTimer);
                pc.removeEventListener("connectionstatechange", connectionChanged);
                captureTrack.onended = null;
            };
            pc.addEventListener("connectionstatechange", connectionChanged);
            captureTrack.onended = () => void this.stopPublishing().catch((error) => logDiagnostic("screen_stop_cleanup", error)).finally(onEnded);
            return result.streamId;
        } catch (error) {
            logDiagnostic("screen_publish_failed", error);
            this.publishReady = false;
            if (this.pendingPublisher === pending) this.pendingPublisher = undefined;
            releaseCapture();
            pc.close();
            if (streamID) await desktopAPI.stopScreen(streamID).catch((error) => { logDiagnostic("screen_publish_cleanup", error); });
            checkCurrent();
            throw error;
        }
    }

    // The capture being published, for a local preview; null while not live.
    // It is the sent track itself, so the preview adds no capture or encoding.
    localPreview = (): MediaStream | null => this.activePublisher?.capture ?? null;

    onPreviewChanged = (listener: () => void): (() => void) => {
        this.previewListeners.add(listener);
        return () => { this.previewListeners.delete(listener); };
    };

    async stopPublishing(): Promise<void> {
        ++this.publishGeneration;
        this.publishReady = false;
        const active = this.publisher;
        const pending = this.pendingPublisher;
        this.publisher = undefined;
        this.pendingPublisher = undefined;
        this.publisherStats.reset();
        if (pending) {
            pending.capture.getTracks().forEach((track) => {
                track.onended = null;
                track.stop();
            });
            pending.releaseSound();
            if (pending.pc.signalingState !== "closed") pending.pc.close();
        }
        if (!active) return;
        active.cleanup?.();
        active.capture.getTracks().forEach((track) => {
            track.onended = null;
            track.stop();
        });
        active.releaseSound();
        if (active.pc.signalingState !== "closed") active.pc.close();
        await desktopAPI.stopScreen(active.streamID);
    }

    async subscribe(streamID: string): Promise<MediaStream> {
        const cleanup = this.unsubscribe();
        const generation = this.viewerGeneration;
        const checkCurrent = () => {
            if (generation !== this.viewerGeneration) throw new DOMException(errorCode("screen-aborted"), "AbortError");
        };
        await cleanup;
        checkCurrent();
        await ensureTrusted();
        checkCurrent();
        const pc = new RTCPeerConnection({iceServers: []});
        this.pendingViewer = pc;
        pc.addEventListener("connectionstatechange", () => {
            logDiagnostic("screen_viewer_state", `stream=${streamID} connection=${pc.connectionState} ice=${pc.iceConnectionState}`);
        });
        const subscriberID = crypto.randomUUID();
        try {
            const receiver = pc.addTransceiver("video", {direction: "recvonly"});
            const receiveCodecs = RTCRtpReceiver.getCapabilities?.("video")?.codecs;
            if (receiveCodecs && receiver.setCodecPreferences) receiver.setCodecPreferences(receiveCodecs);
            const server: ServerCodecs = await desktopAPI.screenCapabilities().catch((reason) => {
                logDiagnostic("screen_viewer_capabilities_failed", reason);
                return {};
            });
            checkCurrent();
            const h264Formats = await receivingH264Formats(receiveCodecs ?? [], server, checkCurrent);
            checkCurrent();
            // Stream sound shares the video's stream id, so it joins the returned
            // MediaStream. Servers or streams without sound leave it inactive.
            pc.addTransceiver("audio", {direction: "recvonly"});
            const offer = await localOffer(pc, h264Formats);
            checkCurrent();
            const result = await desktopAPI.subscribeScreen(streamID, subscriberID, offer);
            checkCurrent();
            // Start the video timeout only after signaling, but install the
            // listener before setRemoteDescription dispatches the track event.
            const remoteVideo = waitForRemoteVideo(pc);
            void remoteVideo.catch(() => undefined);
            await pc.setRemoteDescription({type: "answer", sdp: result.answer.sdp});
            const remote = await remoteVideo;
            checkCurrent();
            await waitForConnected(pc);
            if (generation !== this.viewerGeneration) throw new DOMException(errorCode("screen-aborted"), "AbortError");
            this.pendingViewer = undefined;
            this.viewer = {streamID, subscriberID, pc};
            this.viewerStats.reset();
            this.viewerLog.reset();
            this.startDiagnostics();
            return remote;
        } catch (error) {
            if (this.pendingViewer === pc) this.pendingViewer = undefined;
            logDiagnostic("screen_subscribe_failed", error);
            pc.close();
            await desktopAPI.unsubscribeScreen(streamID, subscriberID).catch((reason) => logDiagnostic("screen_subscribe_cleanup", reason));
            checkCurrent();
            throw error;
        }
    }

    async unsubscribe(): Promise<void> {
        ++this.viewerGeneration;
        this.pendingViewer?.close();
        this.pendingViewer = undefined;
        const active = this.viewer;
        this.viewer = undefined;
        this.viewerStats.reset();
        if (!active) return;
        active.pc.close();
        await desktopAPI.unsubscribeScreen(active.streamID, active.subscriberID);
    }

    async close(): Promise<void> {
        await Promise.allSettled([this.stopPublishing(), this.unsubscribe()]);
    }

    dispose(): void {
        ++this.viewerGeneration;
        this.pendingViewer?.close();
        this.pendingViewer = undefined;
        ++this.publishGeneration;
        const pending = this.pendingPublisher;
        const publisher = this.publisher;
        const viewer = this.viewer;
        this.pendingPublisher = undefined;
        this.publisher = undefined;
        this.viewer = undefined;
        pending?.capture.getTracks().forEach((track) => track.stop());
        pending?.releaseSound();
        pending?.pc.close();
        publisher?.capture.getTracks().forEach((track) => track.stop());
        publisher?.releaseSound();
        publisher?.cleanup?.();
        publisher?.pc.close();
        viewer?.pc.close();
        if (publisher) void desktopAPI.stopScreen(publisher.streamID).catch((error) => logDiagnostic("screen_dispose_cleanup", error));
        if (viewer) void desktopAPI.unsubscribeScreen(viewer.streamID, viewer.subscriberID).catch((error) => logDiagnostic("screen_dispose_cleanup", error));
        this.publisherStats.reset();
        this.viewerStats.reset();
        this.stopDiagnostics();
    }

    // One "screen_stats" line per active direction every 10 s, aligned with the
    // server's screen metrics interval. The timer stops when nothing is active.
    private startDiagnostics() {
        if (this.diagnosticsTimer !== undefined) return;
        this.diagnosticsTimer = window.setInterval(() => void this.logStats(), 10_000);
    }

    private stopDiagnostics() {
        window.clearInterval(this.diagnosticsTimer);
        this.diagnosticsTimer = undefined;
    }

    private async logStats() {
        const publisher = this.publisher;
        const viewer = this.viewer;
        if (!publisher && !viewer) {
            this.stopDiagnostics();
            return;
        }
        try {
            if (publisher) {
                const line = this.publisherLog.sample(await publisher.pc.getStats(), "outbound-rtp");
                const {resolution, fps, codec, bitrate} = publisher.settings;
                if (line && this.publisher === publisher) logDiagnostic("screen_stats", `stream=${publisher.streamID} selected=${resolution}/${fps}/${codec}/${bitrate / 1000}kbps ${line}`);
            }
            if (viewer) {
                const line = this.viewerLog.sample(await viewer.pc.getStats(), "inbound-rtp");
                if (line && this.viewer === viewer) logDiagnostic("screen_stats", `stream=${viewer.streamID} ${line}`);
            }
        } catch (error) {
            logDiagnostic("screen_stats_failed", error);
        }
    }

    async getViewerStats(): Promise<ScreenStats | null> {
        return this.viewer ? this.viewerStats.collect(await this.viewer.pc.getStats(), "inbound-rtp") : null;
    }

    async getPublisherStats(): Promise<ScreenStats | null> {
        const active = this.publisher;
        if (!active) return null;
        const stats = this.publisherStats.collect(await active.pc.getStats(), "outbound-rtp");
        return stats ? {...stats, selected: `${active.settings.resolution} / ${active.settings.fps} FPS / ${active.settings.codec}`} : null;
    }
}
