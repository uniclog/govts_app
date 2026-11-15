import {useCallback, useEffect, useRef, useState} from "react";
import type {ReactNode} from "react";
import type {AudioDeviceDTO, AudioDevicesDTO, ClientViewDTO} from "../../api";
import {desktopAPI} from "../../api";
import {Icon} from "../../components/Icon";
import {Select, type SelectOption} from "../../components/Select";
import {UpdatesPanel, type UpdatesState} from "../updates/Updates";
import {type Appearance, type SavedTheme} from "../../appearance";
import {errorText, rawError, type Messages} from "../../locales";
import {LocaleSwitch, useLocale} from "../../locales/Locale";
import {InterfaceSettings} from "./InterfaceSettings";
import {HotkeySettings} from "./HotkeySettings";
import {loadReduceMotion, saveReduceMotion} from "./motion";
import {AudioWaveform, SensitivitySlider} from "./AudioWaveform";

function vadModeOptions(m: Messages): SelectOption[] {
    return [
        {value: "level", label: m.settings.vadLevel},
        {value: "vad", label: m.settings.vadSpeech},
        {value: "hybrid", label: m.settings.vadHybrid},
    ];
}

function deviceOptions(devices: AudioDeviceDTO[], m: Messages): SelectOption[] {
    return [
        {value: "", label: m.settings.systemDevice},
        ...devices.map((device) => ({
            value: device.id,
            label: device.name,
            detail: device.isDefault ? m.settings.systemDefault : undefined,
            selectedLabel: device.isDefault ? m.settings.systemNamed(device.name) : device.name,
        })),
    ];
}

export function SettingsOverlay({onClose, children, label, closeLabel}: {onClose: () => void; children: ReactNode; label?: string; closeLabel?: string}) {
    const {m} = useLocale();
    label ??= m.settings.title;
    closeLabel ??= m.settings.close;
    useEffect(() => {
        const onKey = (event: KeyboardEvent) => {
            if (event.key !== "Escape" || event.defaultPrevented) return;
            const target = event.target;
            if (target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement || target instanceof HTMLSelectElement) return;
            onClose();
        };
        window.addEventListener("keydown", onKey);
        return () => window.removeEventListener("keydown", onKey);
    }, [onClose]);
    return <div className="settings-overlay" onMouseDown={(event) => {
        if (event.button === 0 && event.target === event.currentTarget) onClose();
    }}>
        <div className="settings-window" role="dialog" aria-modal="true" aria-label={label}>
            <button type="button" className="settings-close" title={closeLabel} aria-label={closeLabel} autoFocus onClick={onClose}><Icon name="close"/></button>
            {children}
        </div>
    </div>;
}

