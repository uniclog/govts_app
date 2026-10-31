import {useEffect, useRef, useState} from "react";
import type {MediaServerIdentity} from "../../api";
import {useLocale} from "../../locales/Locale";

// First use and replacement of a media key require explicit confirmation.

type PendingTrust = {identity: MediaServerIdentity; resolve: (accepted: boolean) => void};

let pending: PendingTrust | null = null;
const listeners = new Set<() => void>();

function publish() {
    for (const listener of listeners) listener();
}

function settle(current: PendingTrust | null, accepted: boolean) {
    if (!current || pending !== current) return;
    pending = null;
    current.resolve(accepted);
    publish();
}

export function confirmMediaServer(identity: MediaServerIdentity): Promise<boolean> {
    if (pending) pending.resolve(false);
    return new Promise((resolve) => {
        pending = {identity, resolve};
        publish();
    });
}

function KeyMark() {
    return <svg viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <circle cx="7.5" cy="15.5" r="4.5"/>
        <path d="m10.7 12.3 9.3-9.3"/>
        <path d="m16 7 3 3"/>
        <path d="m19 4 2 2"/>
    </svg>;
}

export function MediaTrustDialog() {
    const {m} = useLocale();
    const dialogRef = useRef<HTMLDialogElement>(null);
    const shownRef = useRef<PendingTrust | null>(null);
    const [request, setRequest] = useState<PendingTrust | null>(null);
    if (request) shownRef.current = request;
    useEffect(() => {
        const sync = () => setRequest(pending);
        listeners.add(sync);
        sync();
        return () => { listeners.delete(sync); settle(pending, false); };
    }, []);
    useEffect(() => {
        const dialog = dialogRef.current;
        if (!dialog) return;
        if (request) {
            if (!dialog.open) dialog.showModal();
            // Cancel is the safe default: Enter must not trust an unknown key.
            dialog.querySelector<HTMLButtonElement>("button:not(.primary-button)")?.focus();
        } else if (dialog.open) dialog.close();
    }, [request]);
    const identity = (request ?? shownRef.current)?.identity;
    const changed = Boolean(identity?.previousFingerprint);
    return <dialog ref={dialogRef} className="link-confirm media-trust-dialog" aria-labelledby="media-trust-title" aria-describedby="media-trust-text"
        onCancel={(event) => { event.preventDefault(); settle(request, false); }}
        onClick={(event) => { if (event.target === event.currentTarget) settle(request, false); }}>
        {identity && <>
            <div className="link-confirm-mark"><KeyMark/></div>
            <h2 id="media-trust-title">{changed ? m.screen.mediaTrustChangedTitle : m.screen.mediaTrustTitle}</h2>
            <p id="media-trust-text">{changed ? m.screen.mediaTrustChangedText : m.screen.mediaTrustText}</p>
            <dl className="server-key-change">
                <dt>{m.connection.server}</dt><dd>{identity.server}</dd>
                {changed && <><dt>{identity.previousFingerprint.includes("\n") ? m.screen.mediaTrustConflictingKeys : m.connection.previousServerKey}</dt><dd><code>{identity.previousFingerprint}</code></dd></>}
                <dt>{m.connection.newServerKey}</dt><dd><code>{identity.fingerprint}</code></dd>
            </dl>
            <div className="screen-dialog-actions">
                <button type="button" onClick={() => settle(request, false)}>{m.screen.mediaTrustCancel}</button>
                <button className="primary-button" type="button" onClick={() => settle(request, true)}>{changed ? m.connection.trustNewServerKey : m.screen.mediaTrustAccept}</button>
            </div>
        </>}
    </dialog>;
}
