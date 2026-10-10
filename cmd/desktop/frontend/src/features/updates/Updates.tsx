import {useEffect, useRef, useState} from "react";
import {Service, type Snapshot as UpdateSnapshot} from "../../../bindings/uniclog.io/sonoryx/internal/clientupdate";

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

function currentClientLine(version: string, minServer: string) {
    if (!version || !minServer) return "Получение версии клиента…";
    return `Эта сборка клиента Sonoryx ${version} подключается к серверу версии ${minServer} и новее.`;
}

function availableClientLine(version: string, minServer: string) {
    if (!minServer) return `Доступна новая сборка клиента Sonoryx ${version}. Минимальная версия сервера для неё в подписи релиза не указана.`;
    return `Доступна новая сборка клиента Sonoryx ${version}. Она подключается к серверу версии ${minServer} и новее.`;
}

export function UpdatesPanel({updates}: {updates: UpdatesState}) {
    const {view} = updates;
    const busy = updates.acting || ["checking", "downloading", "restarting"].includes(view?.status ?? "");
    const detail = updates.error || view?.error || "";
    return <section className="settings-card updates-panel">
        <h3>Обновления</h3>
        {!view && <p role="status">Получение состояния…</p>}
        {view && <p>{currentClientLine(view.current, view.minServer)}</p>}
        {view?.status === "checking" && <p role="status">Проверка обновлений…</p>}
        {view?.status === "up-to-date" && <p role="status">Установлена последняя версия.</p>}
        {view?.available && <p>{availableClientLine(view.available, view.availableMinServer)}</p>}
        {view?.status === "downloading" && <p role="status">Загрузка и проверка обновления…</p>}
        {view?.status === "downloading" && <><progress max={view.total || 1} value={view.written}/><small>{(view.written / 1048576).toFixed(1)} / {(view.total / 1048576).toFixed(1)} МБ</small></>}
        {view?.status === "ready" && <p>Обновление загружено и проверено. Установка перезапустит Sonoryx и завершит текущий разговор и демонстрацию.</p>}
        {view?.status === "restarting" && <p role="status">Перезапуск…</p>}
        {view?.status === "error" && <p className="screen-error" role="alert">Не удалось завершить обновление.</p>}
        {detail && <p className="screen-error" role="alert">{detail}</p>}
        <div className="update-actions">
            <button disabled={!view || busy || view.status === "ready"} onClick={() => void updates.action("Check")}>Проверить обновления</button>
            {view?.available && ["available", "error"].includes(view.status) && <button disabled={busy} onClick={() => void updates.action("Download")}>Обновить</button>}
            {view?.status === "downloading" && <button onClick={() => void updates.action("Cancel")}>Отмена</button>}
            {view?.status === "ready" && <button disabled={busy} onClick={() => void updates.action("Restart")}>Обновить и перезапустить</button>}
            {view?.releaseURL && <a href={view.releaseURL} target="_blank" rel="noreferrer">Страница релиза</a>}
        </div>
    </section>;
}

export function UpdateButton({updates}: {updates: UpdatesState}) {
    if (!updates.view?.available) return null;
    return <button type="button" className="header-update-button" aria-haspopup="dialog"
        title={`Доступна версия приложения ${updates.view.available}`} onClick={() => updates.setDialog(true)}>
        {updates.view.status === "ready" ? "Обновить" : updates.view.status === "downloading" ? "Загрузка…" : "Доступно обновление"}
    </button>;
}

export function UpdateDialog({updates}: {updates: UpdatesState}) {
    const dialogRef = useRef<HTMLDialogElement>(null);
    useEffect(() => {
        if (updates.dialog) dialogRef.current?.showModal(); else dialogRef.current?.close();
    }, [updates.dialog]);
    return <dialog ref={dialogRef} className="screen-share-dialog update-dialog" aria-label="Обновления приложения"
            onCancel={(event) => { event.preventDefault(); updates.setDialog(false); }}>
            <UpdatesPanel updates={updates}/><button onClick={() => updates.setDialog(false)}>Закрыть</button>
        </dialog>;
}
