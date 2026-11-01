import {useCallback, useEffect, useMemo, useState} from "react";
import {FileAdmin} from "./FileAdmin";
import {desktopAPI, type AdminAccount, type AdminAuditEntry, type AdminChannelInput, type AdminPermission, type ChannelDTO, type ClientViewDTO} from "../../api";
import {Icon} from "../../components/Icon";
import {Select} from "../../components/Select";
import {errorText} from "../../locales";
import {useLocale} from "../../locales/Locale";
import {canBan, canEditAccount, canEditAccountPermissions, canEditChannelLevel, canTogglePermission, channelEditingServerVersion, channelTree, descendantIDs, levelAllowed, parseLevel, reorderPositions, versionAtLeast} from "./adminRules";

type Section = "accounts" | "channels" | "audit" | "files";

const maxReasonBytes = 128;

export function AdminPanel({view}: {view: ClientViewDTO}) {
    const {m} = useLocale();
    const [section, setSection] = useState<Section>("accounts");
    return <section className="settings-page settings-shell admin-page">
        <div className="settings-layout">
            <nav className="settings-nav" aria-label={m.admin.sections}>
                <h2>{m.admin.title}</h2>
                <button type="button" aria-current={section === "accounts" ? "page" : undefined} onClick={() => setSection("accounts")}><Icon name="users"/>{m.admin.accounts}</button>
                <button type="button" aria-current={section === "channels" ? "page" : undefined} onClick={() => setSection("channels")}><Icon name="channels"/>{m.admin.channels}</button>
                <button type="button" aria-current={section === "audit" ? "page" : undefined} onClick={() => setSection("audit")}><Icon name="shield"/>{m.admin.audit}</button>
                <button type="button" aria-current={section === "files" ? "page" : undefined} onClick={() => setSection("files")}><Icon name="file"/>{m.files.admin}</button>
            </nav>
            <div className="settings-panel">
                <div className="settings-heading"><h2>{section === "files" ? m.files.admin : m.admin[section]}</h2><p>{view.server.name || m.connection.server}</p></div>
            <div className="settings-content">
                {section === "accounts" && <AccountsSection view={view}/>}
                {section === "channels" && <ChannelsSection view={view}/>}
                {section === "audit" && <AuditSection/>}
                {section === "files" && <FileAdmin context={view.chatContext}/>}
            </div>
            </div>
        </div>
    </section>;
}

function useStatus() {
    const {m} = useLocale();
    const [error, setError] = useState("");
    const [busy, setBusy] = useState(false);
    const run = useCallback(async (operation: () => Promise<unknown>) => {
        setBusy(true);
        setError("");
        try {
            await operation();
            return true;
        } catch (cause) {
            setError(errorText(cause, m));
            return false;
        } finally {
            setBusy(false);
        }
    }, [m]);
    return {error, busy, run};
}