export function SettingsPage({view, invoke, appearance, setTheme, createCustomTheme, updateAppearance, openSavedTheme, saveNamedTheme, importThemeFile, deleteSavedTheme, exportTheme, updates}: {
    view: ClientViewDTO;
    invoke: (operation: () => Promise<unknown>) => Promise<void>;
    appearance: Appearance;
    setTheme: (value: string) => Promise<void>;
    createCustomTheme: () => void;
    updateAppearance: (patch: Partial<Appearance>) => void;
    openSavedTheme: (id: string) => void;
    saveNamedTheme: (name: string) => void;
    importThemeFile: (file: File) => Promise<void>;
    deleteSavedTheme: (id: string) => void;
    exportTheme: (theme: SavedTheme) => void;
    updates: UpdatesState;
}) {
    const {m} = useLocale();
    const [reduceMotion, setReduceMotion] = useState(loadReduceMotion);
    const [sensitivity, setSensitivity] = useState(view.audio.vadSensitivity);
    useEffect(() => setSensitivity(view.audio.vadSensitivity), [view.audio.vadSensitivity]);
    const [rnnoiseSensitivity, setRNNoiseSensitivity] = useState(view.audio.rnnoiseSensitivity);
    useEffect(() => setRNNoiseSensitivity(view.audio.rnnoiseSensitivity), [view.audio.rnnoiseSensitivity]);
    const [microphoneGain, setMicrophoneGain] = useState(view.audio.microphoneGain);
    useEffect(() => setMicrophoneGain(view.audio.microphoneGain), [view.audio.microphoneGain]);
    const [devices, setDevices] = useState<AudioDevicesDTO | null>(null);
    const [devicesError, setDevicesError] = useState("");
    const [devicePending, setDevicePending] = useState(false);
    const [section, setSection] = useState<"microphone" | "playback" | "interface" | "behavior" | "updates">("microphone");
    const [soundVolume, setSoundVolume] = useState(view.soundVolume);
    useEffect(() => setSoundVolume(view.soundVolume), [view.soundVolume]);
    const [closeToTray, setCloseToTrayState] = useState(false);
    const [rememberVolumes, setRememberVolumes] = useState(true);
    useEffect(() => {
        let active = true;
        void desktopAPI.closeToTray().then((value) => {
            if (active) setCloseToTrayState(value);
        }).catch(() => undefined);
        void desktopAPI.rememberParticipantVolumes().then((value) => {
            if (active) setRememberVolumes(value);
        }).catch(() => undefined);
        return () => {
            active = false;
        };
    }, []);
    const loadDevices = useCallback(async () => {
        try {
            const next = await desktopAPI.audioDevices();
            next.capture ??= [];
            next.playback ??= [];
            setDevices(next);
            setDevicesError("");
        } catch (error) {
            setDevicesError(rawError(error));
        }
    }, []);
    useEffect(() => {
        void loadDevices();
    }, [loadDevices]);
    // Before joining a server nothing captures the microphone; the backend
    // runs a preview pipeline for the meter while the microphone section is open.
    useEffect(() => {
        if (section !== "microphone") return;
        void desktopAPI.setMicrophonePreview(true).catch(() => undefined);
        return () => { void desktopAPI.setMicrophonePreview(false).catch(() => undefined); };
    }, [section]);
    // Disabling the preview also turns the monitor off on the backend, and so
    // does starting a connection; in a session the monitor blocks sending.
    const [monitor, setMonitor] = useState(false);
    const inSession = view.connectionStatus !== "disconnected";
    const wasInSession = useRef(inSession);
    useEffect(() => {
        if (inSession && !wasInSession.current) setMonitor(false);
        wasInSession.current = inSession;
    }, [inSession]);
    useEffect(() => { if (section !== "microphone") setMonitor(false); }, [section]);
    const toggleMonitor = () => {
        const next = !monitor;
        setMonitor(next);
        void desktopAPI.setMicrophoneMonitor(next).catch(() => setMonitor(!next));
    };
    const selectDevice = async (kind: "capture" | "playback", id: string) => {
        if (devicePending) return;
        setDevicePending(true);
        setDevicesError("");
        try {
            if (kind === "capture") await desktopAPI.setCaptureDevice(id);
            else await desktopAPI.setPlaybackDevice(id);
            await loadDevices();
        } catch (error) {
            setDevicesError(rawError(error));
        } finally {
            setDevicePending(false);
        }
    };
    return <section className="settings-page settings-shell">
        <div className="settings-layout">
            <nav className="settings-nav" aria-label={m.settings.sections}>
                <h2>{m.settings.title}</h2>
                <button type="button" aria-current={section === "microphone" ? "page" : undefined} onClick={() => setSection("microphone")}><Icon name="mic"/>{m.settings.microphone}</button>
                <button type="button" aria-current={section === "playback" ? "page" : undefined} onClick={() => setSection("playback")}><Icon name="headphones"/>{m.settings.playback}</button>
                <button type="button" aria-current={section === "interface" ? "page" : undefined} onClick={() => setSection("interface")}><Icon name="screen"/>{m.settings.interface}</button>
                <button type="button" aria-current={section === "behavior" ? "page" : undefined} onClick={() => setSection("behavior")}><Icon name="settings"/>{m.settings.behavior}</button>
                <button type="button" aria-current={section === "updates" ? "page" : undefined} onClick={() => setSection("updates")}><Icon name="download"/>{m.settings.updates}</button>
            </nav>
            <div className="settings-panel">
                <div className="settings-heading"><h2>{m.settings[section]}</h2><p>{m.settings.sectionHints[section]}</p></div>
            <div className="settings-content">
                {(section === "microphone" || section === "playback") && devicesError && <div className="device-error" role="alert">{m.settings.devicesFailed}: {errorText(devicesError, m)}
                    <button className="text-button" onClick={() => void loadDevices()}>{m.settings.retry}</button>
                </div>}
                {section === "microphone" && <>
                    <section className="settings-card"><h3>{m.settings.microphone}</h3><Select id="capture-device"
                                                                                 label={m.settings.capture}
                                                                                 value={devices?.selectedCapture ?? ""}
                                                                                 disabled={!devices || devicePending}
                                                                                 options={deviceOptions(devices?.capture ?? [], m)}
                                                                                 onChange={(id) => void selectDevice("capture", id)}/>
                        {!view.audio.captureAvailable && <p className="device-hint">{m.settings.noMic}</p>}
                        <SensitivitySlider id="microphone-gain" max={2} label={m.settings.gain}
                                           description={m.settings.gainHint}
                                           value={microphoneGain} disabled={false}
                                           onChange={setMicrophoneGain}
                                           onCommit={(value) => invoke(() => desktopAPI.setMicrophoneGain(value))}/>
                    </section>
                    <section className="settings-card">
                        <SettingToggle title={m.settings.denoise}
                                       description={m.settings.denoiseHint}
                                       checked={view.audio.rnnoiseEnabled}
                                       onChange={(value) => invoke(() => desktopAPI.setRNNoiseEnabled(value))}/>
                        <SensitivitySlider label={m.settings.denoiseAmount}
                                           description={m.settings.denoiseAmountHint}
                                           value={rnnoiseSensitivity} disabled={!view.audio.rnnoiseEnabled}
                                           onChange={setRNNoiseSensitivity}
                                           onCommit={(value) => invoke(() => desktopAPI.setRNNoiseSensitivity(value))}/>
                    </section>
                    <section className="settings-card">
                        <SettingToggle title={m.settings.vad}
                                       description={m.settings.vadHint}
                                       checked={view.audio.vadEnabled}
                                       onChange={(value) => invoke(() => desktopAPI.setVADEnabled(value))}/>
                        <Select id="vad-mode" className="mode-select" label={m.settings.mode} value={view.audio.vadMode}
                                disabled={!view.audio.vadEnabled}
                                options={vadModeOptions(m)}
                                labelExtra={<span className={`gate-indicator ${view.audio.vadOpen ? "open" : ""}`}>{view.audio.vadOpen ? m.settings.transmitting : m.settings.waitingSpeech}</span>}
                                onChange={(value) => void invoke(() => desktopAPI.setVADMode(value))}/>
                        <div className="sensitivity-setting">
                            <div><span className="setting-label">{m.settings.gate}</span>
                                <output>{Math.round((1 - sensitivity) * 100)}%</output>
                            </div>
                            <p>{m.settings.gateHint}</p></div>
                        <AudioWaveform sensitivity={sensitivity} disabled={!view.audio.vadEnabled}
                                       onSensitivityChange={setSensitivity}
                                       onSensitivityCommit={(value) => invoke(() => desktopAPI.setVADSensitivity(value))}/>
                        <div className="monitor-setting">
                            <button type="button" className={`monitor-button ${monitor ? "active" : ""}`} aria-pressed={monitor}
                                    onClick={toggleMonitor}>
                                <Icon name="sound"/>{monitor ? m.settings.stopMonitor : m.settings.monitor}
                            </button>
                            <p className="device-hint">{m.settings.monitorOn}</p>
                            {inSession && <p className={`device-hint ${monitor ? "monitor-warning" : ""}`}>{monitor
                                ? m.settings.monitorActive
                                : m.settings.monitorBlocked}</p>}
                        </div>
                    </section></>}
                {section === "playback" &&
                    <section className="settings-card compact-card"><h3>{m.settings.playback}</h3><Select id="playback-device"
                                                                                                         label={m.settings.output}
                                                                                                         value={devices?.selectedPlayback ?? ""}
                                                                                                         disabled={!devices || devicePending}
                                                                                                         options={deviceOptions(devices?.playback ?? [], m)}
                                                                                                         onChange={(id) => void selectDevice("playback", id)}/><SettingToggle
                        title={m.settings.deafen} description={m.settings.deafenHint}
                        checked={view.audio.deafened} onChange={(value) => invoke(() => desktopAPI.setDeafened(value))}/></section>}
                {section === "interface" && <><section className="settings-card"><h3>{m.settings.interface}</h3>
                    <div className="locale-setting">
                        <span className="setting-label" id="locale-label">{m.language.label}</span>
                        <p>{m.language.hint}</p>
                        <LocaleSwitch labelledBy="locale-label"/>
                    </div>
                    <InterfaceSettings appearance={appearance} onTheme={(value) => invoke(() => setTheme(value))} onCreate={createCustomTheme} onChange={updateAppearance} onOpenSaved={openSavedTheme} onSave={saveNamedTheme} onImport={importThemeFile} onExport={exportTheme} onDelete={deleteSavedTheme}/>
                    <SettingToggle title={m.settings.reduceMotion} description={m.settings.reduceMotionHint} checked={reduceMotion}
                                   onChange={async (value) => { saveReduceMotion(value); setReduceMotion(value); }}/>
                </section>
                <section className="settings-card"><h3>{m.settings.sounds}</h3>
                    <Select id="sound-pack" label={m.settings.soundPack} hint={m.settings.soundPackHint}
                            value={view.soundPack || "soft"}
                            options={[
                                {value: "short", label: m.settings.packShort},
                                {value: "soft", label: m.settings.packSoft},
                                {value: "low", label: m.settings.packLow},
                                {value: "double", label: m.settings.packDouble},
                                {value: "signal", label: m.settings.packSignal},
                            ]}
                            onChange={(id) => invoke(() => desktopAPI.setSoundPack(id))}/>
                    <SensitivitySlider id="sound-volume" max={1} label={m.settings.soundVolume}
                                       description={m.settings.soundVolumeHint}
                                       value={soundVolume} disabled={false}
                                       onChange={setSoundVolume}
                                       onCommit={(value) => invoke(() => desktopAPI.setSoundVolume(value))}/>
                    <SoundPackPreview/>
                    <div className="sound-cues">
                        <h4>{m.settings.soundCues}</h4>
                        <p className="device-hint">{m.settings.soundCuesHint}</p>
                        {(Object.keys(m.settings.cues) as (keyof typeof m.settings.cues)[]).map((id) => <SettingToggle
                            key={id} title={m.settings.cues[id]} description=""
                            checked={view.soundCues?.[id] !== false}
                            onChange={(value) => invoke(() => desktopAPI.setSoundCue(id, value))}/>)}
                    </div>
                </section></>}
                {section === "behavior" && <><section className="settings-card"><h3>{m.settings.window}</h3>
                    <SettingToggle title={m.settings.tray}
                                   description={m.settings.trayHint}
                                   checked={closeToTray}
                                   onChange={(value) => invoke(async () => {
                                       const previous = closeToTray;
                                       setCloseToTrayState(value);
                                       try {
                                           await desktopAPI.setCloseToTray(value);
                                       } catch (error) {
                                           setCloseToTrayState(previous);
                                           throw error;
                                       }
                                   })}/>
                </section>
                <section className="settings-card">
                    <SettingToggle title={m.settings.rememberVolumes} description={m.settings.rememberVolumesHint}
                                   checked={rememberVolumes}
                                   onChange={(value) => invoke(async () => {
                                       const previous = rememberVolumes;
                                       setRememberVolumes(value);
                                       try {
                                           await desktopAPI.setRememberParticipantVolumes(value);
                                       } catch (error) {
                                           setRememberVolumes(previous);
                                           throw error;
                                       }
                                   })}/>
                </section>
                <section className="settings-card"><h3>{m.settings.awayAction}</h3>
                    <SettingToggle title={m.settings.muteOnAway} description={m.settings.muteOnAwayHint}
                                   checked={view.muteOnAway}
                                   onChange={(value) => invoke(() => desktopAPI.setMuteOnAway(value))}/>
                    <SettingToggle title={m.settings.deafenOnAway} description={m.settings.deafenOnAwayHint}
                                   checked={view.deafenOnAway}
                                   onChange={(value) => invoke(() => desktopAPI.setDeafenOnAway(value))}/>
                </section>
                <HotkeySettings/></>}
                {section === "updates" && <UpdatesPanel updates={updates}/>}
            </div>
            </div>
        </div>
    </section>;
}

