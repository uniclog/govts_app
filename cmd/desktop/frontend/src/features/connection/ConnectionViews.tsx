import {useEffect, useState} from "react";
import type {ClientViewDTO} from "../../api";
import {desktopAPI} from "../../api";
import {ConnectionStatsPopup} from "./ConnectionStatsPopup";

export type Page = "channels" | "settings";
const serverAddressStorageKey = "govts.serverAddress";

function savedServerAddress(): string {
    try { return localStorage.getItem(serverAddressStorageKey) || "127.0.0.1:9000"; }
    catch { return "127.0.0.1:9000"; }
}

function rememberServerAddress(value: string) {
    try { localStorage.setItem(serverAddressStorageKey, value); }
    catch { /* Keep the controlled input value for this session. */ }
}

function errorText(error: unknown): string {
    return (error instanceof Error ? error.message : String(error)).replace(/^Error:\s*/, "");
}

export function ConnectionPage({view, error, onError, onRefresh, onClearEvents, onConnected}: {
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
    useEffect(() => {
        let active = true;
        void desktopAPI.savedDisplayName()
            .then((savedName) => { if (active) setName((current) => current || savedName); })
            .catch((loadError) => { if (active) onError(errorText(loadError)); });
        return () => { active = false; };
    }, [onError]);
    const submit = async (event: React.FormEvent) => {
        event.preventDefault();
        if (pending) return;
        const address = server.trim();
        setServer(address);
        rememberServerAddress(address);
        onError("");
        setPending(true);
        try {
            onClearEvents();
            await desktopAPI.connect({name, server: address});
            await onRefresh();
            onConnected();
        } catch (connectError) {
            onError(errorText(connectError));
        } finally {
            setPending(false);
        }
    };
    return <main className="connection-page"><section className="connection-card">
        <div className="connection-logo">G</div>
        <p className="eyebrow">GOVTS DESKTOP</p><h1>Подключение к серверу</h1>
        <p className="lead">Введите адрес голосового сервера и имя, под которым вас увидят другие участники.</p>
        <form onSubmit={submit}>
            <label><span>Адрес сервера</span><input autoFocus value={server} onChange={(event) => {
                setServer(event.target.value); rememberServerAddress(event.target.value);
            }} placeholder="192.0.2.1:9000" spellCheck={false}/></label>
            <label><span>Отображаемое имя</span><input value={name} onChange={(event) => setName(event.target.value)}
                placeholder="Ваше имя" maxLength={64}/></label>
            {(error || view.lastError) && <div className="form-error" role="alert">{error || view.lastError}</div>}
            <button className="primary-button" disabled={pending || !server.trim() || !name.trim()}
                type="submit">{pending ? "Подключаемся…" : "Подключиться"}</button>
        </form><p></p>
    </section></main>;
}

export function StatusBar({view, page, onPageChange, invoke}: {
    view: ClientViewDTO;
    page: Page;
    onPageChange: (page: Page) => void;
    invoke: (operation: () => Promise<unknown>) => Promise<void>;
}) {
    return <header className="status-bar">
        <button className="header-nav-button" onClick={() => onPageChange(page === "settings" ? "channels" : "settings")}>
            <span aria-hidden="true">{page === "settings" ? "←" : "⚙"}</span><span>{page === "settings" ? "К каналам" : "Настройки"}</span>
        </button>
        <div className="server-summary"><p className="eyebrow">СЕРВЕР</p><h1>{view.server.name || "Govts"}</h1></div>
        <div className="audio-control-island" role="group" aria-label="Управление звуком">
            <button className={`voice-control ${view.audio.muted || !view.audio.captureAvailable ? "active" : ""}`}
                aria-pressed={view.audio.muted} disabled={!view.audio.captureAvailable}
                onClick={() => void invoke(() => desktopAPI.setMuted(!view.audio.muted))}>{!view.audio.captureAvailable ? "Нет микрофона" : view.audio.muted ? "Микрофон выкл." : "Микрофон"}</button>
            <button className={`voice-control ${view.audio.deafened ? "active" : ""}`} aria-pressed={view.audio.deafened}
                onClick={() => void invoke(() => desktopAPI.setDeafened(!view.audio.deafened))}>{view.audio.deafened ? "Звук выкл." : "Звук"}</button>
        </div>
        <ConnectionStatsPopup sessionId={view.sessionId} status={view.connectionStatus}/>
        {!view.snapshotFresh && <div className="sync-pill">Синхронизация…</div>}
        <button className="header-nav-button disconnect-button" onClick={() => void invoke(() => desktopAPI.disconnect())}>
            <span aria-hidden="true">↪</span><span>Отключиться</span>
        </button>
    </header>;
}
