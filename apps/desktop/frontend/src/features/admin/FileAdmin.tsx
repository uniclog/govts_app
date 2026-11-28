import {useCallback, useEffect, useRef, useState} from "react";
import {desktopAPI, type FileAdminView, type FileAttachment, type FilePolicy} from "../../api";
import {Icon} from "../../components/Icon";
import {useLocale} from "../../locales/Locale";
import {fileSize} from "../chat/files";

export function FileAdmin({context}: {context: string}) {
    const {m} = useLocale();
    const [view, setView] = useState<FileAdminView>();
    const [policy, setPolicy] = useState<FilePolicy>();
    const [error, setError] = useState("");
    const [busy, setBusy] = useState(false);
    const [hasMore, setHasMore] = useState(false);
    const [confirmDelete, setConfirmDelete] = useState<FileAttachment>();
    const busyRef = useRef(false);
    const activeContext = useRef(context);
    activeContext.current = context;

    const load = useCallback(async (cursor = "", append = false) => {
        const result = await desktopAPI.fileAdmin(context, "GET", cursor);
        if (activeContext.current !== context) return;
        setHasMore(result.files.length === 50);
        setView((previous) => append && previous
            ? {...result, files: [...previous.files, ...result.files.filter((file) => !previous.files.some((known) => known.id === file.id))]}
            : result);
        if (!append) setPolicy(result.config);
    }, [context]);
    const run = async (operation: () => Promise<unknown>) => {
        if (busyRef.current) return;
        busyRef.current = true;
        setBusy(true); setError("");
        try { await operation(); }
        catch (e) { if (activeContext.current === context) setError(String(e).includes("policy_conflict") ? m.files.policyConflict : String(e)); }
        finally { busyRef.current = false; if (activeContext.current === context) setBusy(false); }
    };
    useEffect(() => {
        let disposed = false;
        setView(undefined); setPolicy(undefined); setError(""); setConfirmDelete(undefined); setBusy(true);
        void load().catch((e) => {if (!disposed) setError(String(e));}).finally(() => {if (!disposed) setBusy(false);});
        return () => {disposed = true;};
    }, [load]);

    const fields: [keyof FilePolicy, number, string, number][] = [
        ["MaxFileBytes", 1048576, m.files.maxMiB, 1 / 1048576],
        ["UserQuotaBytes", 1073741824, m.files.userGiB, 0],
        ["ServerQuotaBytes", 1073741824, m.files.serverGiB, 0],
        ["MinFreeBytes", 1073741824, m.files.freeGiB, 0],
        ["UserBytesPerSecond", 1024, m.files.userKiB, 16],
        ["ServerBytesPerSecond", 1024, m.files.serverKiB, 16],
        ["MaxTransfers", 1, m.files.parallel, 1],
        ["UploadTTL", 3600000000000, m.files.ttlHours, 1 / 60],
        ["Retention", 86400000000000, m.files.retentionDays, 0],
    ];

    return <div className="file-admin" aria-busy={busy}>
        {error && <div className="device-error" role="alert">{error}</div>}
        <section className="settings-card file-admin-overview">
            <div className="file-admin-heading">
                <h3>{m.files.admin}</h3>
                <button className="secondary-button file-admin-refresh" type="button" disabled={busy} onClick={() => void run(() => load())}>
                    <Icon name="refresh"/>{m.admin.reload}
                </button>
            </div>
            {view && <dl className="file-admin-stats">
                <div><dt>{m.files.used}</dt><dd>{fileSize(view.usage.logicalBytes)}</dd></div>
                <div><dt>{m.files.free}</dt><dd>{fileSize(view.freeBytes)}</dd></div>
                <div><dt>{m.files.active}</dt><dd>{view.metrics.activeTransfers}</dd></div>
            </dl>}
        </section>

        {policy && <form className="settings-card file-admin-policy" onSubmit={(e) => {
            e.preventDefault(); void run(async () => {
                await desktopAPI.fileAdmin(context, "POST", "", {action: "policy", config: policy});
                await load();
            });
        }}>
            <h3>{m.files.limits}</h3>
            <fieldset className="file-admin-fieldset" disabled={busy}>
                <div className="file-policy-grid">
                    {fields.map(([key, unit, label, minimum]) => <label className="file-admin-field" key={key}>
                        <span className="setting-label">{label}</span>
                        <input type="number" min={minimum} step={key === "MaxTransfers" ? 1 : "any"} required
                            value={Number(policy[key]) / unit}
                            onChange={(e) => setPolicy({...policy, [key]: Math.round(Number(e.target.value) * unit)})}/>
                    </label>)}
                </div>
                <div className="file-admin-permissions">
                    <label className="file-admin-check"><input type="checkbox" checked={policy.UploadsEnabled} onChange={(e) => setPolicy({...policy, UploadsEnabled: e.target.checked})}/><span>{m.files.allowUpload}</span></label>
                    <label className="file-admin-check"><input type="checkbox" checked={policy.DownloadsEnabled} onChange={(e) => setPolicy({...policy, DownloadsEnabled: e.target.checked})}/><span>{m.files.allowDownload}</span></label>
                </div>
                <div className="file-admin-actions"><button className="primary-button" disabled={busy} type="submit">{m.admin.save}</button></div>
            </fieldset>
        </form>}

        {view && <section className="settings-card">
            <h3>{m.files.attachments}</h3>
            {!view.files.length && <p className="device-hint">{m.files.noAttachments}</p>}
            <div className="file-admin-list">
                {view.files.map((file) => <div className={`file-admin-item${file.deleted ? " is-deleted" : ""}`} key={file.id}>
                    <Icon name="file"/>
                    <div className="file-admin-item-info"><strong title={file.name}>{file.name}</strong><small>#{file.id} · {fileSize(file.size)}</small></div>
                    {file.deleted ? <span className="file-status-badge">{m.files.deleted}</span> : <button className="secondary-button danger-button" type="button" disabled={busy} onClick={() => setConfirmDelete(file)}>{m.files.delete}</button>}
                    {confirmDelete?.id === file.id && <div className="file-admin-confirm" role="alert">
                        <p>{m.files.confirmDelete}</p>
                        <div className="file-admin-actions">
                            <button className="secondary-button" type="button" disabled={busy} onClick={() => setConfirmDelete(undefined)}>{m.files.cancel}</button>
                            <button className="secondary-button danger-button" type="button" disabled={busy} onClick={() => void run(async () => {
                                await desktopAPI.fileAdmin(context, "POST", "", {action: "delete", id: file.id});
                                setConfirmDelete(undefined); await load();
                            })}>{m.files.delete}</button>
                        </div>
                    </div>}
                </div>)}
            </div>
            {hasMore && view.files.length > 0 && <div className="file-admin-actions">
                <button className="secondary-button" type="button" disabled={busy} onClick={() => void run(() => load(view.files[view.files.length - 1].id, true))}>{m.admin.loadMore}</button>
            </div>}
        </section>}

    </div>;
}
