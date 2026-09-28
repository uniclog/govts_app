import {useCallback, useEffect, useMemo, useRef, useState} from "react";
import type {AudioDeviceDTO, AudioDevicesDTO, ChannelDTO, ClientEventDTO, ClientViewDTO, ParticipantDTO} from "./api";
import {desktopAPI} from "./api";
import {buildChannelGroups, canMoveParticipant, mergeEventTail} from "./model";
import {ScreenMediaController} from "./features/screen/screenMedia";
import {ScreenSharing, ScreenViewerWindow} from "./features/screen/ScreenViews";
import {ConnectionPage, StatusBar, type Page} from "./features/connection/ConnectionViews";
import {ParticipantRow} from "./features/participants/ParticipantRow";

const participantDragType = "application/x-govts-participant";

const emptyView: ClientViewDTO = {
    connectionStatus: "disconnected",
    server: {name: ""}, revision: "0", sessionId: "0", channelId: "0", snapshotFresh: false,
    canKick: false, canBan: false, canDrag: false,
    channels: [], participants: [], screenStreams: [],
    audio: {
        muted: false,
        deafened: false,
        captureAvailable: true,
        rnnoiseEnabled: true,
        rnnoiseSensitivity: 1,
        vadEnabled: false,
        vadMode: "hybrid",
        vadSensitivity: 0.5,
        vadOpen: false
    },
};

function errorText(error: unknown): string {
    return (error instanceof Error ? error.message : String(error)).replace(/^Error:\s*/, "");
}

function App() {
    const [theme, setThemeState] = useState(localStorage.getItem("govts-theme") || "system");
    useEffect(() => {
        let active = true;
        void desktopAPI.theme().then((value) => {
            if (active) setThemeState(value);
        });
        return () => { active = false; };
    }, []);
    useEffect(() => {
        document.documentElement.dataset.theme = theme;
        localStorage.setItem("govts-theme", theme);
    }, [theme]);
    const setTheme = async (value: string) => {
        const previous = theme;
        setThemeState(value);
        try { await desktopAPI.setTheme(value); }
        catch (error) { setThemeState(previous); throw error; }
    };
    const params = new URLSearchParams(window.location.search);
    const streamID = params.get("screen");
    if (streamID) return <ScreenViewerWindow streamID={streamID} ownerName={params.get("owner") || "Участник"}/>;
    return <MainApp theme={theme} setTheme={setTheme}/>;
}

