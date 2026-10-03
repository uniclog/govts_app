import {useEffect, useRef, useState} from "react";
import {Window as WailsWindow} from "@wailsio/runtime";
import type {ClientViewDTO, ParticipantDTO, ScreenStreamDTO} from "../../api";
import {desktopAPI} from "../../api";
import {ScreenMediaController} from "./screenMedia";
import type {ScreenSharingState} from "./ScreenShareDialog";
import type {ScreenStats} from "./screenStats";
import {Icon} from "../../components/Icon";

function ScreenAudioButton({muted, onClick, className = ""}: {muted: boolean; onClick: () => void; className?: string}) {
    const label = muted ? "Включить звук" : "Отключить звук";
    return <button type="button" className={`screen-audio-toggle ${className}`.trim()} aria-pressed={muted} title={label} onClick={onClick}>
        <Icon name={muted ? "soundOff" : "sound"}/><span>{label}</span>
    </button>;
}

function errorText(error: unknown): string {
    return (error instanceof Error ? error.message : String(error)).replace(/^Error:\s*/, "");
}

function bitrateText(value: number): string {
    return value >= 1_000_000 ? `${(value / 1_000_000).toFixed(2)} Мбит/с` : `${Math.round(value / 1000)} Кбит/с`;
}

export function ScreenViewerWindow({streamID, ownerName}: {streamID: string; ownerName: string}) {
    const videoRef = useRef<HTMLVideoElement>(null);
    const controller = useRef<ScreenMediaController | null>(null);
    const [status, setStatus] = useState("Подключение…");
    const [resolution, setResolution] = useState("");
    const [error, setError] = useState("");
    const [fullscreen, setFullscreen] = useState(false);
    const [stats, setStats] = useState<ScreenStats | null>(null);
    const [hasAudio, setHasAudio] = useState(false);
    const [muted, setMuted] = useState(false);

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
                setStatus("Просмотр завершён: вы покинули канал");
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
                    videoRef.current.muted = false;
                    try {
                        await videoRef.current.play();
                    } catch {
                        videoRef.current.muted = true;
                        if (!cancelled) setMuted(true);
                        await videoRef.current.play();
                    }
                }
                const syncAudio = () => { if (!cancelled) setHasAudio(remote.getAudioTracks().length > 0); };
                syncAudio();
                remote.addEventListener("addtrack", syncAudio);
                setStatus("В эфире");
            } catch (reason) {
                if (!cancelled) { setError(errorText(reason)); setStatus("Ошибка подключения"); }
            }
        })();
        const offState = desktopAPI.onStateChanged(() => void validateMembership().catch((reason) => setError(errorText(reason))));
        return () => {
            cancelled = true;
            offState();
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
    const toggleFullscreen = async () => {
        await WailsWindow.ToggleFullscreen();
        setFullscreen(await WailsWindow.IsFullscreen());
    };
    const toggleSound = () => {
        const next = !muted;
        if (videoRef.current) videoRef.current.muted = next;
        setMuted(next);
    };
    return <main className="screen-viewer-window">
        <header className="screen-viewer-toolbar">
            <div><strong>{ownerName}</strong><span>{status}{resolution ? ` · ${resolution}` : ""}
                {stats ? ` · ${stats.fps.toFixed(0)} FPS · ${bitrateText(stats.bitrate)} · потеряно ${stats.packetsLost} (${stats.lossSampleAvailable ? `${stats.lossPercent.toFixed(1)}%` : "нет данных"}) · RTT ${stats.rttMs.toFixed(0)} мс · jitter ${stats.jitterMs.toFixed(0)} мс · кадры ${stats.frames}, сброшено ${stats.framesDropped}${stats.freezeCount ? ` · зависания ${stats.freezeCount} / ${(stats.freezeDurationMs / 1000).toFixed(1)} с` : ""}` : ""}</span></div>
            {hasAudio && <ScreenAudioButton muted={muted} onClick={toggleSound}/>}
            <button onClick={() => void toggleFullscreen()}>{fullscreen ? "Восстановить окно" : "Во весь экран"}</button>
            <button onClick={() => void close()}>Закрыть</button>
        </header>
        {error && <div className="screen-viewer-error" role="alert">{error}</div>}
        <video ref={videoRef} autoPlay playsInline onDoubleClick={() => void toggleFullscreen()} onLoadedMetadata={(event) => {
            const video = event.currentTarget;
            setResolution(`${video.videoWidth}×${video.videoHeight}`);
        }}/>
    </main>;
}

export function ScreenStageTile({stream, ownerName, local, connected, onError, sharing}: {
    stream: ScreenStreamDTO; ownerName: string; local: boolean; connected: boolean; onError: (message: string) => void;
    sharing?: ScreenSharingState;
}) {
    const [pending, setPending] = useState(false);
    const watch = async () => {
        if (pending) return;
        setPending(true);
        try { await desktopAPI.openScreenWindow(stream.id, ownerName); }
        catch (reason) { onError(errorText(reason)); }
        finally { setPending(false); }
    };
    return <div className="stage-screen" aria-label={`Демонстрация: ${ownerName}`}>
        <span className="stage-screen-icon"><Icon name="screen"/></span>
        <span className="stage-screen-name" title={`Экран — ${ownerName}`}>Экран — {ownerName}</span>
        {local ? <span className="stage-screen-own">Вы</span> : <button type="button" disabled={pending || !connected} onClick={() => void watch()}>Смотреть</button>}
        {local && sharing?.audioAvailable && <ScreenAudioButton muted={sharing.audioMuted} onClick={sharing.toggleAudio}/>}
    </div>;
}

export function ScreenSharing({view, channelID, participants, sharing}: {
    view: ClientViewDTO;
    channelID: string;
    participants: ParticipantDTO[];
    sharing: ScreenSharingState;
}) {
    const [pending, setPending] = useState(false);
    const [error, setError] = useState("");
    const streams = (view.screenStreams ?? []).filter((stream) => stream.channelId === channelID);
    const watch = async (streamID: string, ownerName: string) => {
        setPending(true); setError("");
        try { await desktopAPI.openScreenWindow(streamID, ownerName); }
        catch (reason) { setError(errorText(reason)); }
        finally { setPending(false); }
    };

    return <section className="screen-sharing">
        <div className="screen-heading"><div><span>Демонстрации экрана</span></div></div>
        <div className="screen-list">
        {error && <div className="screen-error" role="alert">{error}</div>}
        {streams.length ? <div className="screen-cards">{streams.map((stream) => {
            const owner = participants.find((item) => item.sessionId === stream.ownerSessionId);
            const ownerName = owner?.displayName ?? "Участник";
            return <article className="screen-card" key={stream.id}>
                <div className="screen-card-preview" aria-label={`Демонстрация ${ownerName}`}><Icon name="screen"/><span>{ownerName}</span><small>Демонстрация экрана</small></div>
                <div><strong>{ownerName}</strong><small>показывает экран</small></div>
                {stream.ownerSessionId === view.sessionId ? <><span className="screen-own">Вы</span>{sharing.audioAvailable && <ScreenAudioButton muted={sharing.audioMuted} onClick={sharing.toggleAudio}/>}</>
                    : <button disabled={pending || channelID !== view.channelId} onClick={() => void watch(stream.id, ownerName)}>Смотреть</button>}
            </article>;
        })}</div> : <p className="screen-empty">Сейчас никто не демонстрирует экран.</p>}
        </div>
    </section>;
}
