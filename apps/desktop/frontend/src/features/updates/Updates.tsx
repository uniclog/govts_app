import {useEffect, useRef, useState} from "react";
import {Service, type Snapshot as UpdateSnapshot} from "../../../bindings/uniclog.io/sonoryx/apps/desktop/internal/clientupdate";
import type {Messages} from "../../locales";
import {errorText} from "../../locales";
import {useLocale} from "../../locales/Locale";

export function useUpdates(prepareRestart: () => void) {
    const [view, setView] = useState<UpdateSnapshot | null>(null);
    const [error, setError] = useState("");
    const [dialog, setDialog] = useState(false);
    const [acting, setActing] = useState(false);
    const actionBusy = useRef(false);
    useEffect(() => {
        let active = true, busy = false;
        void Service.ConfirmStartup().catch(() => undefined);
        const refresh = async () => {
            if (busy) return; busy = true;
            try { const next = await Service.Snapshot(); if (active) setView(next); }
            catch { /* Background status reads never interrupt the user. */ }
            finally { busy = false; }
        };
        void refresh();
        const timer = window.setInterval(() => void refresh(), 1000);
        return () => { active = false; window.clearInterval(timer); };
    }, []);
    const action = async (method: "Check" | "Download" | "Restart" | "Cancel") => {
        if (actionBusy.current && method !== "Cancel") return;
        if (method !== "Cancel") { actionBusy.current = true; setActing(true); }
        setError("");
        try {
            if (method === "Restart") prepareRestart();
            await Service[method]();
            setView(await Service.Snapshot());
        } catch (reason) { setError((reason instanceof Error ? reason.message : String(reason)).replace(/^Error:\s*/, "")); }
        finally { if (method !== "Cancel") { actionBusy.current = false; setActing(false); } }
    };
    return {view, error, dialog, setDialog, acting, action};
}

export type UpdatesState = ReturnType<typeof useUpdates>;

function currentClientLine(version: string, minServer: string, m: Messages) {
    if (!version || !minServer) return m.updates.loadingVersion;
    return m.updates.currentBuild(version, minServer);
}

function availableClientLine(version: string, minServer: string, m: Messages) {
    if (!minServer) return m.updates.availableNoServer(version);
    return m.updates.available(version, minServer);
}

export function UpdatesPanel({updates}: {updates: UpdatesState}) {
    const {m} = useLocale();
    const {view} = updates;
    const busy = updates.acting || ["checking", "downloading", "restarting"].includes(view?.status ?? "");
    const detail = updates.error || view?.error || "";
    return <section className="settings-card updates-panel">
        <h3>{m.updates.title}</h3>
        {!view && <p role="status">{m.updates.loading}</p>}
        {view && <p>{currentClientLine(view.current, view.minServer, m)}</p>}
        {view?.status === "checking" && <p role="status">{m.updates.checking}</p>}
        {view?.status === "up-to-date" && <p role="status">{m.updates.current}</p>}
        {view?.available && <p>{availableClientLine(view.available, view.availableMinServer, m)}</p>}
        {view?.status === "downloading" && <p role="status">{m.updates.downloading}</p>}
        {view?.status === "downloading" && <><progress max={view.total || 1} value={view.written}/><small>{m.updates.megabytes((view.written / 1048576).toFixed(1), (view.total / 1048576).toFixed(1))}</small></>}
        {view?.status === "ready" && <p>{m.updates.ready}</p>}
        {view?.status === "restarting" && <p role="status">{m.updates.restarting}</p>}
        {view?.status === "error" && <p className="screen-error" role="alert">{m.updates.failed}</p>}
        {detail && <p className="screen-error" role="alert">{errorText(detail, m)}</p>}
        <div className="update-actions">
            <button disabled={!view || busy || view.status === "ready"} onClick={() => void updates.action("Check")}>{m.updates.check}</button>
            {view?.available && ["available", "error"].includes(view.status) && <button disabled={busy} onClick={() => void updates.action("Download")}>{m.updates.update}</button>}
            {view?.status === "downloading" && <button onClick={() => void updates.action("Cancel")}>{m.updates.cancel}</button>}
            {view?.status === "ready" && <button disabled={busy} onClick={() => void updates.action("Restart")}>{m.updates.restart}</button>}
            {view?.releaseURL && <a href={view.releaseURL} target="_blank" rel="noreferrer">{m.updates.release}</a>}
        </div>
    </section>;
}

export function UpdateButton({updates}: {updates: UpdatesState}) {
    const {m} = useLocale();
    if (!updates.view?.available) return null;
    return <button type="button" className="header-update-button" aria-haspopup="dialog"
        title={m.updates.availableTitle(updates.view.available)} onClick={() => updates.setDialog(true)}>
        {updates.view.status === "ready" ? m.updates.update : updates.view.status === "downloading" ? m.updates.downloadingShort : m.updates.availableShort}
    </button>;
}

export function UpdateDialog({updates}: {updates: UpdatesState}) {
    const {m} = useLocale();
    const dialogRef = useRef<HTMLDialogElement>(null);
    useEffect(() => {
        if (updates.dialog) dialogRef.current?.showModal(); else dialogRef.current?.close();
    }, [updates.dialog]);
    return <dialog ref={dialogRef} className="screen-share-dialog update-dialog" aria-label={m.updates.dialog}
            onCancel={(event) => { event.preventDefault(); updates.setDialog(false); }}>
            <UpdatesPanel updates={updates}/><button onClick={() => updates.setDialog(false)}>{m.updates.close}</button>
        </dialog>;
}
