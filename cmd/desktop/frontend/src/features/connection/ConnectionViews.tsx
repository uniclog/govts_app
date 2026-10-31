import {useEffect, useRef, useState} from "react";
import type {ReactNode} from "react";
import type {ClientViewDTO} from "../../api";
import {desktopAPI} from "../../api";
import {ConnectionStatsPopup} from "./ConnectionStatsPopup";
import {RecentServers} from "./RecentServers";
import {Icon} from "../../components/Icon";
import {errorCode, errorText, rawError} from "../../locales";
import {LocaleSwitch, useLocale} from "../../locales/Locale";
import type {ScreenSharingState} from "../screen/ScreenShareDialog";
import type {ScreenMediaController} from "../screen/screenMedia";

export type Page = "channels" | "settings" | "admin";
const serverAddressStorageKey = "sonoryx.serverAddress";
const rememberAddressStorageKey = "sonoryx.rememberServerAddress";

function shouldRememberAddress(): boolean {
    try { return localStorage.getItem(rememberAddressStorageKey) !== "false"; }
    catch { return true; }
}

function savedServerAddress(): string {
    if (!shouldRememberAddress()) return "";
    try { return localStorage.getItem(serverAddressStorageKey) || ""; }
    catch { return ""; }
}

export function rememberServerAddress(value: string) {
    try {
        if (shouldRememberAddress()) localStorage.setItem(serverAddressStorageKey, value);
        else localStorage.removeItem(serverAddressStorageKey);
    }
    catch { /* Keep the controlled input value for this session. */ }
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
    const {m} = useLocale();
    const [server, setServer] = useState(savedServerAddress);
    const [name, setName] = useState("");
    const [pending, setPending] = useState(false);
    const [remember, setRemember] = useState(shouldRememberAddress);
    const [invitation, setInvitation] = useState("");
    const [invitationError, setInvitationError] = useState("");
    const connecting = useRef(false);
    useEffect(() => {
        let active = true;
        void desktopAPI.savedDisplayName()
            .then((savedName) => { if (active) setName((current) => current || savedName); })
            .catch((loadError) => { if (active) onError(rawError(loadError)); });
        return () => { active = false; };
    }, [onError]);
    const connect = async (value: string) => {
        if (connecting.current) return;
        const address = value.trim().replace(/^srx:\/\//i, "");
        setServer(address);
        rememberServerAddress(address);
        onError("");
        if (!name.trim()) { onError(errorCode("name-required")); return; }
        connecting.current = true;
        setPending(true);
        try {
            onClearEvents();
            await desktopAPI.connect({name, server: address, skipHistory: !remember});
            await onRefresh();
            onConnected();
        } catch (connectError) {
            onError(rawError(connectError));
        } finally {
            connecting.current = false;
            setPending(false);
        }
    };
    const selectServer = (address: string) => { setServer(address); rememberServerAddress(address); };
    const applyInvitation = (value: string) => {
        setInvitation(value);
        const address = value.trim().replace(/^srx:\/\//i, "");
        // Only direct server addresses are supported; invite tokens need a resolver.
        if (!address || /[\s/?#@]/.test(address) || !/^(?:\[[\da-f:]+\]|[\p{L}\d._-]+):\d{1,5}$/iu.test(address)) {
            setInvitationError(m.connection.inviteUnsupported);
            return;
        }
        setInvitationError("");
        selectServer(address);
        document.getElementById("display-name")?.focus();
    };
    const pasteInvitation = async () => {
        if (invitation.trim()) { applyInvitation(invitation); return; }
        try { applyInvitation(await navigator.clipboard.readText()); }
        catch { setInvitationError(m.connection.pasteHint); }
    };
    return <div className="connection-page"><div className="connection-layout">
        <section className="connection-card connection-hero">
        <div className="connection-rings" aria-hidden="true"><i/><i/><i/><i/></div>
        <div className="connection-content">
        <div className="connection-brand"><span className="connection-logo"><VoiceMark/></span><strong>SRX</strong></div>
        <h1>{m.connection.heroLineOne}<br/>{m.connection.heroLineTwo}</h1>
        <p className="lead">{m.connection.heroLead}</p>
        <form className="connection-form" onSubmit={(event) => { event.preventDefault(); void connect(server); }}>
            <label><span>{m.connection.address}</span><span className="connection-input"><Icon name="globe"/><input id="server-address" autoFocus value={server} onChange={(event) => {
                setServer(event.target.value); rememberServerAddress(event.target.value);
            }} placeholder={m.connection.addressPlaceholder} spellCheck={false} autoCapitalize="none" disabled={pending}/></span></label>
            <label><span>{m.connection.nickname}</span><span className="connection-input"><Icon name="users"/><input id="display-name" value={name} onChange={(event) => setName(event.target.value)}
                placeholder={m.connection.namePlaceholder} maxLength={64} disabled={pending}/></span></label>
            <label title={m.connection.passwordUnsupported}><span>{m.connection.password}</span><span className="connection-input"><Icon name="lock"/><input id="server-password" type="password" disabled placeholder={m.connection.passwordPlaceholder}/></span></label>
            <label className="connection-remember" title={m.connection.rememberHint}><input type="checkbox" checked={remember} onChange={(event) => {
                const checked = event.target.checked;
                setRemember(checked);
                try { localStorage.setItem(rememberAddressStorageKey, String(checked)); } catch { /* Session-only preference. */ }
                if (!checked) { try { localStorage.removeItem(serverAddressStorageKey); } catch { /* Storage unavailable. */ } }
                else rememberServerAddress(server);
            }}/><span className="connection-switch" aria-hidden="true"/><span>{m.connection.remember}</span></label>
            {(error || view.lastError) && <div className="form-error" role="alert">{errorText(error || view.lastError, m)}</div>}
            <button className="primary-button" disabled={pending || !server.trim() || !name.trim()}
                type="submit"><Icon name="arrowRight"/>{pending ? m.connection.connecting : m.connection.connect}</button>
        </form>
        </div>
        </section>
        <aside className="connection-card connection-server-card">
            <RecentServers view={view} expanded={true} onToggle={() => {}} standalone disabled={pending} selectedAddress={server.replace(/^srx:\/\//i, "").trim()}
                refreshingServers={refreshingServers} onRefreshServers={onRefreshServers}
                onSelect={selectServer} onReconnect={connect} onError={onError}/>
            <form className="connection-invitation" onSubmit={(event) => { event.preventDefault(); void pasteInvitation(); }}>
                <label htmlFor="server-invitation">{m.connection.inviteLabel}</label>
                <div className="connection-invitation-row"><span className="connection-input"><Icon name="globe"/><input id="server-invitation" value={invitation} onChange={(event) => { setInvitation(event.target.value); setInvitationError(""); }} placeholder="srx://host:port" spellCheck={false} disabled={pending}/></span>
                <button type="submit" disabled={pending}>{m.connection.paste}</button></div>
                {invitationError && <p className="connection-invite-error" role="alert">{invitationError}</p>}
            </form>
        </aside>
    </div></div>;
}

function VoiceMark() {
    return <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" aria-hidden="true"><circle cx="6" cy="12" r="1.5" fill="currentColor" stroke="none"/><path d="M11 7a7 7 0 0 1 0 10M15 3a12 12 0 0 1 0 18"/></svg>;
}

// LobbyBar is the header shown before joining a server: client version,
// update notice and settings, without any session controls.
export function LobbyBar({version, updateAction, onPageChange}: {
    version?: string;
    updateAction?: ReactNode;
    onPageChange: (page: Page) => void;
}) {
    const {m} = useLocale();
    return <header className="status-bar lobby-bar">
        <div className="app-brand"><span className="brand-mark"><VoiceMark/></span><strong>SRX — {m.connection.lobbyTitle}</strong>{version && <span className="server-version" aria-label={m.connection.clientVersion(version)}>{version}</span>}{updateAction}</div>
        <LocaleSwitch variant="header"/>
        <button id="open-settings" className="status-pill settings-pill" type="button" onClick={() => onPageChange("settings")}>
            <Icon name="settings"/><span>{m.connection.settings}</span>
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
    const {m} = useLocale();
    return <header className="status-bar">
        <div className="app-brand"><span className="brand-mark"><Icon name="server"/></span><strong title={view.server.name}>{view.server.name || m.connection.server}</strong>{view.server.version && <span className="server-version" aria-label={m.connection.serverVersion(view.server.version)}>{view.server.version}</span>}{updateAction}</div>
        <div className={`sync-pill ${view.snapshotFresh ? "synced" : ""}`} aria-hidden={view.snapshotFresh}>{m.connection.syncing}</div>
        <ConnectionStatsPopup sessionId={view.sessionId} status={view.connectionStatus} channelId={view.channelId} sharing={sharing} screenMedia={screenMedia}/>
        {view.canAdmin && <button id="open-admin" className="status-pill settings-pill admin-pill" type="button" onClick={() => onPageChange("admin")}>
            <Icon name="shield"/><span>{m.admin.open}</span>
        </button>}
        <button className="status-pill settings-pill disconnect-button" type="button" title={m.connection.disconnect}
            onClick={() => void invoke(() => desktopAPI.disconnect())}><Icon name="hangup"/><span>{m.connection.leave}</span></button>
        <button id="open-settings" className="status-pill settings-pill" type="button" onClick={() => onPageChange("settings")}>
            <Icon name="settings"/><span>{m.connection.settings}</span>
        </button>
    </header>;
}

export function AudioControls({view, invoke, sharing}: {
    view: ClientViewDTO;
    invoke: (operation: () => Promise<unknown>) => Promise<void>;
    sharing: ScreenSharingState;
}) {
    const {m} = useLocale();
    const away = (view.participants ?? []).some((participant) => participant.local && participant.away);
    return <div className="audio-control-island" role="group" aria-label={m.connection.audioGroup}>
            <button className={`voice-control ${view.audio.muted || !view.audio.captureAvailable ? "active" : ""}`}
                aria-pressed={view.audio.muted} disabled={!view.audio.captureAvailable}
                title={!view.audio.captureAvailable ? m.connection.noMic : view.audio.muted ? m.connection.micOn : m.connection.micOff}
                onClick={() => void invoke(() => desktopAPI.setMuted(!view.audio.muted))}><Icon name="mic"/><span>{m.connection.mic}</span></button>
            <button className={`voice-control ${view.audio.deafened ? "active" : ""}`} aria-pressed={view.audio.deafened}
                title={view.audio.deafened ? m.connection.soundOn : m.connection.soundOff}
                onClick={() => void invoke(() => desktopAPI.setDeafened(!view.audio.deafened))}><Icon name="sound"/><span>{m.connection.audio}</span></button>
            <button className={`voice-control away-control ${away ? "active" : ""}`}
                aria-pressed={away}
                disabled={view.connectionStatus !== "connected" || view.channelId === "0"}
                title={away ? m.connection.hereHint : m.connection.awayHint}
                onClick={() => void invoke(() => desktopAPI.setAway(!away))}>
                <Icon name={away ? "here" : "away"}/><span>{away ? m.connection.here : m.connection.away}</span>
            </button>
        <button ref={sharing.buttonRef} className={`voice-control screen-share-button ${sharing.active ? "sharing" : ""}`}
            title={sharing.active ? m.connection.stopShare : m.connection.startShare} aria-pressed={sharing.active}
            aria-haspopup={sharing.active ? undefined : "dialog"} disabled={sharing.pending || !sharing.available}
            onClick={() => void sharing.toggle()}><Icon name="screen"/><span>{sharing.active ? m.connection.stopShare : m.connection.sharing}</span></button>
    </div>;
}
