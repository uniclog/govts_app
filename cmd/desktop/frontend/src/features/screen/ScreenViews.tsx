import {useEffect, useRef, useState, useSyncExternalStore, type SyntheticEvent} from "react";
import {Window as WailsWindow} from "@wailsio/runtime";
import type {ScreenStreamDTO} from "../../api";
import {desktopAPI} from "../../api";
import {ScreenMediaController} from "./screenMedia";
import type {ScreenStats} from "./screenStats";
import {Icon} from "../../components/Icon";
import {errorText, rawError} from "../../locales";
import {useLocale} from "../../locales/Locale";

type SoundPreference = {volume: number; muted: boolean};
const soundKey = "sonoryx.screenSound";
function loadSound(): SoundPreference {
    try {
        const value = JSON.parse(localStorage.getItem(soundKey) ?? "null");
        if (typeof value?.volume === "number" && value.volume >= 0 && value.volume <= 1 && typeof value.muted === "boolean") return value;
    } catch { /* Defaults below. */ }
    return {volume: 1, muted: false};
}
function saveSound(value: SoundPreference) {
    try { localStorage.setItem(soundKey, JSON.stringify(value)); } catch { /* Kept for this window. */ }
}

function bitrateText(value: number, m: ReturnType<typeof useLocale>["m"]): string {
    return value >= 1_000_000 ? m.screen.mbps((value / 1_000_000).toFixed(2)) : m.screen.kbps(String(Math.round(value / 1000)));
}

export function ScreenViewerWindow({streamID, ownerName}: {streamID: string; ownerName: string}) {
    const {m} = useLocale();
    const videoRef = useRef<HTMLVideoElement>(null);
    const controller = useRef<ScreenMediaController | null>(null);
    const [status, setStatus] = useState<"connecting" | "left" | "live" | "failed">("connecting");
    const [resolution, setResolution] = useState("");
    const [error, setError] = useState("");
    const [fullscreen, setFullscreen] = useState(false);
    const [stats, setStats] = useState<ScreenStats | null>(null);
    // The video stays muted so a blocked autoplay of sound never pauses it;
    // sound plays from its own element.
    const audioRef = useRef<HTMLAudioElement>(null);
    const [hasSound, setHasSound] = useState(false);
    const [soundBlocked, setSoundBlocked] = useState(false);
    const [sound, setSound] = useState(loadSound);
    const updateSound = (next: SoundPreference) => { setSound(next); saveSound(next); };
    const playSound = () => {
        const audio = audioRef.current;
        if (!audio?.srcObject) return;
        void audio.play().then(() => setSoundBlocked(false)).catch(() => setSoundBlocked(true));
    };
    useEffect(() => {
        if (!audioRef.current) return;
        audioRef.current.volume = sound.volume;
        audioRef.current.muted = sound.muted;
    }, [sound, hasSound]);

    useEffect(() => {
        let cancelled = false;
        const media = new ScreenMediaController();
        controller.current = media;
        const close = async () => {
            await media.unsubscribe();
            if (!cancelled) await WailsWindow.Close();
        };
        const validateMembership = async () => {
            const snapshot = await desktopAPI.snapshot();
            const stream = (snapshot.screenStreams ?? []).find((item) => item.id === streamID);
            if (!stream || stream.channelId !== snapshot.channelId) {
                setStatus("left");
                await close();
                return false;
            }
            return true;
        };
        void (async () => {
            try {
                if (!await validateMembership() || cancelled) return;
                const remote = await media.subscribe(streamID);
                if (cancelled) { await media.unsubscribe(); return; }
                if (videoRef.current) {
                    videoRef.current.srcObject = remote;
                    await videoRef.current.play();
                }
                const tracks = remote.getAudioTracks();
                if (tracks.length && audioRef.current) {
                    // Saved volume applies before the first sample plays.
                    const saved = loadSound();
                    audioRef.current.volume = saved.volume;
                    audioRef.current.muted = saved.muted;
                    audioRef.current.srcObject = new MediaStream(tracks);
                    setHasSound(true);
                    playSound();
                }
                setStatus("live");
            } catch (reason) {
                if (!cancelled) { setError(rawError(reason)); setStatus("failed"); }
            }
        })();
        const offState = desktopAPI.onStateChanged(() => void validateMembership().catch((reason) => setError(rawError(reason))));
        return () => {
            cancelled = true;
            offState();
            if (audioRef.current) audioRef.current.srcObject = null;
            media.dispose();
            controller.current = null;
        };
    }, [streamID]);
    useEffect(() => {
        const timer = window.setInterval(() => void controller.current?.getViewerStats().then(setStats).catch(() => undefined), 1000);
        return () => window.clearInterval(timer);
    }, []);

    const close = async () => {
        await controller.current?.unsubscribe();
        await WailsWindow.Close();
    };
    const showResolution = (event: SyntheticEvent<HTMLVideoElement>) => {
        const video = event.currentTarget;
        setResolution(`${video.videoWidth}×${video.videoHeight}`);
    };
    const toggleFullscreen = async () => {
        await WailsWindow.ToggleFullscreen();
        setFullscreen(await WailsWindow.IsFullscreen());
    };
    const statusText = status === "left" ? m.screen.leftChannel : status === "live" ? m.screen.live : status === "failed" ? m.screen.connectError : m.screen.connecting;
    const statsText = stats ? m.screen.viewerLine(stats.fps.toFixed(0), bitrateText(stats.bitrate, m), stats.packetsLost, stats.lossSampleAvailable ? `${stats.lossPercent.toFixed(1)}%` : m.screen.lossUnknown, stats.rttMs.toFixed(0), stats.jitterMs.toFixed(0), stats.frames, stats.framesDropped) + (stats.freezeCount ? m.screen.freezes(stats.freezeCount, (stats.freezeDurationMs / 1000).toFixed(1)) : "") : "";
    return <main className="screen-viewer-window">
        <header className="screen-viewer-toolbar">
            <div><strong>{ownerName}</strong><span>{statusText}{resolution ? ` · ${resolution}` : ""}{statsText}{stats?.codec ? ` · ${stats.codec}` : ""}{stats?.implementation ? ` · ${stats.implementation}` : ""}</span></div>
            {hasSound && (soundBlocked ? <button onClick={playSound}>{m.screen.enableSound}</button> : <>
                <button aria-pressed={sound.muted} onClick={() => updateSound({...sound, muted: !sound.muted})}>{sound.muted ? m.screen.unmute : m.screen.mute}</button>
                <input className="screen-viewer-volume" type="range" min={0} max={1} step={0.05} value={sound.volume} aria-label={m.screen.volume}
                    onChange={(event) => updateSound({volume: Number(event.target.value), muted: false})}/>
            </>)}
            <button onClick={() => void toggleFullscreen()}>{fullscreen ? m.screen.restore : m.screen.fullscreen}</button>
            <button onClick={() => void close()}>{m.screen.close}</button>
        </header>
        {error && <div className="screen-viewer-error" role="alert">{errorText(error, m)}</div>}
        {/* The first frames are often smaller while the sender ramps up; resize tracks later changes. */}
        <video ref={videoRef} autoPlay muted playsInline onDoubleClick={() => void toggleFullscreen()} onLoadedMetadata={showResolution} onResize={showResolution}/>
        <audio ref={audioRef} autoPlay/>
    </main>;
}

