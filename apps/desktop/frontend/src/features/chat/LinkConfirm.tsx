import {useEffect, useRef, useState} from "react";
import {useLocale} from "../../locales/Locale";

type PendingLink = {href: string; resolve: (accepted: boolean) => void};

let pending: PendingLink | null = null;
const listeners = new Set<() => void>();

function publish() {
    for (const listener of listeners) listener();
}

function settle(accepted: boolean) {
    const current = pending;
    if (!current) return;
    pending = null;
    current.resolve(accepted);
    publish();
}

export function confirmExternalLink(href: string): Promise<boolean> {
    if (pending) pending.resolve(false);
    return new Promise((resolve) => {
        pending = {href, resolve};
        publish();
    });
}

function LinkMark() {
    return <svg viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71"/>
        <path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71"/>
    </svg>;
}

export function LinkConfirmDialog() {
    const {m} = useLocale();
    const dialogRef = useRef<HTMLDialogElement>(null);
    const shownRef = useRef("");
    const [href, setHref] = useState<string | null>(null);
    if (href) shownRef.current = href;
    useEffect(() => {
        const sync = () => setHref(pending?.href ?? null);
        listeners.add(sync);
        sync();
        return () => { listeners.delete(sync); };
    }, []);
    useEffect(() => {
        const dialog = dialogRef.current;
        if (!dialog) return;
        if (href && !dialog.open) {
            dialog.showModal();
            dialog.querySelector<HTMLButtonElement>(".primary-button")?.focus();
        } else if (!href && dialog.open) dialog.close();
    }, [href]);
    return <dialog ref={dialogRef} className="link-confirm" aria-labelledby="link-confirm-title" aria-describedby="link-confirm-address"
        onCancel={() => settle(false)}
        onClose={() => settle(false)}
        onClick={(event) => { if (event.target === event.currentTarget) dialogRef.current?.close(); }}>
        {(href ?? shownRef.current) && <>
            <div className="link-confirm-mark"><LinkMark/></div>
            <h2 id="link-confirm-title">{m.chat.openLink}</h2>
            <p id="link-confirm-address" className="link-confirm-address">{href ?? shownRef.current}</p>
            <div className="screen-dialog-actions">
                <button type="button" onClick={() => settle(false)}>{m.chat.openLinkCancel}</button>
                <button className="primary-button" type="button" autoFocus onClick={() => settle(true)}>{m.chat.openLinkGo}</button>
            </div>
        </>}
    </dialog>;
}