function MainApp({theme, setTheme}: {theme: string; setTheme: (value: string) => Promise<void>}) {
    const [view, setView] = useState<ClientViewDTO>(emptyView);
    const [page, setPage] = useState<Page>("channels");
    const [events, setEvents] = useState<ClientEventDTO[]>([]);
    const [actionError, setActionError] = useState("");
    const [eventPanelHeight, setEventPanelHeight] = useState(135);
    const lastSequence = useRef("0");
    const screenMedia = useRef<ScreenMediaController | null>(null);
    if (!screenMedia.current) screenMedia.current = new ScreenMediaController();

    const refresh = useCallback(async () => {
        try {
            const next = await desktopAPI.snapshot();
            next.channels ??= [];
            next.participants ??= [];
            next.screenStreams ??= [];
            setView(next);
        } catch (error) {
            setActionError(errorText(error));
        }
    }, []);

    const refreshEvents = useCallback(async () => {
        try {
            const next = (await desktopAPI.eventsAfter(lastSequence.current)) ?? [];
            if (!next.length) return;
            const newestSequence = next[next.length - 1].sequence;
            if (BigInt(newestSequence) > BigInt(lastSequence.current)) {
                lastSequence.current = newestSequence;
            }
            setEvents((current) => mergeEventTail(current, next));
        } catch (error) {
            setActionError(errorText(error));
        }
    }, []);

    const clearEvents = useCallback(() => {
        setEvents([]);
        lastSequence.current = "0";
    }, []);

    useEffect(() => {
        void refresh();
        void refreshEvents();
        const offState = desktopAPI.onStateChanged(() => void refresh());
        const offEvents = desktopAPI.onEventLogChanged(() => void refreshEvents());
        return () => {
            offState();
            offEvents();
        };
    }, [refresh, refreshEvents]);
    useEffect(() => {
        if (view.connectionStatus === "disconnected") void screenMedia.current?.close();
    }, [view.connectionStatus]);
    useEffect(() => {
        const dispose = () => screenMedia.current?.dispose();
        window.addEventListener("beforeunload", dispose);
        return () => {
            window.removeEventListener("beforeunload", dispose);
            dispose();
        };
    }, []);

    const invoke = useCallback(async (operation: () => Promise<unknown>) => {
        setActionError("");
        try {
            await operation();
            await refresh();
        } catch (error) {
            setActionError(errorText(error));
        }
    }, [refresh]);

    if (view.connectionStatus === "disconnected") {
        return (
            <ConnectionPage
                view={view}
                error={actionError}
                onError={setActionError}
                onRefresh={refresh}
                onClearEvents={clearEvents}
                onConnected={() => setPage("channels")}
            />
        );
    }

    return <div className="app-shell">
        <main className="main-area">
            <StatusBar view={view} page={page} onPageChange={setPage} invoke={invoke}/>
            {actionError && <div className="error-banner" role="alert">{actionError}</div>}
            {page === "channels"
                ? <ChannelsPage view={view} events={events} eventPanelHeight={eventPanelHeight}
                                onEventPanelHeightChange={setEventPanelHeight} invoke={invoke} screenMedia={screenMedia.current}
                                onError={setActionError}/>
                : <SettingsPage view={view} invoke={invoke} theme={theme} setTheme={setTheme}/>}
        </main>
    </div>;
}

function ChannelsPage({view, events, eventPanelHeight, onEventPanelHeightChange, invoke, screenMedia, onError}: {
    view: ClientViewDTO;
    events: ClientEventDTO[];
    eventPanelHeight: number;
    onEventPanelHeightChange: (height: number | ((current: number) => number)) => void;
    invoke: (operation: () => Promise<unknown>) => Promise<void>
    screenMedia: ScreenMediaController;
    onError: (message: string) => void;
}) {
    const channels = view.channels ?? [];
    const participants = view.participants ?? [];
    const [selectedID, setSelectedID] = useState(view.channelId !== "0" ? view.channelId : channels[0]?.id ?? "");
    useEffect(() => {
        if (selectedID && channels.some((channel) => channel.id === selectedID)) return;
        setSelectedID(view.channelId !== "0" ? view.channelId : channels[0]?.id ?? "");
    }, [channels, selectedID, view.channelId]);
    const selected = channels.find((channel) => channel.id === selectedID);
    return <section className="channels-page">
        <div className="channels-layout">
            <section className="panel channel-browser">
                <div className="panel-heading">
                    <div><p className="eyebrow">ПРОСТРАНСТВА</p><h2>Каналы</h2></div>
                    <span className="count-badge">{participants.length}</span></div>
                <div className="channel-scroll"><ChannelTree channels={channels} participants={participants}
                                                             canKick={view.canKick} canBan={view.canBan}
                                                             canDrag={view.canDrag && view.connectionStatus === "connected"}
                                                             selectedID={selectedID} currentID={view.channelId}
                                                             onError={onError} onSelect={setSelectedID} onJoin={(id) => {
                    if (id !== view.channelId && view.connectionStatus === "connected") void invoke(() => desktopAPI.joinChannel(id));
                }} onMoveParticipant={(sessionID, channelID) => {
                    void invoke(() => desktopAPI.drag(sessionID, channelID));
                }}/></div>
            </section>
            <section className="panel channel-detail">{selected ? <><p className="eyebrow">ВЫБРАННЫЙ КАНАЛ</p>
                <h2>{selected.name}</h2>
                <p className="channel-topic">{selected.topic || "Голосовой канал"}</p><p
                    className="channel-description">{selected.description || "Описание канала пока не задано."}</p>
                <div className="detail-grid">
                    <div>
                        <span>Участники</span><strong>{participants.filter((item) => item.channelId === selected.id).length}{selected.maxUsers ? ` / ${selected.maxUsers}` : ""}</strong>
                    </div>
                    <div><span>Кодек</span><strong>{selected.audio.codec.toUpperCase()}</strong></div>
                    <div><span>Частота</span><strong>{selected.audio.sampleRate / 1000} кГц</strong></div>
                    <div><span>Битрейт</span><strong>{selected.audio.bitrate / 1000} кбит/с</strong></div>
                </div>
                <ScreenSharing view={view} channelID={selected.id} participants={participants} controller={screenMedia}/>
            </> : <div className="empty-state">Выберите канал</div>}</section>
        </div>
        <EventPanel events={events} height={eventPanelHeight} onHeightChange={onEventPanelHeightChange}/>
    </section>;
}

