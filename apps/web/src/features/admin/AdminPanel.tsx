import { useEffect, useRef, useState, type FormEvent } from "react";
import { messageFor, post } from "../../api/http";
import type { Channel } from "../../api/types";
import { AccountFileRights, FileAdmin } from "./FileAdmin";

type Section = "accounts" | "channels" | "audit" | "files";
type ChannelForm = Omit<Channel, "id" | "canJoin">;
interface Account {
  id: string;
  name: string;
  fingerprint: string;
  joinLevel: number;
  permissions: number;
  owner: boolean;
  online: boolean;
  banned: boolean;
  reason: string;
}
interface AuditEvent {
  id: string;
  at: number;
  actor: string;
  action: string;
  targetUserId: string;
  detail: string;
}
interface Page {
  next: string;
  accounts?: Account[];
  audit?: AuditEvent[];
}

const permissions = [["Выгонять", 1], ["Блокировать", 2], ["Перемещать", 4], ["Администрировать", 8]] as const;
const blank: ChannelForm = { parentId: "0", name: "", topic: "", description: "", position: 0, maxUsers: 0, minJoinLevel: 0, bitrate: 64000, application: "voip" };
const administer = (payload: Record<string, unknown>, signal?: AbortSignal) => post<Page>("/api/admin", payload, signal);

