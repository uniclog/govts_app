import {logDiagnostic} from "../../api";
import {senderH264Level} from "./screenMedia";

// Capability queries do not capture a screen or open a media connection.
export async function logScreenCodecCapabilities(server?: Parameters<typeof senderH264Level>[0]): Promise<void> {
    const h264Formats = (codecs: {mimeType: string; sdpFmtpLine?: string}[] = []) =>
        [...new Set(codecs.filter((codec) => /h264$/i.test(codec.mimeType)).map((codec) => codec.sdpFmtpLine ?? ""))];
    const sender = h264Formats(typeof RTCRtpSender === "undefined" ? [] : RTCRtpSender.getCapabilities?.("video")?.codecs);
    const receiver = h264Formats(typeof RTCRtpReceiver === "undefined" ? [] : RTCRtpReceiver.getCapabilities?.("video")?.codecs);
    const remote = h264Formats(server?.codecs);
    // Separate records keep the server/level data within the logger's 2 KiB limit.
    for (const [source, formats] of Object.entries({sender, receiver, server: remote})) {
        logDiagnostic("screen_h264_capabilities", JSON.stringify({source, formats}));
    }
    logDiagnostic("screen_h264_levels", JSON.stringify({serverKnown: Boolean(server), legacy: server?.legacy ?? null,
        advertisedClientLevel: senderH264Level(), advertisedConnectionLevel: server ? senderH264Level(server) : null}));
    if (!navigator.mediaCapabilities?.encodingInfo) {
        logDiagnostic("screen_h264_encoding_support", "MediaCapabilities.encodingInfo unavailable");
        return;
    }
    // Compare advertised local formats with the server's receive format. These
    // are API predictions, not proof of an encoder or sustained capture FPS.
    const formats = [...new Set([...sender, ...remote])].filter((format) => /(?:^|;)\s*packetization-mode=1(?:;|$)/i.test(format));
    for (const format of formats) {
        for (const [width, height, framerate] of [[1280, 720, 30], [1920, 1080, 30], [1920, 1080, 60]]) {
            const video = {contentType: `video/H264;${format}`, width, height, framerate, bitrate: 6_000_000};
            try {
                const {supported, smooth, powerEfficient} = await navigator.mediaCapabilities.encodingInfo({type: "webrtc", video});
                logDiagnostic("screen_h264_encoding_support", JSON.stringify({format, width, height, framerate,
                    bitrate: video.bitrate, supported, smooth, powerEfficient}));
            } catch (reason) {
                logDiagnostic("screen_h264_encoding_support", JSON.stringify({format, width, height, framerate,
                    error: reason instanceof Error ? `${reason.name}: ${reason.message}` : String(reason)}));
            }
        }
    }
}
