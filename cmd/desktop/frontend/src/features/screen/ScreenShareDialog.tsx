import {useEffect, useRef, useState} from "react";
import type {ClientViewDTO} from "../../api";
import {desktopAPI, logDiagnostic} from "../../api";
import {ScreenMediaController, ScreenCodecLimitError, senderCodecAvailable, senderH264Level, h264SettingsFit, h264SupportedSettings} from "./screenMedia";
import {logScreenCodecCapabilities} from "./screenCodecDiagnostics";
import {supportedH264Options, type H264Option} from "./screenH264";
import {loadScreenSettings, screenBitrates, type ScreenSettings} from "./screenProfiles";
import {loadScreenAudioApp, loadScreenAudioSource, saveScreenAudioApp, saveScreenAudioSource, type ScreenAudioChoice, type ScreenAudioSource} from "./screenAudio";
import {Select} from "../../components/Select";
import {errorText} from "../../locales";
import {useLocale} from "../../locales/Locale";

export function useScreenSharing(view: ClientViewDTO, controller: ScreenMediaController, onError: (message: string) => void) {
    const [open, setOpen] = useState(false);
    const [pending, setPending] = useState(false);
    const [publishing, setPublishing] = useState(false);
    const [error, setError] = useState("");
    const [codecLimit, setCodecLimit] = useState<number | null>(null);
    const [settings, setSettings] = useState<ScreenSettings>(loadScreenSettings);
    // The source persists; an app is chosen per share because process ids change.
    const [soundSource, setSoundSource] = useState<ScreenAudioSource>(loadScreenAudioSource);
    const [soundApp, setSoundApp] = useState(0);
    // null until the dialog probes per-process capture on this system.
    const [nativeSound, setNativeSound] = useState<boolean | null>(null);
    const selectSound = (source: ScreenAudioSource) => { setSoundSource(source); saveScreenAudioSource(source); };
    const updateSettings = (next: ScreenSettings) => {
        setSettings(next);
        setError("");
        setCodecLimit(null);
        try { localStorage.setItem("sonoryx.screenSettings", JSON.stringify(next)); } catch { /* Session choice survives. */ }
    };
    const buttonRef = useRef<HTMLButtonElement>(null);
    const busy = useRef(false);
    const dialogGeneration = useRef(0);
    const context = `${view.sessionId}:${view.channelId}:${view.connectionStatus}`;
    const currentContext = useRef(context);
    currentContext.current = context;
    const available = view.connectionStatus === "connected" && view.channelId !== "0";
    const own = (view.screenStreams ?? []).some((stream) => stream.ownerSessionId === view.sessionId);
    const active = publishing || own;

    useEffect(() => {
        ++dialogGeneration.current;
        setOpen(false);
        setError("");
        setCodecLimit(null);
        setPublishing(false);
        return () => {
            void controller.stopPublishing().catch((reason) => logDiagnostic("screen_context_cleanup", reason));
        };
    }, [context, controller]);

    const close = () => {
        ++dialogGeneration.current;
        setOpen(false);
        if (busy.current) void controller.stopPublishing().catch((reason) => logDiagnostic("screen_cancel_cleanup", reason));
    };
    const selectSettings = updateSettings;
    // The dialog resolves the sound against this system and the server.
    const start = async (sound: ScreenAudioChoice = {source: "off"}) => {
        if (busy.current || !available) return;
        busy.current = true; setPending(true); setError(""); setCodecLimit(null); setOpen(false);
        const startedIn = context;
        const generation = ++dialogGeneration.current;
        const stillCurrent = () => currentContext.current === startedIn && dialogGeneration.current === generation;
        try {
            if (!controller.publishingPrepared()) {
                await controller.preparePublishing();
                if (stillCurrent()) setOpen(true);
                return; // Capture requires a new, explicit Start click.
            }
            await controller.publish(() => { if (currentContext.current === startedIn) setPublishing(false); }, settings, stillCurrent, sound);
            if (stillCurrent()) { setPublishing(true); setOpen(false); }
        } catch (reason) {
            const cancelled = reason instanceof DOMException && ["NotAllowedError", "AbortError"].includes(reason.name)
                || /permission denied by user|user cancel(?:led|ed)/i.test(String(reason));
            if (stillCurrent()) {
                setOpen(true);
                if (!cancelled) {
                    if (reason instanceof ScreenCodecLimitError) setCodecLimit(reason.level);
                    else setError(reason instanceof Error ? reason.message : String(reason));
                }
            }
        } finally { busy.current = false; setPending(false); }
    };
    const toggle = async () => {
        if (busy.current || !available) return;
        busy.current = true; setPending(true);
        const startedIn = context;
        try {
            if (active) {
                await controller.stopPublishing();
                setPublishing(false);
            } else {
                setError("");
                await controller.preparePublishing();
                if (currentContext.current === startedIn) setOpen(true);
            }
        }
        catch (reason) {
            if (!(reason instanceof DOMException && reason.name === "AbortError")) onError(reason instanceof Error ? reason.message : String(reason));
        }
        finally { busy.current = false; setPending(false); }
    };
    return {open, pending, active, available, settings, selectSettings, soundSource, selectSound, soundApp, setSoundApp, nativeSound, setNativeSound, error, codecLimit, buttonRef, close, start, toggle};
}