function ChannelTree({channels, participants, canKick, canBan, canDrag, selectedID, currentID, onSelect, onJoin, onMoveParticipant, onError}: {
    channels: ChannelDTO[];
    participants: ParticipantDTO[];
    canKick: boolean;
    canBan: boolean;
    canDrag: boolean;
    selectedID: string;
    currentID: string;
    onSelect: (id: string) => void;
    onJoin: (id: string) => void
    onMoveParticipant: (sessionID: string, channelID: string) => void;
    onError: (message: string) => void;
}) {
    const [draggingSessionID, setDraggingSessionID] = useState<string | null>(null);
    const [dropTargetID, setDropTargetID] = useState<string | null>(null);
    const children = useMemo(() => {
        return buildChannelGroups(channels);
    }, [channels]);
    const draggingParticipant = participants.find((participant) => participant.sessionId === draggingSessionID);
    const renderLevel = (parentID: string, depth: number): React.ReactNode => (children.get(parentID) ?? []).map((channel) => {
        const members = participants.filter((participant) => participant.channelId === channel.id);
        const canDrop = canMoveParticipant(draggingParticipant, channel.id, canDrag);
        return <div key={channel.id}>
            <button
                className={`channel-row ${selectedID === channel.id ? "selected" : ""} ${currentID === channel.id ? "current" : ""} ${canDrop && dropTargetID === channel.id ? "drop-target" : ""}`}
                style={{paddingLeft: 14 + depth * 18}} onClick={() => onSelect(channel.id)}
                title={canDrop ? "Переместить участника в этот канал" : !channel.canJoin ? "Нет доступа к каналу" : undefined}
                onDragOver={(event) => {
                    if (!canDrop) return;
                    event.preventDefault();
                    event.dataTransfer.dropEffect = "move";
                    if (dropTargetID !== channel.id) setDropTargetID(channel.id);
                }}
                onDragLeave={(event) => {
                    if (!(event.relatedTarget instanceof Node) || !event.currentTarget.contains(event.relatedTarget)) {
                        setDropTargetID((current) => current === channel.id ? null : current);
                    }
                }}
                onDrop={(event) => {
                    if (!canDrop || !draggingSessionID || event.dataTransfer.getData(participantDragType) !== draggingSessionID) return;
                    event.preventDefault();
                    setDropTargetID(null);
                    setDraggingSessionID(null);
                    onMoveParticipant(draggingSessionID, channel.id);
                }}
                onDoubleClick={() => onJoin(channel.id)}><span className="channel-icon" aria-hidden="true">⌁</span><span
                className="channel-name">{channel.name}</span><span className="channel-count">{members.length}</span>
            </button>
            {members.map((participant) => <ParticipantRow key={participant.sessionId} participant={participant}
                                                          channels={channels} canKick={canKick} canBan={canBan} canDrag={canDrag}
                                                          depth={depth} onError={onError}
                                                          onDragStart={(event, source) => {
                                                              if (!canDrag) {
                                                                  event.preventDefault();
                                                                  return;
                                                              }
                                                              event.dataTransfer.setData(participantDragType, source.sessionId);
                                                              event.dataTransfer.effectAllowed = "move";
                                                              setDraggingSessionID(source.sessionId);
                                                          }}
                                                          onDragEnd={() => { setDraggingSessionID(null); setDropTargetID(null); }}/>)}{renderLevel(channel.id, depth + 1)}</div>;
    });
    return <div className="channel-tree">{channels.length ? renderLevel("0", 0) :
        <div className="empty-state">Каналы ещё не загружены</div>}</div>;
}