export function AdminPanel({ channels, userId, isOwner, close }: {
  channels: Channel[]; userId: string; isOwner: boolean; close: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [section, setSection] = useState<Section>("accounts");
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [audit, setAudit] = useState<AuditEvent[]>([]);
  const [next, setNext] = useState("0");
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const [selectedChannel, setSelectedChannel] = useState("");
  const [channel, setChannel] = useState<ChannelForm>(blank);

  useEffect(() => { dialog.current?.showModal(); }, []);
  useEffect(() => {
    if (section === "channels" || section === "files") return;
    const controller = new AbortController();
    setError("");
    setPending(true);
    void administer({ operation: section, cursor: "0" }, controller.signal).then((page) => {
      if (section === "accounts") setAccounts(page.accounts ?? []);
      else setAudit(page.audit ?? []);
      setNext(page.next);
    }).catch((reason) => { if (!controller.signal.aborted) setError(messageFor(reason)); })
      .finally(() => { if (!controller.signal.aborted) setPending(false); });
    return () => controller.abort();
  }, [section]);

  async function more() {
    setPending(true);
    setError("");
    try {
      const page = await administer({ operation: section, cursor: next });
      if (section === "accounts") setAccounts((current) => [...current, ...(page.accounts ?? [])]);
      else setAudit((current) => [...current, ...(page.audit ?? [])]);
      setNext(page.next);
    } catch (reason) { setError(messageFor(reason)); }
    finally { setPending(false); }
  }

  async function updateAccount(id: string, payload: Record<string, unknown>, remove = false) {
    setError("");
    setPending(true);
    try {
      const page = await administer({ id, ...payload });
      const patched = page.accounts?.[0];
      setAccounts((current) => remove ? current.filter((account) => account.id !== id)
        : patched ? current.map((account) => account.id === id ? patched : account) : current);
    } catch (reason) { setError(messageFor(reason)); }
    finally { setPending(false); }
  }

  function editChannel(id: string) {
    setSelectedChannel(id);
    const selected = channels.find((entry) => entry.id === id);
    if (!selected) { setChannel(blank); return; }
    const { id: _discarded, canJoin: _joinable, ...form } = selected;
    setChannel(form);
  }

  async function updateChannel(event?: FormEvent, remove = false) {
    event?.preventDefault();
    setError("");
    setPending(true);
    try {
      const operation = remove ? "delete-channel" : selectedChannel ? "update-channel" : "create-channel";
      await administer(remove ? { operation, id: selectedChannel } : { operation, id: selectedChannel, channel: { ...channel, name: channel.name.trim() } });
      if (remove || !selectedChannel) editChannel("");
    } catch (reason) { setError(messageFor(reason)); }
    finally { setPending(false); }
  }

  const channelNumber = (name: "position" | "maxUsers" | "minJoinLevel" | "bitrate", label: string, min: number, max: number) =>
    <label>{label}<input required type="number" min={min} max={max} value={channel[name]} disabled={pending}
      onChange={(event) => setChannel({ ...channel, [name]: event.target.valueAsNumber || 0 })} /></label>;

  return <dialog ref={dialog} className="settings-dialog admin-dialog" aria-label="Управление сервером"
    onCancel={(event) => { event.preventDefault(); close(); }}>
    <header><h2>Управление сервером</h2><button type="button" onClick={close} aria-label="Закрыть">×</button></header>
    <div className="settings-layout">
      <nav aria-label="Управление сервером">
        <button type="button" aria-pressed={section === "accounts"} disabled={pending} onClick={() => setSection("accounts")}>Пользователи</button>
        <button type="button" aria-pressed={section === "channels"} disabled={pending} onClick={() => setSection("channels")}>Каналы</button>
        <button type="button" aria-pressed={section === "audit"} disabled={pending} onClick={() => setSection("audit")}>Журнал</button>
        <button type="button" aria-pressed={section === "files"} disabled={pending} onClick={() => setSection("files")}>Файлы</button>
      </nav>
      <section className="settings-content">
        {error && <p className="notice" role="status">{error}</p>}
        {section === "accounts" && <>
          {accounts.map((account) => {
            const editable = !account.owner && account.id !== userId && (isOwner || !(account.permissions & 8));
            return <div className="admin-entry" key={account.id}>
              <div><strong>{account.name}</strong> <small>{account.online ? "В сети" : "Не в сети"}</small></div>
              <small className="admin-fingerprint" title="Отпечаток ключа">{account.fingerprint}</small>
              <label>Уровень доступа<input type="number" key={account.joinLevel} defaultValue={account.joinLevel} min={0} max={65535}
                disabled={!editable || pending} onBlur={(event) => {
                  const level = event.target.valueAsNumber;
                  if (event.target.validity.valid && level !== account.joinLevel) void updateAccount(account.id, { operation:"join-level", level });
                }} /></label>
              <div className="admin-permissions">{permissions.map(([label, permission]) => <label className="settings-toggle" key={permission}>
                <input type="checkbox" checked={(account.permissions & permission) !== 0} disabled={!editable || pending || (permission === 8 && !isOwner)}
                  onChange={(event) => void updateAccount(account.id, { operation:"permission", permission, enabled:event.target.checked })} />{label}
              </label>)}</div>
              {account.banned && <p className="notice">{account.reason || "Пользователь заблокирован"}</p>}
              {editable && <AccountFileRights id={account.id} disabled={pending} />}
              {editable && <div className="admin-permissions">
                <button type="button" disabled={pending} onClick={() => {
                  const reason = account.banned ? "" : window.prompt("Причина блокировки", "");
                  if (reason !== null) void updateAccount(account.id, { operation:"ban", enabled:!account.banned, reason });
                }}>{account.banned ? "Разблокировать" : "Заблокировать"}</button>
                {isOwner && <button type="button" disabled={pending} onClick={() => {
                  if (window.confirm("Удалить аккаунт " + account.name + "?")) void updateAccount(account.id, { operation:"delete-account" }, true);
                }}>Удалить аккаунт</button>}
              </div>}
            </div>;
          })}
          {!pending && !accounts.length && !error && <p className="empty">Пользователей нет.</p>}
        </>}
        {section === "channels" && <form className="admin-form" onSubmit={(event) => void updateChannel(event)}>
          <label>Канал<select value={selectedChannel} onChange={(event) => editChannel(event.target.value)}>
            <option value="">Новый канал</option>{channels.map(({ id, name }) => <option key={id} value={id}>{name}</option>)}
          </select></label>
          <label>Название<input required value={channel.name} onChange={(event) => setChannel({ ...channel, name:event.target.value })} /></label>
          <label>Тема<input value={channel.topic} onChange={(event) => setChannel({ ...channel, topic:event.target.value })} /></label>
          <label>Описание<textarea rows={3} value={channel.description} onChange={(event) => setChannel({ ...channel, description:event.target.value })} /></label>
          <label>Родительский канал<select value={channel.parentId} onChange={(event) => setChannel({ ...channel, parentId:event.target.value })}>
            <option value="0">Корень</option>{channels.filter(({ id }) => id !== selectedChannel).map(({ id, name }) => <option key={id} value={id}>{name}</option>)}
          </select></label>
          <div className="admin-numbers">
            {channelNumber("position", "Позиция", 0, 4294967295)}
            {channelNumber("maxUsers", "Лимит участников (0 — без лимита)", 0, 4294967295)}
            {channelNumber("minJoinLevel", "Минимальный уровень входа", 0, 65535)}
            {channelNumber("bitrate", "Битрейт Opus, бит/с", 6000, 472000)}
          </div>
          <label>Режим Opus<select value={channel.application} onChange={(event) => setChannel({ ...channel, application: event.target.value === "audio" ? "audio" : "voip" })}>
            <option value="voip">Голос</option><option value="audio">Аудио</option>
          </select></label>
          <div className="admin-permissions">
            <button className="primary" type="submit" disabled={pending}>{selectedChannel ? "Сохранить" : "Создать канал"}</button>
            {selectedChannel && <button type="button" disabled={pending} onClick={() => {
              if (window.confirm("Удалить канал " + channel.name + "?")) void updateChannel(undefined, true);
            }}>Удалить канал</button>}
          </div>
        </form>}
        {section === "audit" && <>
          {audit.map((entry) => <div className="admin-entry" key={entry.id}>
            <strong>{entry.action}</strong>
            <small>{new Date(entry.at).toLocaleString("ru")} · {entry.actor}</small>
            <span>{entry.detail}</span>
          </div>)}
          {!pending && !audit.length && !error && <p className="empty">Записей пока нет.</p>}
        </>}
        {section === "files" && <FileAdmin />}
        {(section === "accounts" || section === "audit") && next !== "0" && <button type="button" disabled={pending} onClick={() => void more()}>Загрузить ещё</button>}
      </section>
    </div>
  </dialog>;
}