export type ScreenSharingState = ReturnType<typeof useScreenSharing>;

export function ScreenShareDialog({sharing}: {sharing: ScreenSharingState}) {
    const {m} = useLocale();
    const dialogRef = useRef<HTMLDialogElement>(null);
    const restoreFocus = useRef(false);
    const [serverCodecs, setServerCodecs] = useState<Awaited<ReturnType<typeof desktopAPI.screenCapabilities>>>();
    const [checkingCodecs, setCheckingCodecs] = useState(false);
    const [verifiedH264, setVerifiedH264] = useState<{server: typeof serverCodecs; bitrate: number; options: H264Option[]} | null>(null);
    useEffect(() => {
        if (!sharing.open) return;
        let cancelled = false;
        setServerCodecs(undefined);
        setCheckingCodecs(true);
        void desktopAPI.screenCapabilities().then((result) => {
            if (!cancelled) {
                setServerCodecs(result);
                void logScreenCodecCapabilities(result).catch((reason) => logDiagnostic("screen_h264_capabilities_failed", reason));
            }
        }).catch((reason) => {
            // Publishing performs the authenticated check again.
            if (!cancelled) {
                logDiagnostic("screen_capabilities_failed", reason);
                void logScreenCodecCapabilities().catch((error) => logDiagnostic("screen_h264_capabilities_failed", error));
            }
        }).finally(() => {
            if (!cancelled) setCheckingCodecs(false);
        });
        return () => { cancelled = true; };
    }, [sharing.open]);
    useEffect(() => {
        if (!sharing.open || !serverCodecs) return;
        let cancelled = false;
        const abort = new AbortController();
        const bitrate = sharing.settings.bitrate;
        void supportedH264Options(serverCodecs, bitrate, abort.signal).then((options) => {
            if (!cancelled) setVerifiedH264({server: serverCodecs, bitrate, options});
        }).catch((reason) => {
            if (!cancelled) {
                logDiagnostic("screen_h264_options_failed", reason);
                setVerifiedH264({server: serverCodecs, bitrate, options: []});
            }
        });
        return () => { cancelled = true; abort.abort(); };
    }, [sharing.open, serverCodecs, sharing.settings.bitrate]);
    const [soundApps, setSoundApps] = useState<{pid: number; name: string; title: string}[]>([]);
    const {nativeSound, setNativeSound, soundApp, setSoundApp} = sharing;
    useEffect(() => {
        if (!sharing.open || nativeSound !== null) return;
        void desktopAPI.screenAudioSupported().then(setNativeSound).catch(() => setNativeSound(false));
    }, [sharing.open, nativeSound, setNativeSound]);
    // Process ids change between runs, so the last app is remembered by exe name.
    const loadSoundApps = () => void desktopAPI.screenAudioApps().then((list) => {
        const apps = list ?? [];
        setSoundApps(apps);
        if (apps.some((app) => app.pid === soundApp)) return;
        const remembered = loadScreenAudioApp();
        setSoundApp((apps.find((app) => remembered && app.name === remembered) ?? apps[0])?.pid ?? 0);
    }).catch((reason) => { setSoundApps([]); logDiagnostic("screen_audio_apps", reason); });
    const chooseSoundApp = (pid: number) => {
        setSoundApp(pid);
        const name = soundApps.find((app) => app.pid === pid)?.name;
        if (name) saveScreenAudioApp(name);
    };
    useEffect(() => {
        if (sharing.open && sharing.soundSource === "app" && nativeSound) loadSoundApps();
    }, [sharing.open, sharing.soundSource, nativeSound]);
    // Unknown while checking: publishing checks the server again.
    const serverSound = !serverCodecs || (!serverCodecs.legacy && Boolean(serverCodecs.audio?.length));
    const soundValue = sharing.soundSource;
    // A server or Windows without stream sound, or "app" with no app picked,
    // shares video only.
    const soundAvailable = serverSound && nativeSound !== false;
    const effectiveSound: ScreenAudioChoice = !soundAvailable || soundValue === "app" && !soundApp ? {source: "off"} : {source: soundValue, pid: soundApp};
    const h264Level = sharing.codecLimit ?? senderH264Level(serverCodecs);
    const verifiedOptions = verifiedH264?.server === serverCodecs && verifiedH264?.bitrate === sharing.settings.bitrate ? verifiedH264.options : null;
    const checkingH264 = Boolean(serverCodecs && !verifiedOptions);
    const checkingCompatibility = checkingCodecs || checkingH264;
    const fallbackH264 = h264SupportedSettings(h264Level).flatMap((option) =>
        ([60, 30, 15, 10] as const).filter((fps) => fps <= option.fps).map((fps) => ({...option, fps})));
    const allowedH264 = (verifiedOptions ?? fallbackH264).filter((option) =>
        sharing.codecLimit === null || h264SettingsFit({...sharing.settings, ...option}, sharing.codecLimit) !== false);
    const h264Options = allowedH264.filter((option, index) => allowedH264.findIndex((candidate) => candidate.resolution === option.resolution) === index);
    const h264Selected = sharing.settings.codec === "H264";
    const fitsH264 = sharing.settings.resolution === "native" || !verifiedOptions
        ? h264SettingsFit(sharing.settings, h264Level) !== false
        : verifiedOptions.some((option) => option.resolution === sharing.settings.resolution && option.fps === sharing.settings.fps);
    const incompatible = h264Selected && !checkingCompatibility && !fitsH264;
    const codecWarning = incompatible || sharing.codecLimit !== null;
    const currentHeight = sharing.settings.resolution === "native" ? Infinity : Number(sharing.settings.resolution.slice(0, -1));
    const suggestions = allowedH264.filter((option) => option.fps <= sharing.settings.fps);
    const suggestion = suggestions.filter((option, index) => suggestions.findIndex((candidate) => candidate.resolution === option.resolution) === index)
        .reverse().find((option) => Number(option.resolution.slice(0, -1)) <= currentHeight);
    const useSuggestion = () => {
        if (suggestion) sharing.selectSettings({...sharing.settings, resolution: suggestion.resolution, fps: Math.min(sharing.settings.fps, suggestion.fps) as ScreenSettings["fps"]});
    };
    useEffect(() => {
        const dialog = dialogRef.current;
        if (!dialog) return;
        if (sharing.open && !dialog.open) dialog.showModal();
        else if (!sharing.open && dialog.open) {
            dialog.close();
            restoreFocus.current = true;
        }
        if (!sharing.open && !sharing.pending && restoreFocus.current) {
            sharing.buttonRef.current?.focus();
            restoreFocus.current = false;
        }
    }, [sharing.open, sharing.pending, sharing.buttonRef]);
    return <dialog ref={dialogRef} className="screen-share-dialog" aria-labelledby="screen-share-title"
        onCancel={(event) => { event.preventDefault(); sharing.close(); }}>
        <h2 id="screen-share-title">{m.screen.title}</h2>
        <p>{m.screen.pick}</p>
        <fieldset className="screen-settings" disabled={sharing.pending}>
            <Select id="screen-fps" label="FPS" value={String(sharing.settings.fps)}
                options={[10, 15, 30, 60].map((fps) => ({value: String(fps), label: `${fps} FPS`}))}
                onChange={(value) => sharing.selectSettings({...sharing.settings, fps: Number(value) as ScreenSettings["fps"]})}/>
            <Select id="screen-resolution" label={m.connection.resolution} value={sharing.settings.resolution}
                options={["720p", "1080p", "1440p", "2160p", "native"].map((size) => ({value: size, label: size === "native" ? m.screen.settings.native : size}))}
                onChange={(value) => sharing.selectSettings({...sharing.settings, resolution: value as ScreenSettings["resolution"]})}/>
            <Select id="screen-codec" label={m.screen.settings.codec} value={sharing.settings.codec}
                options={["auto", "VP8", "H264", "VP9", "AV1"].map((codec) => {
                    const unavailable = codec !== "auto" && !senderCodecAvailable(codec);
                    return {value: codec, label: codec === "auto" ? m.screen.settings.auto : codec === "H264" ? "H.264" : codec, detail: unavailable ? m.screen.settings.unavailable : undefined, disabled: unavailable};
                })}
                onChange={(value) => sharing.selectSettings({...sharing.settings, codec: value as ScreenSettings["codec"]})}/>
            <Select id="screen-content" label={m.screen.settings.content} value={sharing.settings.content}
                options={[{value: "detail", label: m.screen.settings.detail}, {value: "motion", label: m.screen.settings.motion}]}
                onChange={(value) => sharing.selectSettings({...sharing.settings, content: value as ScreenSettings["content"]})}/>
            <Select id="screen-bitrate" label={m.connection.bitrate} value={String(sharing.settings.bitrate)}
                options={screenBitrates.map((bitrate) => ({value: String(bitrate), label: `${bitrate / 1_000_000} Mbps`}))}
                onChange={(value) => sharing.selectSettings({...sharing.settings, bitrate: Number(value)})}/>
            <Select id="screen-sound" label={m.screen.audio.label} value={soundAvailable ? soundValue : "off"} disabled={!soundAvailable}
                options={[{value: "off", label: m.screen.audio.off}, {value: "system", label: m.screen.audio.system}, {value: "app", label: m.screen.audio.app}]}
                onChange={(value) => sharing.selectSound(value as ScreenAudioSource)}/>
            {soundAvailable && soundValue === "app" && <Select id="screen-sound-app" className="screen-sound-app" label={m.screen.audio.pickApp} value={String(soundApp)}
                options={soundApps.length ? soundApps.map((app) => ({value: String(app.pid), label: app.title, detail: app.name || undefined})) : [{value: "0", label: m.screen.audio.noApps, disabled: true}]}
                onOpen={loadSoundApps}
                onChange={(value) => chooseSoundApp(Number(value))}/>}
        </fieldset>
        {serverCodecs && !serverSound && <p className="screen-capability-note">{m.screen.audio.unsupportedServer}</p>}
        {serverSound && nativeSound === false && <p className="screen-capability-note">{m.screen.audio.unavailable}</p>}
        {/* Hidden, not removed, while it has nothing to say (VP9/AV1 once the
            server is checked), so the live region keeps announcing changes. */}
        <div id="screen-codec-help" className={`screen-codec-help${codecWarning ? " screen-codec-warning" : ""}`} aria-live="polite"
            hidden={!(checkingCompatibility || codecWarning || !serverCodecs || h264Selected || sharing.settings.codec === "auto" || sharing.settings.codec === "VP8")}>
            {checkingCompatibility ? <p>{m.screen.compatibility.checking}</p> : <>
                {sharing.settings.codec === "auto" ? <p>{m.screen.compatibility.auto}</p> : sharing.settings.codec === "VP8" ? <p>{m.screen.compatibility.vp8}</p> : h264Selected && <p>{m.screen.compatibility.h264}</p>}
                {h264Selected && h264Options.length > 0 && <p>{serverCodecs ? m.screen.compatibility.connectionLimits : m.screen.compatibility.clientLimits} {h264Options.map((option) => `${option.resolution} / ${option.fps} FPS`).join(" · ")}</p>}
                {h264Selected && sharing.settings.resolution === "native" && <p>{m.screen.compatibility.native}</p>}
                {!serverCodecs && <p>{m.screen.settings.capabilities}</p>}
            </>}
            {codecWarning && <>
                <p role="alert">{!h264Level ? m.screen.compatibility.unsupported : sharing.codecLimit !== null ? m.screen.compatibility.captureTooLarge : m.screen.compatibility.tooLarge}</p>
                <div className="screen-codec-actions">
                    {suggestion && <button type="button" disabled={sharing.pending} onClick={useSuggestion}>{m.screen.compatibility.use} {suggestion.resolution} / {Math.min(sharing.settings.fps, suggestion.fps)} FPS</button>}
                    <button type="button" disabled={sharing.pending} onClick={() => sharing.selectSettings({...sharing.settings, codec: "auto"})}>{m.screen.compatibility.useAuto}</button>
                </div>
            </>}
        </div>
        <p className="screen-capability-note">{m.screen.compatibility.bitrate}</p>
        {sharing.error && <p className="screen-error" role="alert">{errorText(sharing.error, m)}</p>}
        <div className="screen-dialog-actions">
            <button type="button" onClick={sharing.close}>{m.screen.cancel}</button>
            <button className="primary-button" type="button" disabled={sharing.pending || !sharing.available || incompatible || sharing.codecLimit !== null || h264Selected && checkingCompatibility}
                onClick={() => {
                    // Remove the modal top layer before WebView2 opens its capture
                    // picker, while keeping getDisplayMedia in the click call chain.
                    dialogRef.current?.close();
                    restoreFocus.current = true;
                    void sharing.start(effectiveSound);
                }}>{sharing.pending ? m.screen.starting : m.screen.start}</button>
        </div>
    </dialog>;
}