function EventPanel({events, height, onHeightChange}: {
    events: ClientEventDTO[];
    height: number;
    onHeightChange: (height: number | ((current: number) => number)) => void
}) {
    const listRef = useRef<HTMLDivElement>(null);
    const dockRef = useRef<HTMLDivElement>(null);
    const stickToBottom = useRef(true);
    const dragRef = useRef<{ startY: number; startHeight: number; maxHeight: number } | null>(null);
    useEffect(() => {
        if (stickToBottom.current && listRef.current) listRef.current.scrollTop = listRef.current.scrollHeight;
    }, [events]);
    useEffect(() => {
        const move = (event: PointerEvent) => {
            const drag = dragRef.current;
            if (!drag) return;
            onHeightChange(Math.max(135, Math.min(drag.maxHeight, drag.startHeight + drag.startY - event.clientY)));
        };
        const stop = () => {
            if (!dragRef.current) return;
            dragRef.current = null;
            document.body.style.removeProperty("cursor");
            document.body.style.removeProperty("user-select");
        };
        window.addEventListener("pointermove", move);
        window.addEventListener("pointerup", stop);
        window.addEventListener("pointercancel", stop);
        return () => {
            stop();
            window.removeEventListener("pointermove", move);
            window.removeEventListener("pointerup", stop);
            window.removeEventListener("pointercancel", stop);
        };
    }, [onHeightChange]);
    useEffect(() => {
        const page = dockRef.current?.parentElement;
        if (!page) return;
        const observer = new ResizeObserver(() => onHeightChange((current) => Math.min(current, Math.max(135, page.clientHeight - 250))));
        observer.observe(page);
        return () => observer.disconnect();
    }, [onHeightChange]);
    const startResize = (event: React.PointerEvent<HTMLDivElement>) => {
        if (event.button !== 0) return;
        const page = dockRef.current?.parentElement;
        if (!page) return;
        event.preventDefault();
        dragRef.current = {
            startY: event.clientY,
            startHeight: height,
            maxHeight: Math.max(135, page.clientHeight - 250)
        };
        document.body.style.cursor = "ns-resize";
        document.body.style.userSelect = "none";
    };
    return <div className="event-dock" ref={dockRef} style={{height}}>
        <div className="event-resize-handle" role="separator" aria-label="Изменить высоту журнала событий"
             aria-orientation="horizontal" onPointerDown={startResize}></div>
        <section className="panel event-panel">
            <div className="event-list" ref={listRef} onScroll={(event) => {
                const element = event.currentTarget;
                stickToBottom.current = element.scrollHeight - element.scrollTop - element.clientHeight < 12;
            }}>{events.length === 0 ?
                <div className="empty-event">События появятся после подключения</div> : events.map((item) => <div
                    className={`event-row kind-${item.kind}`} key={item.sequence}>
                    <time>{new Date(item.time).toLocaleTimeString("ru-RU", {
                        hour: "2-digit",
                        minute: "2-digit",
                        second: "2-digit"
                    })}</time>
                    <span className="event-marker"/><span>{item.message}</span></div>)}</div>
        </section>
    </div>;
}

