import {desktopAPI, logDiagnostic} from "../../api";

// Stream sound source, captured natively in Go with WASAPI process loopback:
// all sound except Sonoryx, or one app. "off" is also used when the server or
// this Windows cannot carry sound, or no app is picked.
export type ScreenAudioSource = "off" | "system" | "app";
export type ScreenAudioChoice = {source: ScreenAudioSource; pid?: number};

const choiceKey = "sonoryx.screenAudio";
export function loadScreenAudioSource(): ScreenAudioSource {
    try {
        const value = localStorage.getItem(choiceKey);
        if (value === "off" || value === "app") return value;
    } catch { /* Default below. */ }
    return "system";
}
export function saveScreenAudioSource(value: ScreenAudioSource) {
    try { localStorage.setItem(choiceKey, value); } catch { /* Session choice survives. */ }
}

// A jitter buffer in the audio thread: starts after 60 ms of sound, outputs
// silence on underrun and skips ahead past 300 ms so latency stays bounded.
const workletSource = `
class ScreenAudioSink extends AudioWorkletProcessor {
    constructor() {
        super();
        this.size = 48000;
        this.left = new Float32Array(this.size);
        this.right = new Float32Array(this.size);
        this.read = 0; this.write = 0; this.count = 0; this.primed = false;
        this.port.onmessage = (event) => this.push(new Int16Array(event.data));
    }
    push(pcm) {
        const frames = pcm.length >> 1;
        for (let i = 0; i < frames; i++) {
            this.left[this.write] = pcm[2 * i] / 32768;
            this.right[this.write] = pcm[2 * i + 1] / 32768;
            this.write = (this.write + 1) % this.size;
            if (this.count < this.size) this.count++; else this.read = (this.read + 1) % this.size;
        }
        if (this.count > 14400) {
            const skip = this.count - 2880;
            this.read = (this.read + skip) % this.size;
            this.count -= skip;
        }
    }
    process(inputs, outputs) {
        const left = outputs[0][0], right = outputs[0][1] ?? left;
        if (!this.primed && this.count >= 2880) this.primed = true;
        for (let i = 0; i < left.length; i++) {
            if (this.primed && this.count > 0) {
                left[i] = this.left[this.read];
                right[i] = this.right[this.read];
                this.read = (this.read + 1) % this.size;
                this.count--;
            } else { left[i] = 0; right[i] = 0; }
        }
        if (this.count === 0) this.primed = false;
        return true;
    }
}
registerProcessor("screen-audio-sink", ScreenAudioSink);
`;

function decodeBase64(value: string): ArrayBuffer {
    const text = atob(value);
    const bytes = new Uint8Array(text.length);
    for (let i = 0; i < text.length; i++) bytes[i] = text.charCodeAt(i);
    return bytes.buffer;
}

export function closeAudioContext(context: AudioContext | undefined) {
    if (context && context.state !== "closed") void context.close().catch(() => undefined);
}

// Native stream sound: Go captures PCM, this page pulls it and turns it into
// a WebRTC track. The context must be created in the click call chain.
export class ScreenAudioBridge {
    private running = true;

    private constructor(private readonly id: string, private readonly context: AudioContext, private readonly node: AudioWorkletNode, readonly track: MediaStreamTrack) {}

    static async start(context: AudioContext, pid: number): Promise<ScreenAudioBridge> {
        const url = URL.createObjectURL(new Blob([workletSource], {type: "text/javascript"}));
        try { await context.audioWorklet.addModule(url); }
        finally { URL.revokeObjectURL(url); }
        const node = new AudioWorkletNode(context, "screen-audio-sink", {numberOfInputs: 0, numberOfOutputs: 1, outputChannelCount: [2]});
        const destination = context.createMediaStreamDestination();
        destination.channelCount = 2;
        node.connect(destination);
        const track = destination.stream.getAudioTracks()[0];
        track.contentHint = "music";
        let id: string;
        try { id = await desktopAPI.startScreenAudio(pid); }
        catch (error) { track.stop(); node.disconnect(); throw error; }
        await context.resume().catch(() => undefined);
        const bridge = new ScreenAudioBridge(id, context, node, track);
        void bridge.pump();
        return bridge;
    }

    private async pump() {
        while (this.running) {
            try {
                const data = await desktopAPI.readScreenAudio(this.id);
                if (!this.running) return;
                if (data) {
                    const buffer = decodeBase64(data);
                    this.node.port.postMessage(buffer, [buffer]);
                }
            } catch (error) {
                if (this.running) logDiagnostic("screen_audio_read", error);
                return;
            }
        }
    }

    stop() {
        if (!this.running) return;
        this.running = false;
        this.track.stop();
        this.node.disconnect();
        closeAudioContext(this.context);
        void desktopAPI.stopScreenAudio(this.id).catch((error) => logDiagnostic("screen_audio_stop", error));
    }
}

const appKey = "sonoryx.screenAudioApp";
export function loadScreenAudioApp(): string {
    try { return localStorage.getItem(appKey) ?? ""; } catch { return ""; }
}
export function saveScreenAudioApp(name: string) {
    try { localStorage.setItem(appKey, name); } catch { /* Session choice survives. */ }
}