function AccountsSection({view}: {view: ClientViewDTO}) {
    const {m} = useLocale();
    const [accounts, setAccounts] = useState<AdminAccount[]>([]);
    const [next, setNext] = useState("");
    const [loaded, setLoaded] = useState(false);
    const [query, setQuery] = useState("");
    const [selectedID, setSelectedID] = useState("");
    const {error, busy, run} = useStatus();

    const load = useCallback((cursor: string) => run(async () => {
        const page = await desktopAPI.adminAccounts(cursor);
        const items = page.accounts ?? [];
        setAccounts((current) => cursor === "" ? items : [...current, ...items.filter((item) => !current.some((known) => known.id === item.id))]);
        setNext(page.next);
        setLoaded(true);
    }), [run]);
    useEffect(() => { void load(""); }, [load]);

    const replace = (account: AdminAccount) => setAccounts((current) => current.map((item) => item.id === account.id ? account : item));
    const filter = query.trim().toLowerCase();
    const visible = useMemo(() => filter === "" ? accounts : accounts.filter((account) =>
        account.name.toLowerCase().includes(filter) || account.id === filter || account.fingerprint.startsWith(filter)), [accounts, filter]);
    const selected = accounts.find((account) => account.id === selectedID);

    return <div className="admin-accounts">
        <section className="settings-card admin-list-card">
            <h3>{m.admin.accounts}</h3>
            <input type="search" className="admin-search" value={query} placeholder={m.admin.search} aria-label={m.admin.search}
                   onChange={(event) => setQuery(event.target.value)}/>
            {error && <div className="device-error" role="alert">{error}</div>}
            <ul className="admin-list" aria-busy={busy}>
                {visible.map((account) => <li key={account.id}>
                    <button type="button" className="admin-account-row" aria-pressed={account.id === selectedID} onClick={() => setSelectedID(account.id)}>
                        <span className={`admin-presence ${account.online ? "online" : ""}`} aria-hidden="true"/>
                        <span className="admin-account-main">
                            <span className="admin-account-title"><span className="admin-account-name" title={account.name}>{account.name}</span><span className="admin-account-id">#{account.id}</span></span>
                            <AccountBadges account={account} compact/>
                        </span>
                    </button>
                </li>)}
            </ul>
            {loaded && visible.length === 0 && <p className="device-hint">{m.admin.empty}</p>}
            <div className="admin-list-actions">
                {next && <button type="button" className="secondary-button" disabled={busy} onClick={() => void load(next)}>{busy ? m.admin.loading : m.admin.loadMore}</button>}
                <button type="button" className="text-button" disabled={busy} onClick={() => void load("")}>{m.admin.reload}</button>
            </div>
        </section>
        {selected ? <AccountCard key={selected.id} view={view} account={selected} onChange={replace}
                                 onDelete={() => { setAccounts((current) => current.filter((item) => item.id !== selected.id)); setSelectedID(""); }}/>
            : <section className="settings-card admin-detail-card"><p className="device-hint">{m.admin.selectAccount}</p></section>}
    </div>;
}

// AccountBadges in compact form (the list row) leaves out what the row
// already shows: presence is the dot, and the owner implies administrator.
function AccountBadges({account, compact = false}: {account: AdminAccount; compact?: boolean}) {
    const {m} = useLocale();
    const admin = account.admin && !(compact && account.owner);
    const online = account.online && !compact;
    if (!account.owner && !admin && !account.banned && !online) return null;
    return <span className="admin-badges">
        {account.owner && <span className="admin-badge accent">{m.admin.owner}</span>}
        {admin && <span className="admin-badge">{m.admin.admin}</span>}
        {account.banned && <span className="admin-badge danger">{m.admin.banned}</span>}
        {online && <span className="admin-badge">{m.admin.online}</span>}
    </span>;
}