function SoundPackPreview() {
    const {m} = useLocale();
    const [playing, setPlaying] = useState(false);
    const [error, setError] = useState("");
    const generation = useRef(0);
    const play = async () => {
        if (playing) {
            generation.current += 1;
            setPlaying(false);
            await desktopAPI.stopSoundPreview().catch(() => undefined);
            return;
        }
        const token = ++generation.current;
        setPlaying(true);
        setError("");
        try {
            await desktopAPI.previewSoundPack();
        } catch (cause) {
            if (generation.current === token) setError(rawError(cause));
        } finally {
            if (generation.current === token) setPlaying(false);
        }
    };
    return <div className="monitor-setting">
        <button type="button" className={`monitor-button ${playing ? "active" : ""}`} aria-pressed={playing} onClick={() => void play()}>
            <Icon name="sound"/>{playing ? m.settings.previewPackStop : m.settings.previewPack}
        </button>
        <p className="device-hint">{m.settings.previewPackHint}</p>
        {error && <p className="device-hint" role="alert">{error}</p>}
    </div>;
}

function SettingToggle({title, description, checked, onChange}: {
    title: string;
    description: string;
    checked: boolean;
    onChange: (value: boolean) => Promise<void>
}) {
    return <div className="setting-toggle">
        <div><strong>{title}</strong><p>{description}</p></div>
        <label className="switch"><input type="checkbox" checked={checked}
                                         onChange={(event) => void onChange(event.target.checked)}/><span/></label>
    </div>;
}