function SettingsPage({view, invoke, theme, setTheme}: {
    view: ClientViewDTO;
    invoke: (operation: () => Promise<unknown>) => Promise<void>;
    theme: string;
    setTheme: (value: string) => Promise<void>;
}) {
    const [sensitivity, setSensitivity] = useState(view.audio.vadSensitivity);
    useEffect(() => setSensitivity(view.audio.vadSensitivity), [view.audio.vadSensitivity]);
    const [rnnoiseSensitivity, setRNNoiseSensitivity] = useState(view.audio.rnnoiseSensitivity);
    useEffect(() => setRNNoiseSensitivity(view.audio.rnnoiseSensitivity), [view.audio.rnnoiseSensitivity]);
    const [devices, setDevices] = useState<AudioDevicesDTO | null>(null);
    const [devicesError, setDevicesError] = useState("");
    const [devicePending, setDevicePending] = useState(false);
    const loadDevices = useCallback(async () => {
        try {
            const next = await desktopAPI.audioDevices();
            next.capture ??= [];
            next.playback ??= [];
            setDevices(next);
            setDevicesError("");
        } catch (error) {
            setDevicesError(errorText(error));
        }
    }, []);
    useEffect(() => {
        void loadDevices();
    }, [loadDevices]);
    const selectDevice = async (kind: "capture" | "playback", id: string) => {
        if (devicePending) return;
        setDevicePending(true);
        setDevicesError("");
        try {
            if (kind === "capture") await desktopAPI.setCaptureDevice(id);
            else await desktopAPI.setPlaybackDevice(id);
            await loadDevices();
        } catch (error) {
            setDevicesError(errorText(error));
        } finally {
            setDevicePending(false);
        }
    };
    return <section className="settings-page">
        <div className="settings-heading"><p className="eyebrow">ПАРАМЕТРЫ КЛИЕНТА</p><h2>Настройки</h2></div>
        {devicesError && <div className="device-error" role="alert">Не удалось получить аудиоустройства: {devicesError}
            <button className="text-button" onClick={() => void loadDevices()}>Повторить</button>
        </div>}
        <section className="settings-card"><h3>Микрофон</h3><DeviceSelect id="capture-device"
                                                                          label="Устройство захвата звука"
                                                                          devices={devices?.capture ?? []}
                                                                          value={devices?.selectedCapture ?? ""}
                                                                          disabled={!devices || devicePending}
                                                                          onChange={(id) => selectDevice("capture", id)}/>
            {!view.audio.captureAvailable && <p className="device-hint">Микрофон не найден. Выберите устройство, когда оно появится. Пока его нет, вы остаётесь в канале без передачи голоса.</p>}
            <SettingToggle title="Шумоподавление"
                           description="Убирает постоянный фоновый шум до анализа голосовой активности."
                           checked={view.audio.rnnoiseEnabled}
                           onChange={(value) => invoke(() => desktopAPI.setRNNoiseEnabled(value))}/>
            <SensitivitySlider label="Интенсивность шумоподавления"
                               description="Чем выше значение, тем сильнее подавляется фоновый шум."
                               value={rnnoiseSensitivity} disabled={!view.audio.rnnoiseEnabled}
                               onChange={setRNNoiseSensitivity}
                               onCommit={(value) => invoke(() => desktopAPI.setRNNoiseSensitivity(value))}/>
            <SettingToggle title="Обнаружение голоса (VAD)"
                           description="Микрофон передаёт звук, когда обнаружена речь."
                           checked={view.audio.vadEnabled}
                           onChange={(value) => invoke(() => desktopAPI.setVADEnabled(value))}/>
            <ModeSelect value={view.audio.vadMode} disabled={!view.audio.vadEnabled}
                        open={view.audio.vadOpen}
                        onChange={(value) => invoke(() => desktopAPI.setVADMode(value))}/>
            <div className="sensitivity-setting">
                <div><span className="setting-label">Порог передачи звука</span>
                    <output>{Math.round((1 - sensitivity) * 100)}%</output>
                </div>
                <p>Чем выше значение, тем тише может быть речь, открывающая микрофон.</p></div>
            <AudioWaveform sensitivity={sensitivity} disabled={!view.audio.vadEnabled}
                           onSensitivityChange={setSensitivity}
                           onSensitivityCommit={(value) => invoke(() => desktopAPI.setVADSensitivity(value))}/>
        </section>
        <section className="settings-card compact-card"><h3>Воспроизведение</h3><DeviceSelect id="playback-device"
                                                                                              label="Устройство вывода звука"
                                                                                              devices={devices?.playback ?? []}
                                                                                              value={devices?.selectedPlayback ?? ""}
                                                                                              disabled={!devices || devicePending}
                                                                                              onChange={(id) => selectDevice("playback", id)}/><SettingToggle
            title="Заглушить звук" description="Входящий голос продолжает обрабатываться, но не воспроизводится."
            checked={view.audio.deafened} onChange={(value) => invoke(() => desktopAPI.setDeafened(value))}/></section>
        <section className="settings-card compact-card"><h3>Интерфейс</h3>
            <label className="theme-setting"><span className="setting-label">Тема оформления</span>
                <select value={theme} onChange={(event) => void invoke(() => setTheme(event.target.value))}>
                    <option value="system">Системная</option><option value="dark">Тёмная</option><option value="light">Светлая</option>
                </select><small>Системная тема следует настройкам Windows.</small>
            </label>
        </section>
    </section>;
}