function AccountCard({view, account, onChange, onDelete}: {view: ClientViewDTO; account: AdminAccount; onChange: (account: AdminAccount) => void; onDelete: () => void}) {
    const {m} = useLocale();
    const {error, busy, run} = useStatus();
    const [level, setLevel] = useState(String(account.joinLevel));
    const [reason, setReason] = useState("");
    const editable = canEditAccount(view, account);
    const permissionsEditable = canEditAccountPermissions(view, account);
    const banAllowed = canBan(view, account);
    const parsedLevel = parseLevel(level);
    const levelValid = levelAllowed(view, parsedLevel);
    const reasonTooLong = new TextEncoder().encode(reason).length > maxReasonBytes;

    const update = (operation: () => Promise<AdminAccount>) => run(async () => {
        const changed = await operation();
        onChange(changed);
        setLevel(String(changed.joinLevel));
    });
    const permissions: {id: AdminPermission; title: string; hint: string}[] = [
        {id: "kick", title: m.admin.permKick, hint: m.admin.permKickHint},
        {id: "ban", title: m.admin.permBan, hint: m.admin.permBanHint},
        {id: "drag", title: m.admin.permDrag, hint: m.admin.permDragHint},
        {id: "admin", title: m.admin.permAdmin, hint: m.admin.permAdminHint},
    ];

    return <section className="settings-card admin-detail-card" aria-busy={busy}>
        <h3>{account.name} <span className="admin-account-id">#{account.id}</span></h3>
        <p className="device-hint admin-fingerprint">{m.admin.fingerprint}: <code>{account.fingerprint}</code></p>
        <AccountBadges account={account}/>
        {!editable && <p className="device-hint">{permissionsEditable ? m.admin.ownerPermissionsHint : m.admin.protected}</p>}
        {error && <div className="device-error" role="alert">{error}</div>}

        {permissions.map((permission) => <div key={permission.id} className="setting-toggle">
            <div><strong>{permission.title}</strong><p>{permission.hint}</p></div>
            <label className="switch"><input type="checkbox" checked={account[permission.id]} disabled={busy || !canTogglePermission(view, account, permission.id)}
                                             onChange={(event) => void update(() => desktopAPI.adminSetPermission(account.id, permission.id, event.target.checked))}/><span/></label>
        </div>)}

        <AccountFileRights key={`${view.chatContext}:${account.id}`} context={view.chatContext} accountID={account.id} editable={permissionsEditable} accountBusy={busy}/>

        <form className="admin-inline-form" onSubmit={(event) => {
            event.preventDefault();
            if (parsedLevel !== null && levelValid) void update(() => desktopAPI.adminSetJoinLevel(account.id, parsedLevel));
        }}>
            <label><span className="setting-label">{m.admin.joinLevel}</span>
                <input inputMode="numeric" value={level} disabled={busy || !editable} onChange={(event) => setLevel(event.target.value)}/></label>
            <button type="submit" className="secondary-button" disabled={busy || !editable || !levelValid || parsedLevel === account.joinLevel}>{m.admin.save}</button>
            <p className="device-hint">{view.isOwner ? m.admin.joinLevelHint : m.admin.joinLevelLimit((view.joinLevel ?? 0))}</p>
        </form>

        <div className="admin-inline-form admin-ban">
            {account.banned ? <>
                <p className="device-hint">{m.admin.bannedReason(account.banReason)}</p>
                <button type="button" className="secondary-button" disabled={busy || !banAllowed}
                        onClick={() => void update(() => desktopAPI.adminSetBan(account.id, false, ""))}>{m.admin.unban}</button>
            </> : <>
                <label><span className="setting-label">{m.admin.banReason}</span>
                    <input value={reason} maxLength={maxReasonBytes} disabled={busy || !editable} onChange={(event) => setReason(event.target.value)}/></label>
                <button type="button" className="secondary-button danger-button" disabled={busy || !banAllowed || reasonTooLong}
                        onClick={() => {
                            if (!window.confirm(m.admin.banConfirm(account.name))) return;
                            void update(() => desktopAPI.adminSetBan(account.id, true, reason.trim()));
                        }}>{m.admin.ban}</button>
            </>}
        </div>

        {versionAtLeast(view.server.version, channelEditingServerVersion) && <div className="admin-inline-form admin-ban">
            <p className="device-hint">{account.banned ? m.admin.deleteAccountBanned : m.admin.deleteAccountHint}</p>
            <button type="button" className="secondary-button danger-button" disabled={busy || !banAllowed || account.banned}
                    onClick={() => {
                        if (!window.confirm(m.admin.deleteAccountConfirm(account.name))) return;
                        void run(async () => {
                            await desktopAPI.adminDeleteAccount(account.id);
                            onDelete();
                        });
                    }}>{m.admin.deleteAccount}</button>
        </div>}
    </section>;
}

function AccountFileRights({context, accountID, editable, accountBusy}: {context: string; accountID: string; editable: boolean; accountBusy: boolean}) {
    const {m} = useLocale();
    const [rights, setRights] = useState<{upload: boolean; download: boolean}>();
    const [loadError, setLoadError] = useState("");
    const [attempt, setAttempt] = useState(0);
    const {error, busy, run} = useStatus();
    useEffect(() => {
        let disposed = false;
        setRights(undefined); setLoadError("");
        void desktopAPI.fileAccountRights(context, accountID).then((value) => {
            if (!value || typeof value.upload !== "boolean" || typeof value.download !== "boolean") throw new Error("Invalid file rights response");
            if (!disposed) setRights(value);
        }).catch((cause) => {if (!disposed) setLoadError(errorText(cause, m));});
        return () => {disposed = true;};
    }, [context, accountID, attempt, m]);
    return <div className="admin-account-file-rights" aria-busy={!rights && !loadError || busy}>
        <h4>{m.files.admin}</h4>
        {loadError && <div className="device-error" role="alert">{loadError} <button className="text-button" type="button" onClick={() => setAttempt((value) => value + 1)}>{m.admin.reload}</button></div>}
        {error && <div className="device-error" role="alert">{error}</div>}
        {!rights && !loadError && <p className="device-hint">{m.admin.loading}</p>}
        {rights && (["upload", "download"] as const).map((permission) => <div className="setting-toggle" key={permission}>
            <strong>{permission === "upload" ? m.files.allowUpload : m.files.allowDownload}</strong>
            <label className="switch"><input type="checkbox" aria-label={permission === "upload" ? m.files.allowUpload : m.files.allowDownload} checked={rights[permission]} disabled={!editable || busy || accountBusy}
                onChange={(event) => {
                    const changed = {...rights, [permission]: event.target.checked};
                    void run(async () => {
                        await desktopAPI.fileAdmin(context, "POST", "", {action: "rights", id: accountID, ...changed});
                        setRights(changed);
                    });
                }}/><span/></label>
        </div>)}
    </div>;
}

