import {useEffect, useRef, useState} from "react";
import type {ReactNode} from "react";
import type {ClientViewDTO} from "../../api";
import {desktopAPI} from "../../api";
import {ConnectionStatsPopup} from "./ConnectionStatsPopup";
import {RecentServers} from "./RecentServers";
import {Icon} from "../../components/Icon";
import type {ScreenSharingState} from "../screen/ScreenShareDialog";
import type {ScreenMediaController} from "../screen/screenMedia";

export type Page = "channels" | "settings";
const serverAddressStorageKey = "govts.serverAddress";

function savedServerAddress(): string {
    try { return localStorage.getItem(serverAddressStorageKey) || "127.0.0.1:9000"; }
    catch { return "127.0.0.1:9000"; }
}

export function rememberServerAddress(value: string) {
    try { localStorage.setItem(serverAddressStorageKey, value); }
    catch { /* Keep the controlled input value for this session. */ }
}

function errorText(error: unknown): string {
    return (error instanceof Error ? error.message : String(error)).replace(/^Error:\s*/, "");
}

export function ConnectionPage({view, error, onError, onRefresh, onClearEvents, onConnected, refreshingServers, onRefreshServers}: {
    refreshingServers: boolean;
    onRefreshServers: () => void;
    view: ClientViewDTO;
    error: string;
    onError: (value: string) => void;
    onRefresh: () => Promise<void>;
    onClearEvents: () => void;
    onConnected: () => void;
}) {
    const [server, setServer] = useState(savedServerAddress);
    const [name, setName] = useState("");
    const [pending, setPending] = useState(false);
    const [serverCount, setServerCount] = useState(0);
    const connecting = useRef(false);
    useEffect(() => {
        let active = true;
        void desktopAPI.savedDisplayName()
            .then((savedName) => { if (active) setName((current) => current || savedName); })
            .catch((loadError) => { if (active) onError(errorText(loadError)); });
        return () => { active = false; };
    }, [onError]);
    const connect = async (value: string) => {
        if (connecting.current) return;
        const address = value.trim();
        setServer(address);
        rememberServerAddress(address);
        onError("");
        if (!name.trim()) { onError("Введите отображаемое имя"); return; }
        connecting.current = true;
        setPending(true);
        try {
            onClearEvents();
            await desktopAPI.connect({name, server: address});
            await onRefresh();
            onConnected();
        } catch (connectError) {
            onError(errorText(connectError));
        } finally {
            connecting.current = false;
            setPending(false);
        }
    };
    const selectServer = (address: string) => { setServer(address); rememberServerAddress(address); };
    return <div className="connection-page"><div className={`connection-layout ${serverCount === 0 ? "connection-layout-empty" : ""}`}>
        <section className="connection-card connection-server-card" hidden={serverCount === 0}>
            <RecentServers view={view} expanded={true} onToggle={() => {}} standalone disabled={pending}
                refreshingServers={refreshingServers} onRefreshServers={onRefreshServers}
                onSelect={selectServer} onReconnect={connect} onError={onError} onCountChange={setServerCount}/>
        </section>
        <section className="connection-card">
        <div className="connection-logo">GTS</div>
        <h1>Подключение к серверу</h1>
        <p className="lead">Введите адрес голосового сервера и имя, под которым вас увидят другие участники.</p>
        <form onSubmit={(event) => { event.preventDefault(); void connect(server); }}>
            <label><span>Адрес сервера</span><input autoFocus value={server} onChange={(event) => {
                setServer(event.target.value); rememberServerAddress(event.target.value);
            }} placeholder="192.0.2.1:9000" spellCheck={false}/></label>
            <label><span>Отображаемое имя</span><input value={name} onChange={(event) => setName(event.target.value)}
                placeholder="Ваше имя" maxLength={64}/></label>
            {(error || view.lastError) && <div className="form-error" role="alert">{error || view.lastError}</div>}
            <button className="primary-button" disabled={pending || !server.trim() || !name.trim()}
                type="submit">{pending ? "Подключаемся…" : "Подключиться"}</button>
        </form><p></p>
    </section></div></div>;
}