function SensitivitySlider({label, description, value, disabled, onChange, onCommit}: {
    label: string;
    description: string;
    value: number;
    disabled: boolean;
    onChange: (value: number) => void;
    onCommit: (value: number) => Promise<void>;
}) {
    const commit = (value: string) => void onCommit(Number(value));
    return <div className={`sensitivity-setting slider-setting ${disabled ? "disabled" : ""}`}>
        <div><label className="setting-label" htmlFor="rnnoise-sensitivity">{label}</label>
            <output htmlFor="rnnoise-sensitivity">{Math.round(value * 100)}%</output>
        </div>
        <p>{description}</p>
        <input id="rnnoise-sensitivity" aria-label={label} type="range" min="0" max="1" step="0.01"
               value={value} disabled={disabled}
               onChange={(event) => onChange(Number(event.target.value))}
               onPointerUp={(event) => commit(event.currentTarget.value)}
               onKeyUp={(event) => commit(event.currentTarget.value)}/>
    </div>;
}

function AudioWaveform({sensitivity, disabled, onSensitivityChange, onSensitivityCommit}: {
    sensitivity: number;
    disabled: boolean;
    onSensitivityChange: (value: number) => void;
    onSensitivityCommit: (value: number) => Promise<void>
}) {
    const canvasRef = useRef<HTMLCanvasElement>(null);
    const thresholdRef = useRef<HTMLDivElement>(null);
    const targetRef = useRef({input: 0, processed: 0, transmitted: 0, updatedAt: 0});
    const thresholdPosition = 1 - sensitivity;
    const thresholdLevel = (40 - 35 * sensitivity) / 60;
    const thresholdLevelRef = useRef(thresholdLevel);
    const sensitivityFromPosition = (position: number) => 1 - position;

    useEffect(() => {
        thresholdLevelRef.current = thresholdLevel;
        thresholdRef.current?.style.setProperty("--threshold", `${thresholdPosition * 100}%`);
    }, [thresholdLevel, thresholdPosition]);

    useEffect(() => desktopAPI.onAudioMeter((sample) => {
        targetRef.current = {
            input: Math.max(0, Math.min(1, sample.input)),
            processed: Math.max(0, Math.min(1, sample.processed ?? sample.input)),
            transmitted: Math.max(0, Math.min(1, sample.transmitted)),
            updatedAt: performance.now(),
        };
    }), []);

    useEffect(() => {
        const canvas = canvasRef.current;
        if (!canvas) return;
        const context = canvas.getContext("2d");
        if (!context) return;
        const points = 96;
        const rejectedHistory = Array<number>(points).fill(0);
        const transmittedHistory = Array<number>(points).fill(0);
        let currentInput = 0;
        let currentProcessed = 0;
        let currentTransmitted = 0;
        let previousSample = performance.now();
        let animationFrame = 0;

        const resize = () => {
            const bounds = canvas.getBoundingClientRect();
            const scale = window.devicePixelRatio || 1;
            canvas.width = Math.max(1, Math.round(bounds.width * scale));
            canvas.height = Math.max(1, Math.round(bounds.height * scale));
            context.setTransform(scale, 0, 0, scale, 0, 0);
        };
        const observer = new ResizeObserver(resize);
        observer.observe(canvas);
        resize();

        const ribbon = (history: number[], center: number, height: number, fill: string, stroke: string) => {
            const width = canvas.clientWidth;
            const step = width / Math.max(1, history.length - 1);
            context.beginPath();
            history.forEach((value, index) => {
                const y = center - Math.max(0.6, value * height);
                if (index === 0) context.moveTo(0, y);
                else context.lineTo(index * step, y);
            });
            for (let index = history.length - 1; index >= 0; index--) {
                context.lineTo(index * step, center + Math.max(0.6, history[index] * height));
            }
            context.closePath();
            context.fillStyle = fill;
            context.fill();
            context.strokeStyle = stroke;
            context.lineWidth = 1;
            context.stroke();
        };

        const draw = (now: number) => {
            const target = targetRef.current;
            const stale = now - target.updatedAt > 120;
            const inputTarget = stale ? 0 : target.input;
            const processedTarget = stale ? 0 : target.processed;
            const transmittedTarget = stale ? 0 : target.transmitted;
            currentInput += (inputTarget - currentInput) * (inputTarget > currentInput ? 0.42 : 0.16);
            currentProcessed += (processedTarget - currentProcessed) * (processedTarget > currentProcessed ? 0.45 : 0.035);
            currentTransmitted += (transmittedTarget - currentTransmitted) * (transmittedTarget > currentTransmitted ? 0.48 : 0.2);
            if (thresholdRef.current) {
                const levelOnThresholdScale = Math.max(0, Math.min(1, (currentProcessed - 1 / 12) / (7 / 12)));
                thresholdRef.current.style.setProperty("--level", `${levelOnThresholdScale * 100}%`);
                thresholdRef.current.classList.toggle("open", processedTarget >= thresholdLevelRef.current && processedTarget > 0.003);
            }
            if (now - previousSample >= 25) {
                const rejected = Math.max(0, currentInput - currentTransmitted);
                rejectedHistory.shift();
                rejectedHistory.push(rejected);
                transmittedHistory.shift();
                transmittedHistory.push(currentTransmitted);
                previousSample = now;
            }
            const width = canvas.clientWidth;
            const height = canvas.clientHeight;
            context.clearRect(0, 0, width, height);
            const waveCenter = height * 0.5;
            context.beginPath();
            context.moveTo(0, waveCenter);
            context.lineTo(width, waveCenter);
            context.strokeStyle = "rgba(126, 143, 174, .10)";
            context.lineWidth = 1;
            context.stroke();
            ribbon(rejectedHistory, waveCenter, height * 0.38, "rgba(126, 143, 174, .18)", "rgba(151, 166, 193, .34)");
            ribbon(transmittedHistory, waveCenter, height * 0.38, "rgba(19, 231, 176, .24)", "rgba(28, 240, 184, .9)");
            animationFrame = requestAnimationFrame(draw);
        };
        animationFrame = requestAnimationFrame(draw);
        return () => {
            cancelAnimationFrame(animationFrame);
            observer.disconnect();
        };
    }, []);

    const updateThreshold = (position: number, commit: boolean) => {
        const value = sensitivityFromPosition(position);
        onSensitivityChange(value);
        if (commit) void onSensitivityCommit(value);
    };
    return <>
        <div className={`threshold-meter ${disabled ? "disabled" : ""}`} ref={thresholdRef}><span
            className="threshold-track"><span className="threshold-level"/></span><span className="threshold-marker"
                                                                                        aria-hidden="true"/><input
            aria-label="Порог голосовой активности" type="range" min="0" max="1" step="0.01" value={thresholdPosition}
            disabled={disabled} onChange={(event) => updateThreshold(Number(event.target.value), false)}
            onPointerUp={(event) => updateThreshold(Number(event.currentTarget.value), true)}
            onKeyUp={(event) => updateThreshold(Number(event.currentTarget.value), true)}/></div>
        <div className="audio-waveform">
            <div className="audio-waveform-heading"><span className="setting-label">Активность микрофона</span><span
                className="wave-legend"><i className="transmitted"/>Передаётся<i className="rejected"/>Отсечено</span>
            </div>
            <canvas ref={canvasRef} role="img" aria-label="Индикатор передаваемого и отсечённого звука"/>
        </div>
    </>;
}