function ChannelsSection({view}: {view: ClientViewDTO}) {
    const {m} = useLocale();
    const channels = view.channels ?? [];
    if (!versionAtLeast(view.server.version, channelEditingServerVersion)) {
        // Older servers only support the join threshold.
        return <section className="settings-card">
            <h3>{m.admin.channels}</h3>
            <p className="device-hint">{m.admin.channelsHint}</p>
            <p className="device-hint">{m.admin.channelEditingNeedsServer(channelEditingServerVersion)}</p>
            {channels.map((channel) => <ChannelLevelRow key={channel.id} view={view} channelID={channel.id} name={channel.name} level={channel.minJoinLevel ?? 0}/>)}
        </section>;
    }
    return <ChannelEditorSection view={view}/>;
}

type ChannelDraft = {parentId: string; name: string; topic: string; description: string; position: string; maxUsers: string;
    minJoinLevel: string; bitrateKbps: string; application: "voip" | "audio"};

function channelDraft(channel: ChannelDTO): ChannelDraft {
    return {
        parentId: channel.parentId, name: channel.name, topic: channel.topic, description: channel.description,
        position: String(channel.position), maxUsers: String(channel.maxUsers), minJoinLevel: String(channel.minJoinLevel ?? 0),
        bitrateKbps: String(Math.round(channel.audio.bitrate / 1000)), application: channel.audio.application === "audio" ? "audio" : "voip",
    };
}

function channelInput(channel: ChannelDTO, patch: Partial<AdminChannelInput> = {}): AdminChannelInput {
    return {
        parentId: channel.parentId, name: channel.name, topic: channel.topic, description: channel.description, position: channel.position,
        maxUsers: channel.maxUsers, minJoinLevel: channel.minJoinLevel ?? 0, bitrate: channel.audio.bitrate,
        application: channel.audio.application === "audio" ? "audio" : "voip", ...patch,
    };
}

const textEncoder = new TextEncoder();
const utf8Length = (value: string) => textEncoder.encode(value).length;

function parseDraft(draft: ChannelDraft): AdminChannelInput | null {
    const whole = (value: string, max: number) => /^\d{1,10}$/.test(value.trim()) && Number(value) <= max ? Number(value) : null;
    const name = draft.name.trim();
    const position = whole(draft.position, 4294967295);
    const maxUsers = whole(draft.maxUsers, 4294967295);
    const level = parseLevel(draft.minJoinLevel);
    const kbps = whole(draft.bitrateKbps, 472);
    if (name === "" || utf8Length(name) > 64 || utf8Length(draft.topic) > 128 || utf8Length(draft.description) > 512 ||
        position === null || maxUsers === null || level === null || kbps === null || kbps < 6) return null;
    return {parentId: draft.parentId, name, topic: draft.topic, description: draft.description, position, maxUsers,
        minJoinLevel: level, bitrate: kbps * 1000, application: draft.application};
}