export function ScreenStageTile({stream, ownerName, local, connected, onError, screenMedia}: {
    stream: ScreenStreamDTO; ownerName: string; local: boolean; connected: boolean; onError: (message: string) => void;
    screenMedia: ScreenMediaController;
}) {
    const {m} = useLocale();
    const [pending, setPending] = useState(false);
    const preview = useSyncExternalStore(screenMedia.onPreviewChanged, screenMedia.localPreview);
    const watch = async () => {
        if (pending) return;
        setPending(true);
        try { await desktopAPI.openScreenWindow(stream.id, ownerName); }
        catch (reason) { onError(rawError(reason)); }
        finally { setPending(false); }
    };
    return <div className="stage-screen stage-screen-card" aria-label={m.screen.tile(ownerName)}>
        {local && preview ? <div className="stage-screen-preview"><ScreenSelfPreview stream={preview} label={m.screen.selfPreview}/></div>
            : <span className="stage-screen-icon"><Icon name="screen"/></span>}
        <span className="stage-screen-name" title={m.screen.tileName(ownerName)}>{m.screen.tileName(ownerName)}</span>
        {local ? <span className="stage-screen-own">{m.screen.you}</span> : <button type="button" disabled={pending || !connected} onClick={() => void watch()}>{m.screen.watch}</button>}
    </div>;
}

// The sharer's own capture, shown muted from the published track; no extra
// capture, subscription or encoding.
function ScreenSelfPreview({stream, label}: {stream: MediaStream; label: string}) {
    const videoRef = useRef<HTMLVideoElement>(null);
    useEffect(() => {
        const video = videoRef.current;
        if (!video) return;
        video.srcObject = stream;
        void video.play().catch(() => undefined);
        return () => { video.srcObject = null; };
    }, [stream]);
    return <video ref={videoRef} className="screen-self-preview" aria-label={label} autoPlay muted playsInline/>;
}