function DeviceSelect({id, label, devices, value, disabled, onChange}: {
    id: string;
    label: string;
    devices: AudioDeviceDTO[];
    value: string;
    disabled: boolean;
    onChange: (id: string) => Promise<void>
}) {
    const [open, setOpen] = useState(false);
    const selected = devices.find((device) => device.id === value);
    const selectedName = selected ? `${selected.name}${selected.isDefault ? " — системное по умолчанию" : ""}` : "Системное устройство по умолчанию";
    useEffect(() => {
        if (!open) return;
        const closeOnEscape = (event: KeyboardEvent) => {
            if (event.key === "Escape") setOpen(false);
        };
        window.addEventListener("keydown", closeOnEscape);
        return () => {
            window.removeEventListener("keydown", closeOnEscape);
        };
    }, [open]);
    const choose = (deviceID: string) => {
        setOpen(false);
        if (deviceID !== value) void onChange(deviceID);
    };
    return <div className="device-select"><span className="setting-label" id={`${id}-label`}>{label}</span>
        <div className="device-select-control">
            <button id={id} className="device-select-trigger" type="button" disabled={disabled}
                    aria-labelledby={`${id}-label ${id}`} aria-haspopup="listbox" aria-expanded={open}
                    onClick={() => setOpen((current) => !current)}><span>{selectedName}</span></button>
            {open && <div className="device-options" role="listbox" aria-labelledby={`${id}-label`}>
                <button type="button" role="option" aria-selected={value === ""}
                        className={value === "" ? "selected" : ""} onClick={() => choose("")}>Системное устройство по
                    умолчанию
                </button>
                {devices.map((device) => <button type="button" role="option" aria-selected={device.id === value}
                                                 className={device.id === value ? "selected" : ""}
                                                 onClick={() => choose(device.id)}
                                                 key={device.id}>{device.name}{device.isDefault ?
                    <small>Системное по умолчанию</small> : null}</button>)}</div>}</div>
    </div>;
}

