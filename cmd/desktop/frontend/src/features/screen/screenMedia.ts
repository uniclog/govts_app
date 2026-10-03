import {desktopAPI, logDiagnostic} from "../../api";
import {ScreenStatsCollector, type ScreenStats} from "./screenStats";
import {defaultScreenProfile, type ScreenProfile} from "./screenProfiles";

function waitForGathering(pc: RTCPeerConnection): Promise<void> {
    if (pc.iceGatheringState === "complete") return Promise.resolve();
    return new Promise((resolve, reject) => {
        const timeout = window.setTimeout(() => finish(new Error("Истекло время подготовки WebRTC offer")), 10_000);
        const changed = () => {
            if (pc.iceGatheringState === "complete") finish();
            else if (pc.connectionState === "closed") finish(new DOMException("Операция отменена", "AbortError"));
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

async function localOffer(pc: RTCPeerConnection): Promise<{type: string; sdp: string}> {
    await pc.setLocalDescription(await pc.createOffer());
    await waitForGathering(pc);
    if (!pc.localDescription?.sdp) throw new Error("Не удалось создать WebRTC offer");
    return {type: "offer", sdp: pc.localDescription.sdp};
}

function waitForConnected(pc: RTCPeerConnection, timeoutMs = 20_000): Promise<void> {
    if (pc.connectionState === "connected") return Promise.resolve();
    return new Promise((resolve, reject) => {
        const timeout = window.setTimeout(() => finish(new Error("Не удалось установить WebRTC media-соединение. Проверьте UDP-порты сервера и advertised IP")), timeoutMs);
        const changed = () => {
            if (pc.connectionState === "connected") finish();
            if (pc.connectionState === "failed" || pc.connectionState === "closed") {
                finish(new Error("WebRTC media-соединение не установлено. Проверьте UDP-порты сервера и advertised IP"));
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

function attachRemoteStream(pc: RTCPeerConnection, timeoutMs = 20_000): Promise<MediaStream> {
    const stream = new MediaStream();
    return new Promise((resolve, reject) => {
        let settled = false;
        const timeout = window.setTimeout(() => finish(new Error("Не удалось получить видеотрек демонстрации")), timeoutMs);
        const onTrack = (event: RTCTrackEvent) => {
            if (!stream.getTracks().some((track) => track.id === event.track.id)) stream.addTrack(event.track);
            if (event.track.kind === "video") finish();
        };
        const onConnectionStateChanged = () => {
            if (pc.connectionState === "failed" || pc.connectionState === "closed") {
                finish(new Error("WebRTC media-соединение закрылось до получения видеотрека"));
            }
        };
        const finish = (error?: Error) => {
            if (settled) return;
            if (error) {
                settled = true;
                window.clearTimeout(timeout);
                pc.removeEventListener("track", onTrack);
                pc.removeEventListener("connectionstatechange", onConnectionStateChanged);
                reject(error);
                return;
            }
            if (!stream.getVideoTracks().length) return;
            settled = true;
            window.clearTimeout(timeout);
            pc.removeEventListener("connectionstatechange", onConnectionStateChanged);
            resolve(stream);
        };
        pc.addEventListener("track", onTrack);
        pc.addEventListener("connectionstatechange", onConnectionStateChanged);
    });
}

type DisplayCaptureOptions = DisplayMediaStreamOptions & {
    systemAudio?: "include" | "exclude";
    windowAudio?: "system" | "window" | "exclude";
};

async function captureScreen(): Promise<MediaStream> {
    const cancelled = (error: unknown) => error instanceof DOMException && (error.name === "NotAllowedError" || error.name === "AbortError");
    try {
        return await navigator.mediaDevices.getDisplayMedia({video: true, audio: true, systemAudio: "include", windowAudio: "window"} as DisplayCaptureOptions);
    } catch (error) {
        if (cancelled(error)) throw error;
        logDiagnostic("screen_audio_constraints", error);
    }
    try {
        return await navigator.mediaDevices.getDisplayMedia({video: true, audio: true});
    } catch (error) {
        if (cancelled(error)) throw error;
        logDiagnostic("screen_audio_request", error);
    }
    return navigator.mediaDevices.getDisplayMedia({video: true, audio: false});
}

async function ensureTrusted(): Promise<void> {
    const identity = await desktopAPI.mediaServerIdentity();
    if (identity.trusted) return;
    if (identity.known) throw new Error(`Ключ media-сервера изменился. Новый отпечаток: ${identity.fingerprint}`);
    await desktopAPI.trustMediaServer(identity.fingerprint);
}

export class ScreenMediaController {
    private publisher?: {streamID: string; pc: RTCPeerConnection; capture: MediaStream};
    private publishAudioSender?: RTCRtpSender;
    private pendingPublisher?: {pc: RTCPeerConnection; capture: MediaStream};
    private viewer?: {streamID: string; subscriberID: string; pc: RTCPeerConnection};
    private publisherStats = new ScreenStatsCollector();
    private viewerStats = new ScreenStatsCollector();
    private publishGeneration = 0;
    private picking = false;

    async publish(onEnded: () => void, profile: ScreenProfile = defaultScreenProfile, isCurrent: () => boolean = () => true): Promise<string> {
        if (this.publisher) return this.publisher.streamID;
        if (this.picking || this.pendingPublisher) throw new Error("Демонстрация уже запускается");
        const generation = ++this.publishGeneration;
        const checkCurrent = () => {
            if (generation !== this.publishGeneration || !isCurrent()) throw new DOMException("Операция отменена", "AbortError");
        };
        // Keep the picker in the direct click call chain: some WebViews require user activation.
        this.picking = true;
        let capture: MediaStream;
        try {
            logDiagnostic("screen_picker_open", `generation=${generation}`);
            capture = await captureScreen();
            const surface = capture.getVideoTracks()[0]?.getSettings().displaySurface ?? "";
            logDiagnostic("screen_picker_selected", `generation=${generation} surface=${surface} audio=${capture.getAudioTracks().length}`);
        } catch (error) {
            logDiagnostic("screen_picker_closed", error);
            throw error;
        }
        finally { this.picking = false; }
        try { checkCurrent(); }
        catch (error) { capture.getTracks().forEach((track) => track.stop()); throw error; }
        const pc = new RTCPeerConnection({iceServers: []});
        const pending = {pc, capture};
        this.pendingPublisher = pending;
        let streamID = "";
        const missingVideoTrack = (): never => { throw new Error("Источник не предоставил видеотрек"); };
        const publishSuperseded = (): never => { throw new DOMException("Операция отменена", "AbortError"); };
        try {
            const captureTrack = capture.getVideoTracks()[0];
            if (!captureTrack) missingVideoTrack();
            const checkCapture = () => {
                if (captureTrack.readyState === "ended") throw new DOMException("Захват завершён", "AbortError");
            };
            captureTrack.onended = () => pc.close();
            captureTrack.contentHint = profile.contentHint;
            try {
                await captureTrack.applyConstraints({
                    width: {ideal: profile.width, max: profile.width},
                    height: {ideal: profile.height, max: profile.height},
                    frameRate: {min: profile.minFrameRate, ideal: profile.frameRate, max: profile.frameRate},
                });
            } catch (error) {
                logDiagnostic("screen_capture_constraints_fallback", error);
                // Some capture backends reject minimum frame-rate constraints.
                // Retain the preferred target without aborting screen sharing.
                await captureTrack.applyConstraints({
                    width: {ideal: profile.width, max: profile.width},
                    height: {ideal: profile.height, max: profile.height},
                    frameRate: {ideal: profile.frameRate, max: profile.frameRate},
                }).catch((error) => { logDiagnostic("screen_capture_constraints", error); });
            }
            checkCurrent();
            const transceiver = pc.addTransceiver(captureTrack, {
                direction: "sendonly",
                streams: [capture],
                sendEncodings: [{maxBitrate: profile.bitrate}],
            });
            const senderParameters = transceiver.sender.getParameters();
            senderParameters.degradationPreference = profile.degradationPreference;
            await transceiver.sender.setParameters(senderParameters).catch((error) => { logDiagnostic("screen_sender_parameters", error); });
            const audioTrack = capture.getAudioTracks()[0];
            if (audioTrack) {
                audioTrack.contentHint = "music";
                this.publishAudioSender = pc.addTransceiver(audioTrack, {direction: "sendonly", streams: [capture]}).sender;
            }
            await ensureTrusted();
            checkCurrent();
            const offer = await localOffer(pc);
            checkCurrent();
            checkCapture();
            const result = await desktopAPI.publishScreen(offer);
            streamID = result.streamId;
            checkCurrent();
            await pc.setRemoteDescription({type: "answer", sdp: result.answer.sdp});
            await waitForConnected(pc);
            checkCurrent();
            checkCapture();
            if (this.pendingPublisher !== pending) publishSuperseded();
            this.pendingPublisher = undefined;
            const active = {streamID, pc, capture};
            this.publisher = active;
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
                this.publishAudioSender = undefined;
                this.publisherStats.reset();
                capture.getTracks().forEach((track) => track.stop());
                pc.removeEventListener("connectionstatechange", connectionChanged);
                if (pc.connectionState !== "closed") pc.close();
                void desktopAPI.stopScreen(streamID).catch((error) => { logDiagnostic("screen_stop_cleanup", error); });
                onEnded();
            };
            pc.addEventListener("connectionstatechange", connectionChanged);
            captureTrack.onended = () => void this.stopPublishing().catch((error) => logDiagnostic("screen_stop_cleanup", error)).finally(onEnded);
            return result.streamId;
        } catch (error) {
            logDiagnostic("screen_publish_failed", error);
            this.publishAudioSender = undefined;
            if (this.pendingPublisher === pending) this.pendingPublisher = undefined;
            capture.getTracks().forEach((track) => track.stop());
            pc.close();
            if (streamID) await desktopAPI.stopScreen(streamID).catch((error) => { logDiagnostic("screen_publish_cleanup", error); });
            checkCurrent();
            throw error;
        }
    }

    async stopPublishing(): Promise<void> {
        ++this.publishGeneration;
        const active = this.publisher;
        const pending = this.pendingPublisher;
        this.publisher = undefined;
        this.pendingPublisher = undefined;
        this.publishAudioSender = undefined;
        this.publisherStats.reset();
        if (pending) {
            pending.capture.getTracks().forEach((track) => track.stop());
            pending.pc.close();
        }
        if (!active) return;
        active.capture.getTracks().forEach((track) => track.stop());
        active.pc.close();
        await desktopAPI.stopScreen(active.streamID);
    }

    hasPublishAudio(): boolean {
        return (this.publisher?.capture.getAudioTracks().length ?? 0) > 0;
    }

    setPublishAudioMuted(muted: boolean): void {
        const track = this.publisher?.capture.getAudioTracks()[0];
        const sender = this.publishAudioSender;
        if (!track || !sender) return;
        track.enabled = !muted;
        void sender.replaceTrack(muted ? null : track).catch((error) => logDiagnostic("screen_audio_mute", error));
    }

    async subscribe(streamID: string): Promise<MediaStream> {
        await this.unsubscribe();
        await ensureTrusted();
        const pc = new RTCPeerConnection({iceServers: []});
        pc.addEventListener("connectionstatechange", () => {
            logDiagnostic("screen_viewer_state", `stream=${streamID} connection=${pc.connectionState} ice=${pc.iceConnectionState}`);
        });
        // Install the listener before applying the answer: WebRTC dispatches `track`
        // from setRemoteDescription, before the signaling call below returns.
        const remoteVideo = attachRemoteStream(pc);
        void remoteVideo.catch(() => undefined);
        pc.addTransceiver("video", {direction: "recvonly"});
        pc.addTransceiver("audio", {direction: "recvonly"});
        const subscriberID = crypto.randomUUID();
        try {
            const result = await desktopAPI.subscribeScreen(streamID, subscriberID, await localOffer(pc));
            await pc.setRemoteDescription({type: "answer", sdp: result.answer.sdp});
            const remote = await remoteVideo;
            await waitForConnected(pc);
            this.viewer = {streamID, subscriberID, pc};
            this.viewerStats.reset();
            return remote;
        } catch (error) {
            logDiagnostic("screen_subscribe_failed", error);
            pc.close();
            throw error;
        }
    }

    async unsubscribe(): Promise<void> {
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
        ++this.publishGeneration;
        const pending = this.pendingPublisher;
        const publisher = this.publisher;
        const viewer = this.viewer;
        this.pendingPublisher = undefined;
        this.publisher = undefined;
        this.publishAudioSender = undefined;
        this.viewer = undefined;
        pending?.capture.getTracks().forEach((track) => track.stop());
        pending?.pc.close();
        publisher?.capture.getTracks().forEach((track) => track.stop());
        publisher?.pc.close();
        viewer?.pc.close();
        this.publisherStats.reset();
        this.viewerStats.reset();
    }

    async getViewerStats(): Promise<ScreenStats | null> {
        return this.viewer ? this.viewerStats.collect(await this.viewer.pc.getStats(), "inbound-rtp") : null;
    }

    async getPublisherStats(): Promise<ScreenStats | null> {
        return this.publisher ? this.publisherStats.collect(await this.publisher.pc.getStats(), "outbound-rtp") : null;
    }
}