function ChannelEditorSection({view}: {view: ClientViewDTO}) {
    const {m} = useLocale();
    const channels = useMemo(() => view.channels ?? [], [view.channels]);
    const tree = useMemo(() => channelTree(channels), [channels]);
    const [selectedID, setSelectedID] = useState<string>("");
    const [creatingUnder, setCreatingUnder] = useState<string | null>(null);
    const {error, busy, run} = useStatus();
    const selected = channels.find((channel) => channel.id === selectedID);

    const move = (channel: ChannelDTO, direction: -1 | 1) => run(async () => {
        for (const change of reorderPositions(channels, channel.id, direction)) {
            const target = channels.find((item) => item.id === change.id);
            if (target) await desktopAPI.adminUpdateChannel(target.id, channelInput(target, {position: change.position}));
        }
    });

    return <div className="admin-accounts">
        <section className="settings-card admin-list-card">
            <h3>{m.admin.channels}</h3>
            <div className="admin-list-actions admin-list-actions-top">
                <button type="button" className="secondary-button" onClick={() => { setCreatingUnder(selected?.id ?? "0"); setSelectedID(""); }}>{m.admin.createChannel}</button>
            </div>
            {error && <div className="device-error" role="alert">{error}</div>}
            <ul className="admin-list admin-channel-tree" aria-busy={busy}>
                {tree.map(({channel, depth}) => {
                    const editable = canEditChannelLevel(view, channel.minJoinLevel ?? 0);
                    return <li key={channel.id} style={{paddingLeft: depth * 14}}>
                        <div className="admin-channel-node">
                            <button type="button" className="admin-account-row" aria-pressed={channel.id === selectedID} onClick={() => { setSelectedID(channel.id); setCreatingUnder(null); }}>
                                <span className="admin-account-main"><span className="admin-account-title">
                                    <span className="admin-account-name" title={channel.name}>{channel.name}</span>
                                    {(channel.minJoinLevel ?? 0) > 0 && <span className="admin-badge">{m.admin.levelBadge(channel.minJoinLevel ?? 0)}</span>}
                                    {channel.id === view.server.defaultChannelId && <span className="admin-badge accent">{m.admin.defaultChannel}</span>}
                                </span></span>
                            </button>
                            <span className="admin-channel-order">
                                <button type="button" className="text-button" title={m.admin.moveUp} aria-label={m.admin.moveUpNamed(channel.name)} disabled={busy || !editable || reorderPositions(channels, channel.id, -1).length === 0}
                                        onClick={() => void move(channel, -1)}>↑</button>
                                <button type="button" className="text-button" title={m.admin.moveDown} aria-label={m.admin.moveDownNamed(channel.name)} disabled={busy || !editable || reorderPositions(channels, channel.id, 1).length === 0}
                                        onClick={() => void move(channel, 1)}>↓</button>
                            </span>
                        </div>
                    </li>;
                })}
            </ul>
        </section>
        {creatingUnder !== null
            ? <ChannelForm key={`new:${creatingUnder}`} view={view} channels={channels} onDone={(id) => { setCreatingUnder(null); if (id) setSelectedID(id); }}
                           initial={{parentId: creatingUnder, name: "", topic: "", description: "", position: String(channels.filter((c) => c.parentId === creatingUnder).length),
                               maxUsers: "0", minJoinLevel: "0", bitrateKbps: "24", application: "voip"}}/>
            : selected
                ? <ChannelForm key={JSON.stringify(selected)} view={view} channels={channels} channel={selected} initial={channelDraft(selected)}
                               onDone={(id) => { if (id === "") setSelectedID(""); }}/>
                : <section className="settings-card admin-detail-card"><p className="device-hint">{m.admin.selectChannel}</p></section>}
    </div>;
}