function ModeSelect({value, disabled, open, onChange}: {
    value: string;
    disabled: boolean;
    open: boolean;
    onChange: (value: string) => Promise<void>;
}) {
    const [optionsOpen, setOptionsOpen] = useState(false);
    const options = [
        {value: "level", label: "По громкости"},
        {value: "vad", label: "Распознавание речи"},
        {value: "hybrid", label: "Гибридный"},
    ];
    const selectedName = options.find((option) => option.value === value)?.label ?? value;
    useEffect(() => {
        if (!optionsOpen) return;
        const closeOnEscape = (event: KeyboardEvent) => {
            if (event.key === "Escape") setOptionsOpen(false);
        };
        window.addEventListener("keydown", closeOnEscape);
        return () => window.removeEventListener("keydown", closeOnEscape);
    }, [optionsOpen]);
    const choose = (nextValue: string) => {
        setOptionsOpen(false);
        if (nextValue !== value) void onChange(nextValue);
    };
    return <div className="device-select mode-select">
        <div className="mode-select-heading"><span className="setting-label" id="vad-mode-label">Режим</span><span
            className={`gate-indicator ${open ? "open" : ""}`}>{open ? "Передача" : "Ожидание речи"}</span></div>
        <div className="device-select-control">
            <button id="vad-mode" className="device-select-trigger" type="button" disabled={disabled}
                    aria-labelledby="vad-mode-label vad-mode" aria-haspopup="listbox" aria-expanded={optionsOpen}
                    onClick={() => setOptionsOpen((current) => !current)}><span>{selectedName}</span></button>
            {optionsOpen && <div className="device-options" role="listbox" aria-labelledby="vad-mode-label">
                {options.map((option) => <button type="button" role="option" aria-selected={option.value === value}
                                                 className={option.value === value ? "selected" : ""}
                                                 onClick={() => choose(option.value)}
                                                 key={option.value}>{option.label}</button>)}
            </div>}
        </div>
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

export default App;
