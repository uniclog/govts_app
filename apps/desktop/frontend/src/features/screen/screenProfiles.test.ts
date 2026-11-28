import {afterEach, beforeEach, describe, expect, it, vi} from "vitest";
import {defaultScreenSettings, loadScreenSettings, profileFromSettings} from "./screenProfiles";
import {en} from "../../locales/en";
import {ru} from "../../locales/ru";
import {ky} from "../../locales/ky";

const storage = new Map<string, string>();
beforeEach(() => {
    storage.clear();
    vi.stubGlobal("localStorage", {getItem: (key: string) => storage.get(key) ?? null});
});
afterEach(() => vi.unstubAllGlobals());

describe("screen settings migration and validation", () => {
    it.each([
        ["economy", "720p", 15, "motion", 1_000_000],
        ["text", "1080p", 15, "detail", 2_000_000],
        ["standard", "1080p", 30, "motion", 2_500_000],
    ])("preserves the previous %s profile", (id, resolution, fps, content, bitrate) => {
        storage.set("sonoryx.screenProfile", String(id));
        expect(loadScreenSettings()).toMatchObject({version: 1, codec: "auto", resolution, fps, content, bitrate});
    });

    it.each(["null", "[]", "not JSON", JSON.stringify({...defaultScreenSettings, version: 2}), JSON.stringify({...defaultScreenSettings, resolution: "__proto__"}), JSON.stringify({...defaultScreenSettings, fps: 120}), JSON.stringify({...defaultScreenSettings, codec: "H265"}), JSON.stringify({...defaultScreenSettings, bitrate: -1})])("falls back safely for invalid stored settings: %s", (value) => {
        storage.set("sonoryx.screenSettings", value);
        expect(loadScreenSettings()).toEqual(defaultScreenSettings);
    });

    it("keeps independent fields and prioritizes the new schema over an old profile", () => {
        const settings = {...defaultScreenSettings, resolution: "2160p" as const, fps: 60 as const, codec: "AV1" as const};
        storage.set("sonoryx.screenProfile", "economy");
        storage.set("sonoryx.screenSettings", JSON.stringify(settings));
        expect(loadScreenSettings()).toEqual(settings);
        expect(profileFromSettings(settings)).toMatchObject({width: 3840, height: 2160, frameRate: 60, contentHint: "detail", degradationPreference: "maintain-resolution", bitrate: 2_000_000});
    });

    it("retains native size and motion priority independently of FPS", () => {
        expect(profileFromSettings({...defaultScreenSettings, resolution: "native", content: "motion", fps: 10})).toMatchObject({width: 0, height: 0, frameRate: 10, contentHint: "motion", degradationPreference: "maintain-framerate"});
    });

    it("has intact translated labels", () => {
        for (const locale of [en, ru, ky]) {
            for (const value of Object.values(locale.screen.settings)) expect(value).not.toMatch(/\?{3,}|\uFFFD/);
        }
    });
});
