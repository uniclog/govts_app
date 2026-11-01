export type ScreenProfileID = "economy" | "text" | "standard";

export type ScreenProfile = {
    id: ScreenProfileID;
    label: string;
    width: number;
    height: number;
    minFrameRate: number;
    frameRate: number;
    bitrate: number;
    contentHint: "detail" | "motion";
    degradationPreference: "maintain-framerate" | "maintain-resolution";
};

export const screenProfiles: ScreenProfile[] = [
    {
        id: "economy",
        label: "Экономный · 720p/15",
        width: 1280,
        height: 720,
        minFrameRate: 10,
        frameRate: 15,
        bitrate: 1_000_000,
        contentHint: "motion",
        degradationPreference: "maintain-framerate"
    },
    {
        id: "text",
        label: "Текст · 1080p/15",
        width: 1920,
        height: 1080,
        minFrameRate: 10,
        frameRate: 15,
        bitrate: 2_000_000,
        contentHint: "detail",
        degradationPreference: "maintain-resolution"
    },
    {
        id: "standard",
        label: "Стандартный · 1080p/30",
        width: 1920,
        height: 1080,
        minFrameRate: 15,
        frameRate: 30,
        bitrate: 2_500_000,
        contentHint: "motion",
        degradationPreference: "maintain-framerate"
    },
];

export type ScreenCodec = "auto" | "VP8" | "H264" | "VP9" | "AV1";
export type ScreenSettings = {version: 1; fps: 10 | 15 | 30 | 60; resolution: "720p" | "1080p" | "1440p" | "2160p" | "native"; codec: ScreenCodec; content: "detail" | "motion"; bitrate: number};
export const defaultScreenSettings: ScreenSettings = {version: 1, fps: 15, resolution: "1080p", codec: "auto", content: "detail", bitrate: 2_000_000};
export const screenBitrates: number[] = [1_000_000, 2_000_000, 2_500_000, 4_000_000, 6_000_000, 8_000_000, 10_000_000, 15_000_000, 20_000_000];
export const screenSizes = {"720p": [1280,720], "1080p": [1920,1080], "1440p": [2560,1440], "2160p": [3840,2160], native: [0,0]} as const;
export function settingsFromProfile(id: ScreenProfileID): ScreenSettings {
    const p = screenProfiles.find((p) => p.id === id)!;
    return {version: 1, fps: p.frameRate as ScreenSettings["fps"], resolution: id === "economy" ? "720p" : "1080p", codec: "auto", content: p.contentHint, bitrate: p.bitrate};
}
export function loadScreenSettings(): ScreenSettings {
    try {
        const raw = localStorage.getItem("sonoryx.screenSettings");
        if (raw) {
            const s = JSON.parse(raw);
            if (s.version === 1 && [10,15,30,60].includes(s.fps) && Object.prototype.hasOwnProperty.call(screenSizes,s.resolution) && ["auto","VP8","H264","VP9","AV1"].includes(s.codec) && ["detail","motion"].includes(s.content) && screenBitrates.includes(s.bitrate)) return s;
            return {...defaultScreenSettings};
        }
        const old = localStorage.getItem("sonoryx.screenProfile");
        return screenProfiles.some((p) => p.id === old) ? settingsFromProfile(old as ScreenProfileID) : {...defaultScreenSettings};
    } catch { return {...defaultScreenSettings}; }
}
export function profileFromSettings(s: ScreenSettings): ScreenProfile {
    const [width,height] = screenSizes[s.resolution];
    return {id: "text", label: "", width, height, minFrameRate: 0, frameRate: s.fps, bitrate: s.bitrate, contentHint: s.content, degradationPreference: s.content === "detail" ? "maintain-resolution" : "maintain-framerate"};
}