// onDone receives the ID of a created channel, or "" after a deletion.
function ChannelForm({view, channels, channel, initial, onDone}: {view: ClientViewDTO; channels: ChannelDTO[]; channel?: ChannelDTO; initial: ChannelDraft; onDone: (createdID?: string) => void}) {
    const {m} = useLocale();
    const {error, busy, run} = useStatus();
    const [draft, setDraft] = useState(initial);
    const [confirmDelete, setConfirmDelete] = useState(false);
    const set = (patch: Partial<ChannelDraft>) => setDraft((current) => ({...current, ...patch}));
    const isDefault = channel !== undefined && channel.id === view.server.defaultChannelId;
    const editable = channel === undefined || canEditChannelLevel(view, channel.minJoinLevel ?? 0);
    const parsed = parseDraft(draft);
    const valid = parsed !== null && levelAllowed(view, parsed.minJoinLevel) && (!isDefault || (parsed.minJoinLevel === 0 && parsed.maxUsers === 0));
    const blocked = channel ? descendantIDs(channels, channel.id) : new Set<string>();
    const parentOptions = [{value: "0", label: m.admin.rootChannel},
        ...channelTree(channels).filter(({channel: item}) => !blocked.has(item.id)).map(({channel: item, depth}) => ({value: item.id, label: `${"  ".repeat(depth)}${item.name}`}))];

    const hasChildren = channel !== undefined && channels.some((item) => item.parentId === channel.id);
    const members = channel ? (view.participants ?? []).filter((participant) => participant.channelId === channel.id).length : 0;
    const deletable = channel !== undefined && editable && !isDefault && !hasChildren;
    const remove = () => {
        if (!channel || !deletable) return;
        void run(async () => {
            await desktopAPI.adminDeleteChannel(channel.id);
            onDone("");
        });
    };

    const submit = () => {
        if (!parsed || !valid) return;
        void run(async () => {
            if (channel) {
                await desktopAPI.adminUpdateChannel(channel.id, parsed);
                onDone();
            } else {
                onDone(await desktopAPI.adminCreateChannel(parsed));
            }
        });
    };

    return <section className="settings-card admin-detail-card" aria-busy={busy}>
        <h3>{channel ? channel.name : m.admin.newChannel}{channel && <span className="admin-account-id"> #{channel.id}</span>}</h3>
        {!editable && <p className="device-hint">{m.admin.channelProtected}</p>}
        {isDefault && <p className="device-hint">{m.admin.defaultChannelHint}</p>}
        {error && <div className="device-error" role="alert">{error}</div>}
        <form className="admin-channel-form" onSubmit={(event) => { event.preventDefault(); submit(); }}>
            <label><span className="setting-label">{m.admin.channelName}</span>
                <input value={draft.name} maxLength={64} disabled={busy || !editable} onChange={(event) => set({name: event.target.value})}/></label>
            <label><span className="setting-label">{m.admin.channelTopic}</span>
                <input value={draft.topic} maxLength={128} disabled={busy || !editable} onChange={(event) => set({topic: event.target.value})}/></label>
            <label><span className="setting-label">{m.admin.channelDescription}</span>
                <textarea rows={3} value={draft.description} maxLength={512} disabled={busy || !editable} onChange={(event) => set({description: event.target.value})}/></label>
            <Select id={`channel-parent-${channel?.id ?? "new"}`} label={m.admin.channelParent} value={draft.parentId} options={parentOptions}
                    disabled={busy || !editable} onChange={(value) => set({parentId: value})}/>
            <div className="admin-channel-grid">
                <label><span className="setting-label">{m.admin.channelPosition}</span>
                    <input inputMode="numeric" value={draft.position} disabled={busy || !editable} onChange={(event) => set({position: event.target.value})}/></label>
                <label><span className="setting-label">{m.admin.channelMaxUsers}</span>
                    <input inputMode="numeric" value={draft.maxUsers} disabled={busy || !editable || isDefault} onChange={(event) => set({maxUsers: event.target.value})}/></label>
                <label><span className="setting-label">{m.admin.minJoinLevelLabel}</span>
                    <input inputMode="numeric" value={draft.minJoinLevel} disabled={busy || !editable || isDefault} onChange={(event) => set({minJoinLevel: event.target.value})}/></label>
                <label><span className="setting-label">{m.admin.channelBitrate}</span>
                    <input inputMode="numeric" value={draft.bitrateKbps} disabled={busy || !editable} onChange={(event) => set({bitrateKbps: event.target.value})}/></label>
            </div>
            <Select id={`channel-application-${channel?.id ?? "new"}`} label={m.admin.channelApplication} value={draft.application}
                    options={[{value: "voip", label: m.admin.applicationVoip, detail: m.admin.applicationVoipHint}, {value: "audio", label: m.admin.applicationAudio, detail: m.admin.applicationAudioHint}]}
                    disabled={busy || !editable} onChange={(value) => set({application: value === "audio" ? "audio" : "voip"})}/>
            <p className="device-hint">{m.admin.channelLimitsHint}</p>
            <div className="admin-list-actions">
                <button type="submit" className="primary-button" disabled={busy || !editable || !valid}>{channel ? m.admin.save : m.admin.createChannel}</button>
                {!channel && <button type="button" className="text-button" disabled={busy} onClick={() => onDone()}>{m.admin.cancel}</button>}
            </div>
        </form>
        {channel && !isDefault && <div className="admin-channel-delete">
            {hasChildren && <p className="device-hint">{m.admin.deleteChannelHasChildren}</p>}
            {confirmDelete
                ? <>
                    <p className="device-hint">{m.admin.deleteChannelConfirm(channel.name, members)}</p>
                    <div className="admin-list-actions">
                        <button type="button" className="secondary-button danger-button" disabled={busy || !deletable} onClick={remove}>{m.admin.deleteChannel}</button>
                        <button type="button" className="text-button" disabled={busy} onClick={() => setConfirmDelete(false)}>{m.admin.cancel}</button>
                    </div>
                </>
                : <div className="admin-list-actions">
                    <button type="button" className="secondary-button danger-button" disabled={busy || !deletable} onClick={() => setConfirmDelete(true)}>{m.admin.deleteChannel}</button>
                </div>}
        </div>}
    </section>;
}

function ChannelLevelRow({view, channelID, name, level}: {view: ClientViewDTO; channelID: string; name: string; level: number}) {
    const {m} = useLocale();
    const {error, busy, run} = useStatus();
    const [value, setValue] = useState(String(level));
    useEffect(() => setValue(String(level)), [level]);
    const parsed = parseLevel(value);
    const editable = canEditChannelLevel(view, level);
    const valid = levelAllowed(view, parsed);
    return <form className="admin-inline-form admin-channel-row" onSubmit={(event) => {
        event.preventDefault();
        if (parsed !== null && valid) void run(() => desktopAPI.adminSetChannelJoinLevel(channelID, parsed));
    }}>
        <label><span className="setting-label">{name}</span>
            <input inputMode="numeric" aria-label={m.admin.minJoinLevel(name)} value={value} disabled={busy || !editable} onChange={(event) => setValue(event.target.value)}/></label>
        <button type="submit" className="secondary-button" disabled={busy || !editable || !valid || parsed === level}>{m.admin.save}</button>
        {error && <div className="device-error" role="alert">{error}</div>}
    </form>;
}

function AuditSection() {
    const {m, htmlLang} = useLocale();
    const [entries, setEntries] = useState<AdminAuditEntry[]>([]);
    const [next, setNext] = useState("");
    const [loaded, setLoaded] = useState(false);
    const {error, busy, run} = useStatus();
    const load = useCallback((cursor: string) => run(async () => {
        const page = await desktopAPI.adminAudit(cursor);
        const items = page.entries ?? [];
        setEntries((current) => cursor === "" ? items : [...current, ...items.filter((item) => !current.some((known) => known.id === item.id))]);
        setNext(page.next);
        setLoaded(true);
    }), [run]);
    useEffect(() => { void load(""); }, [load]);
    const format = useMemo(() => new Intl.DateTimeFormat(htmlLang, {dateStyle: "short", timeStyle: "medium"}), [htmlLang]);
    const actorName = (actor: string) => actor === "local-console" ? m.admin.console : m.admin.accountRef(actor);
    return <section className="settings-card">
        <h3>{m.admin.audit}</h3>
        {error && <div className="device-error" role="alert">{error}</div>}
        <table className="admin-audit" aria-busy={busy}>
            <thead><tr><th>{m.admin.auditTime}</th><th>{m.admin.auditActor}</th><th>{m.admin.auditAction}</th><th>{m.admin.auditTarget}</th><th>{m.admin.auditDetail}</th></tr></thead>
            <tbody>
                {entries.map((entry) => <tr key={entry.id}>
                    <td>{entry.happenedAtMs > 0 ? format.format(new Date(entry.happenedAtMs)) : "—"}</td>
                    <td>{actorName(entry.actor)}</td>
                    <td><code>{entry.action}</code></td>
                    <td>{entry.targetUserId ? m.admin.accountRef(entry.targetUserId) : "—"}</td>
                    <td className="admin-audit-detail">{entry.detail}</td>
                </tr>)}
            </tbody>
        </table>
        {loaded && entries.length === 0 && <p className="device-hint">{m.admin.auditEmpty}</p>}
        <div className="admin-list-actions">
            {next && <button type="button" className="secondary-button" disabled={busy} onClick={() => void load(next)}>{busy ? m.admin.loading : m.admin.loadMore}</button>}
            <button type="button" className="text-button" disabled={busy} onClick={() => void load("")}>{m.admin.reload}</button>
        </div>
    </section>;
}
