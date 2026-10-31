import {useEffect, useRef, useState} from "react";
import type {ClientEventDTO, ClientViewDTO} from "../../api";
import {desktopAPI} from "../../api";
import {errorText} from "../../locales";
import {useLocale} from "../../locales/Locale";
import {Icon} from "../../components/Icon";
import {EventPanel} from "./EventPanel";

export function ConnectionProgress({view, events, onDisconnect}: {
    onDisconnect: () => Promise<void>;
    view: ClientViewDTO;
    events: ClientEventDTO[];
}) {
    const {m} = useLocale();
    const cancelling = useRef(false);
    const [pending, setPending] = useState(false);
    const [trustError, setTrustError] = useState("");
    const change = view.serverIdentityChange;
    const trust = async () => {
        if (!change || cancelling.current) return;
        cancelling.current = true; setPending(true); setTrustError("");
        try { await desktopAPI.trustServerIdentity(change.endpoint, change.newFingerprint); }
        catch (error) { setTrustError(errorText(error, m)); }
        finally { cancelling.current = false; setPending(false); }
    };
    useEffect(() => { setTrustError(""); }, [change?.endpoint, change?.newFingerprint]);
    const cancel = async () => {
        if (cancelling.current) return;
        cancelling.current = true; setPending(true);
        try { await onDisconnect(); }
        finally { cancelling.current = false; setPending(false); }
    };
    const [startedAt] = useState(Date.now);
    const [elapsed, setElapsed] = useState(0);
    const connected = view.connectionStatus === "connected";
    const reconnecting = view.connectionStatus === "reconnecting";
    useEffect(() => {
        if (connected && view.snapshotFresh) return;
        const timer = window.setInterval(() => setElapsed(Math.floor((Date.now() - startedAt) / 1000)), 1000);
        return () => window.clearInterval(timer);
    }, [startedAt, connected, view.snapshotFresh]);
    return <section className="connection-progress-wrap" aria-label={m.connection.statsRegion}>
        <div className="connection-progress">
            <div role="status" className="connection-progress-heading">
                <h1><span className={connected ? "connection-progress-dot connected" : "connection-progress-dot"} aria-hidden="true"/>{change ? m.connection.serverKeyChanged : connected ? (view.snapshotFresh ? m.channels.waitingChannel : m.connection.receivingState) : reconnecting ? m.connection.connectionLost : m.connection.awaitingServer}</h1>
                <p>{change ? m.connection.serverKeyChangedHint : connected ? (view.snapshotFresh ? m.connection.selectChannelHint : m.connection.receivingStateHint) : reconnecting ? m.connection.connectionLostHint : m.connection.waitingServerHint}</p>
            </div>
            {change && <dl className="server-key-change">
                <dt>{m.connection.server}</dt><dd>{change.endpoint}</dd>
                <dt>{m.connection.previousServerKey}</dt><dd><code>{change.previousFingerprint}</code></dd>
                <dt>{m.connection.newServerKey}</dt><dd><code>{change.newFingerprint}</code></dd>
            </dl>}
            {!change && (!connected || !view.snapshotFresh) && <span className="connection-progress-elapsed">{m.connection.elapsed} {Math.floor(elapsed / 60)}:{String(elapsed % 60).padStart(2, "0")}</span>}
            <div className="connection-progress-actions"><button type="button" className="connection-cancel" disabled={pending} aria-busy={pending} onClick={() => void cancel()}><Icon name="disconnect"/>{connected ? m.connection.disconnect : m.connection.cancelConnection}</button>
                {change && <button type="button" className="primary-button" disabled={pending} aria-busy={pending} onClick={() => void trust()}>{m.connection.trustNewServerKey}</button>}
            </div>
            {trustError && <div className="form-error" role="alert">{trustError}</div>}
            {view.lastError && <div className="form-error" role="alert">{errorText(view.lastError, m)}</div>}
        </div>
        <div className="connection-progress-log"><h2>{m.channels.eventLog}</h2><EventPanel events={events} active/></div>
    </section>;
}
