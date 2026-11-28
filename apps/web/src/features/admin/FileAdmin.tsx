import { useCallback, useEffect, useState } from "react";
import { messageFor, post, request } from "../../api/http";

interface FilePolicy {
  MaxFileBytes: number; UserQuotaBytes: number; ServerQuotaBytes: number; MinFreeBytes: number;
  UserBytesPerSecond: number; ServerBytesPerSecond: number; MaxTransfers: number;
  UploadTTL: number; Retention: number; Revision: number;
  UploadsEnabled: boolean; DownloadsEnabled: boolean;
}
interface Entry { id: string; name: string; deleted: boolean; size: number }
interface FileView {
  config: FilePolicy; files: Entry[] | null; freeBytes: number;
  usage: { logicalBytes: number }; metrics: { activeTransfers: number };
}
type Rights = { upload: boolean; download: boolean };

const path = "/api/admin/files";
const fileFields = [
  ["MaxFileBytes", 1048576, "Макс. размер файла, МиБ", 1 / 1048576],
  ["UserQuotaBytes", 1073741824, "Квота пользователя, ГиБ", 0],
  ["ServerQuotaBytes", 1073741824, "Квота сервера, ГиБ", 0],
  ["MinFreeBytes", 1073741824, "Резерв диска, ГиБ", 0],
  ["UserBytesPerSecond", 1024, "Скорость пользователя, КиБ/с", 16],
  ["ServerBytesPerSecond", 1024, "Скорость сервера, КиБ/с", 16],
  ["MaxTransfers", 1, "Одновременные передачи", 1],
  ["UploadTTL", 3_600_000_000_000, "Срок загрузки, ч", 1 / 60],
  ["Retention", 86_400_000_000_000, "Хранение файлов, дней", 0],
] as const;

export function FileAdmin() {
  const [view, setView] = useState<FileView>();
  const [policy, setPolicy] = useState<FilePolicy>();
  const [more, setMore] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const load = useCallback(async (cursor = "", signal?: AbortSignal) => {
    const result = await request<FileView>(path + (cursor ? "?cursor=" + cursor : ""), { signal });
    if (signal?.aborted) return;
    setMore(result.files?.length === 50);
    setView((previous) => cursor && previous ? { ...result, files: [...(previous.files ?? []), ...(result.files ?? [])] } : result);
    if (!cursor) setPolicy(result.config);
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    void load("", controller.signal).catch((reason) => { if (!controller.signal.aborted) setError(messageFor(reason)); });
    return () => controller.abort();
  }, [load]);

  async function run(operation: () => Promise<void>) {
    setBusy(true);
    setError("");
    try { await operation(); }
    catch (reason) { setError(messageFor(reason)); }
    finally { setBusy(false); }
  }

  return <>
    {error && <p className="notice" role="status">{error}</p>}
    <button type="button" disabled={busy} onClick={() => void run(() => load())}>Обновить файлы</button>
    {view && <p className="empty">Место на диске {Math.floor(view.freeBytes / 1048576)} МиБ · Передач {view.metrics?.activeTransfers ?? 0}</p>}
    {policy && <form className="admin-form" onSubmit={(event) => {
      event.preventDefault();
      void run(async () => { await post<void>(path, { action:"policy", config:policy }); await load(); });
    }}>
      <div className="admin-numbers">{fileFields.map(([key, unit, label, min]) =>
        <label key={key}>{label}<input type="number" required step={key === "MaxTransfers" ? 1 : "any"} min={min}
          value={policy[key] / unit} onChange={(event) => setPolicy({ ...policy, [key]: Math.round((event.target.valueAsNumber || 0) * unit) })} /></label>)}</div>
      <label className="settings-toggle"><input type="checkbox" checked={policy.UploadsEnabled} onChange={(event) => setPolicy({ ...policy, UploadsEnabled:event.target.checked })} />Разрешить загрузку</label>
      <label className="settings-toggle"><input type="checkbox" checked={policy.DownloadsEnabled} onChange={(event) => setPolicy({ ...policy, DownloadsEnabled:event.target.checked })} />Разрешить скачивание</label>
      <button type="submit" className="primary" disabled={busy}>Сохранить политику</button>
    </form>}
    {view?.files?.map((file) => <div className="admin-entry" key={file.id}>
      <strong>{file.name}</strong><small>{Math.ceil(file.size / 1024)} КиБ</small>
      {file.deleted ? <span>Удалён</span> : <button type="button" disabled={busy} onClick={() => {
        if (window.confirm("Удалить вложение " + file.name + "?")) void run(async () => {
          await post<void>(path, { action:"delete", id:file.id }); await load();
        });
      }}>Удалить вложение</button>}
    </div>)}
    {more && view?.files?.length && <button type="button" disabled={busy} onClick={() => void run(() => load(view.files?.at(-1)?.id))}>Загрузить ещё</button>}
  </>;
}

export function AccountFileRights({ id, disabled }: { id: string; disabled: boolean }) {
  const [rights, setRights] = useState<Rights>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function read() {
    setError("");
    setBusy(true);
    try { setRights(await request<Rights>(path + "/rights/" + id)); }
    catch (reason) { setError(messageFor(reason)); }
    finally { setBusy(false); }
  }
  async function change(key: keyof Rights, value: boolean) {
    if (!rights) return;
    const next = { ...rights, [key]:value };
    setError("");
    setBusy(true);
    try { await post<void>(path, { action:"rights", id, ...next }); setRights(next); }
    catch (reason) { setError(messageFor(reason)); }
    finally { setBusy(false); }
  }
  return <>
    {error && <p className="notice" role="status">{error}</p>}
    {rights ? <div className="admin-permissions">
      <label className="settings-toggle"><input type="checkbox" checked={rights.upload} disabled={disabled || busy}
        onChange={(event) => void change("upload", event.target.checked)} />Загрузка файлов</label>
      <label className="settings-toggle"><input type="checkbox" checked={rights.download} disabled={disabled || busy}
        onChange={(event) => void change("download", event.target.checked)} />Скачивание файлов</label>
    </div> : <button type="button" disabled={disabled || busy} onClick={() => void read()}>Права на файлы</button>}
  </>;
}
