import {screenSizes, type ScreenSettings} from "./screenProfiles";

type Codec = {mimeType: string; sdpFmtpLine?: string};
type Server = {legacy?: boolean; codecs?: Codec[]};
export type H264Format = {original: string; negotiated: string};
export type H264Option = {resolution: ScreenSettings["resolution"]; fps: ScreenSettings["fps"]};
type Probe = {deadline: number; timedOut: boolean; signal?: AbortSignal; checkCurrent?: () => void};
export function createH264Probe(signal?: AbortSignal, checkCurrent?: () => void): Probe {
    return {deadline: performance.now() + 5_000, timedOut: false, signal, checkCurrent};
}
function checkProbe(probe: Probe): void {
    if (probe.signal?.aborted) throw new DOMException("H.264 capability query cancelled", "AbortError");
    probe.checkCurrent?.();
}
export const h264Limits: Record<number, [number, number]> = {
    31: [3600, 108000], 32: [5120, 216000], 40: [8192, 245760], 41: [8192, 245760],
    42: [8704, 522240], 50: [22080, 589824], 51: [36864, 983040], 52: [36864, 2073600],
};
export function h264CaptureFPS(actual: MediaTrackSettings, fps: number): number {
    return Math.min(fps, actual.frameRate && actual.frameRate > 0 ? actual.frameRate : fps);
}
export function h264Fits(level: number, actual: MediaTrackSettings, fps: number): boolean {
    const limit = h264Limits[level];
    if (!limit || !actual.width || !actual.height) return false;
    const blocks = Math.ceil(actual.width / 16) * Math.ceil(actual.height / 16);
    return blocks <= limit[0] && blocks * h264CaptureFPS(actual, fps) <= limit[1];
}
const parameters = (line = "") => Object.fromEntries(line.split(";").map((part) => {
    const [key, value] = part.split("=", 2);
    return [key.trim().toLowerCase(), value?.trim().toLowerCase()];
}));
const level = (line = "") => parseInt(parameters(line)["profile-level-id"]?.slice(4) ?? "0", 16);
export function matchingH264(a: Codec, b: Codec): boolean {
    const x = parameters(a.sdpFmtpLine), y = parameters(b.sdpFmtpLine);
    return /h264$/i.test(a.mimeType) && /h264$/i.test(b.mimeType) &&
        /^[0-9a-f]{6}$/.test(x["profile-level-id"] ?? "") && /^[0-9a-f]{6}$/.test(y["profile-level-id"] ?? "") &&
        x["profile-level-id"].slice(0, 4) === y["profile-level-id"].slice(0, 4) &&
        (x["packetization-mode"] ?? "0") === (y["packetization-mode"] ?? "0");
}
function withLevel(line: string, target: number): string {
    return line.replace(/(^|;\s*)(profile-level-id\s*=\s*[0-9a-f]{4})[0-9a-f]{2}/i,
        (_, separator: string, prefix: string) => separator + prefix + target.toString(16).padStart(2, "0"));
}
export function requiredH264Level(width: number, height: number, fps: number): number {
    if (!(width > 0 && height > 0 && fps > 0)) return 0;
    const blocks = Math.ceil(width / 16) * Math.ceil(height / 16);
    return Number(Object.keys(h264Limits).find((key) => blocks <= h264Limits[Number(key)][0] && blocks * fps <= h264Limits[Number(key)][1]) ?? 0);
}
async function capability(format: string, width: number, height: number, fps: number, bitrate: number, receive: boolean, probe: Probe): Promise<boolean | null> {
    checkProbe(probe);
    const api = navigator.mediaCapabilities;
    const remaining = probe.deadline - performance.now();
    if (probe.timedOut || remaining <= 0 || !api || !(receive ? api.decodingInfo : api.encodingInfo)) return null;
    const video = {contentType: `video/H264;${format}`, width, height, framerate: fps, bitrate};
    let timer: ReturnType<typeof setTimeout> | undefined;
    let cancelled: (() => void) | undefined;
    let supported: boolean | null = null;
    try {
        const interrupted = new Promise<null>((resolve) => {
            timer = setTimeout(() => { probe.timedOut = true; resolve(null); }, Math.min(1_500, remaining));
            cancelled = () => resolve(null);
            probe.signal?.addEventListener("abort", cancelled, {once: true});
        });
        const result = await Promise.race([receive ? api.decodingInfo({type: "webrtc", video}) : api.encodingInfo({type: "webrtc", video}), interrupted]);
        supported = result?.supported ?? null;
    } catch { /* Older WebViews may not implement the WebRTC query. */ }
    finally {
        clearTimeout(timer);
        if (cancelled) probe.signal?.removeEventListener("abort", cancelled);
    }
    checkProbe(probe);
    return supported;
}
export async function sendingH264Format(local: Codec, peer: Codec, width: number, height: number, fps: number, bitrate: number, probe = createH264Probe()): Promise<H264Format | null> {
    checkProbe(probe);
    if (!matchingH264(local, peer)) return null;
    const required = requiredH264Level(width, height, fps);
    const own = level(local.sdpFmtpLine), remote = level(peer.sdpFmtpLine);
    if (!required || !h264Limits[own] || !h264Limits[remote] || required > remote) return null;
    const asymmetric = parameters(local.sdpFmtpLine)["level-asymmetry-allowed"] === "1" && parameters(peer.sdpFmtpLine)["level-asymmetry-allowed"] === "1";
    if (required > own && !asymmetric) return null;
    const original = local.sdpFmtpLine ?? "";
    const negotiated = withLevel(original, required);
    const supported = await capability(negotiated, width, height, fps, bitrate, false, probe);
    if (supported === false || (supported === null && required > own)) return null;
    return {original, negotiated};
}
export async function supportedH264Options(server: Server, bitrate: number, signal?: AbortSignal, checkCurrent?: () => void): Promise<H264Option[]> {
    const probe = createH264Probe(signal, checkCurrent);
    checkProbe(probe);
    if (server.legacy || typeof RTCRtpSender === "undefined") return [];
    const codecs = RTCRtpSender.getCapabilities?.("video")?.codecs ?? [];
    const pairs = codecs.flatMap((local) => (server.codecs ?? []).filter((remote) => matchingH264(local, remote)).map((remote) => ({local, remote})));
    const options: H264Option[] = [];
    for (const resolution of ["720p", "1080p", "1440p", "2160p"] as const) {
        for (const fps of [60, 30, 15, 10] as const) {
            const [width, height] = screenSizes[resolution];
            for (const {local, remote} of pairs) {
                if (await sendingH264Format(local, remote, width, height, fps, bitrate, probe)) {
                    options.push({resolution, fps});
                    break;
                }
            }
        }
    }
    return options;
}
export async function receivingH264Formats(codecs: Codec[], server: Server, checkCurrent?: () => void): Promise<H264Format[]> {
    const probe = createH264Probe(undefined, checkCurrent);
    checkProbe(probe);
    if (server.legacy) return [];
    const result: H264Format[] = [];
    for (const local of codecs) {
        const peer = server.codecs?.find((remote) => matchingH264(local, remote));
        if (!peer) continue;
        const own = level(local.sdpFmtpLine);
        // Verify a concrete receive size/rate for every level we advertise.
        for (const [candidate, width, height, fps] of [[52, 3840, 2160, 60], [51, 3840, 2160, 30], [50, 2560, 1440, 30], [42, 1920, 1080, 60], [40, 1920, 1080, 30], [32, 1280, 720, 60]]) {
            if (candidate <= own || candidate > level(peer.sdpFmtpLine)) continue;
            const original = local.sdpFmtpLine ?? "", negotiated = withLevel(original, candidate);
            if (await capability(negotiated, width, height, fps, 10_000_000, true, probe) === true) {
                result.push({original, negotiated});
                break;
            }
        }
    }
    return result;
}
// setCodecPreferences accepts the original capabilities only. Apply verified
// levels to the offer BEFORE setLocalDescription, keeping payload IDs and all
// unrelated parameters intact. Pion then records the actual offered level.
export function applyH264Formats(sdp: string, formats: H264Format[]): string {
    return sdp.split(/(?=^m=)/m).map((section) => {
        if (!section.startsWith("m=video ")) return section;
        const payloads = new Set([...section.matchAll(/^a=rtpmap:(\d+) H264\/90000\r?$/gim)].map((match) => match[1]));
        return section.replace(/^a=fmtp:(\d+) ([^\r\n]+)/gm, (line, payload: string, value: string) => {
            if (!payloads.has(payload)) return line;
            const plan = formats.find((format) => matchingH264({mimeType: "video/H264", sdpFmtpLine: value}, {mimeType: "video/H264", sdpFmtpLine: format.original}) && level(value) === level(format.original));
            return plan ? `a=fmtp:${payload} ${withLevel(value, level(plan.negotiated))}` : line;
        });
    }).join("");
}