// LobbyBar is the header shown before joining a server: client version,
// update notice and settings, without any session controls.
export function LobbyBar({version, updateAction, onPageChange}: {
    version?: string;
    updateAction?: ReactNode;
    onPageChange: (page: Page) => void;
}) {
    return <header className="status-bar">
        <div className="app-brand"><span className="brand-mark"><Icon name="server"/></span><strong>Govts</strong>{version && <span className="server-version" aria-label={`Версия клиента: ${version}`}>{version}</span>}{updateAction}</div>
        <button id="open-settings" className="status-pill settings-pill" type="button" onClick={() => onPageChange("settings")}>
            <Icon name="settings"/><span>Настройки</span>
        </button>
    </header>;
}

export function StatusBar({view, onPageChange, sharing, screenMedia, updateAction, invoke}: {
    invoke: (operation: () => Promise<unknown>) => Promise<void>;
    updateAction?: ReactNode;
    view: ClientViewDTO;
    onPageChange: (page: Page) => void;
    sharing: ScreenSharingState;
    screenMedia: ScreenMediaController;
}) {
    return <header className="status-bar">
        <div className="app-brand"><span className="brand-mark"><Icon name="server"/></span><strong title={view.server.name}>{view.server.name || "Сервер"}</strong>{view.server.version && <span className="server-version" aria-label={`Версия сервера: ${view.server.version}`}>{view.server.version}</span>}{updateAction}</div>
        <div className={`sync-pill ${view.snapshotFresh ? "synced" : ""}`} aria-hidden={view.snapshotFresh}>Синхронизация…</div>
        <ConnectionStatsPopup sessionId={view.sessionId} status={view.connectionStatus} channelId={view.channelId} sharing={sharing} screenMedia={screenMedia}/>
        <button id="open-settings" className="status-pill settings-pill" type="button" onClick={() => onPageChange("settings")}>
            <Icon name="settings"/><span>Настройки</span>
        </button>
        <button className="status-pill settings-pill disconnect-button" type="button" title="Отключиться от сервера"
            onClick={() => void invoke(() => desktopAPI.disconnect())}><Icon name="hangup"/><span>Выйти</span></button>
    </header>;
}

export function AudioControls({view, invoke, sharing}: {
    view: ClientViewDTO;
    invoke: (operation: () => Promise<unknown>) => Promise<void>;
    sharing: ScreenSharingState;
}) {
    return <div className="audio-control-island" role="group" aria-label="Управление звуком">
            <button className={`voice-control ${view.audio.muted || !view.audio.captureAvailable ? "active" : ""}`}
                aria-pressed={view.audio.muted} disabled={!view.audio.captureAvailable}
                title={!view.audio.captureAvailable ? "Нет микрофона" : view.audio.muted ? "Включить микрофон" : "Выключить микрофон"}
                onClick={() => void invoke(() => desktopAPI.setMuted(!view.audio.muted))}><Icon name="mic"/>Микрофон</button>
            <button className={`voice-control ${view.audio.deafened ? "active" : ""}`} aria-pressed={view.audio.deafened}
                title={view.audio.deafened ? "Включить звук" : "Выключить звук"}
                onClick={() => void invoke(() => desktopAPI.setDeafened(!view.audio.deafened))}><Icon name="sound"/>Аудио</button>
        <button ref={sharing.buttonRef} className={`voice-control screen-share-button ${sharing.active ? "sharing" : ""}`}
            title={sharing.active ? "Завершить демонстрацию" : "Демонстрация экрана"} aria-pressed={sharing.active}
            aria-haspopup={sharing.active ? undefined : "dialog"} disabled={sharing.pending || !sharing.available}
            onClick={() => void sharing.toggle()}><Icon name="screen"/>{sharing.active ? "Завершить демонстрацию" : "Демонстрация"}</button>
    </div>;
}
