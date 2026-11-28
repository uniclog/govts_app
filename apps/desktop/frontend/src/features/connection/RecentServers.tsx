import {useCallback, useEffect, useRef, useState} from "react";
import type {ReactNode} from "react";
import {desktopAPI, type ClientViewDTO, type RecentServer} from "../../api";
import {Icon} from "../../components/Icon";
import type {Messages} from "../../locales";
import {useLocale} from "../../locales/Locale";

function statusTitle(server: RecentServer, m: Messages, htmlLang: string): string {
    const label = server.current ? (server.status === "available" ? m.connection.current : m.connection.currentSync)
        : server.status === "available" ? m.connection.statusOk
        : server.status === "unknown" ? m.connection.statusUnknown
        : m.connection.statusDown;
    const count = server.onlineCount == null ? "" : `\n${server.status === "available" ? m.connection.onlineNow(server.onlineCount) : m.connection.onlineLast(server.onlineCount)}`;
    return `${label}${count}${server.lastAttemptAt ? `\n${m.connection.checkedAt(new Date(server.lastAttemptAt).toLocaleString(htmlLang))}` : ""}${server.updatedAt ? `\n${m.connection.receivedAt(new Date(server.updatedAt).toLocaleString(htmlLang))}` : ""}`;
}

function ServerPopulation({server}: {server: RecentServer}) {
    const {m, htmlLang} = useLocale();
    const count = server.onlineCount;
    const title = statusTitle(server, m, htmlLang);
    return <span className={`server-population ${server.status}`} title={title} role="img" aria-label={title}>
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" aria-hidden="true"><circle cx="12" cy="7" r="3"/><path d="M5 21v-3a7 7 0 0 1 14 0v3"/></svg>
        <span>{count ?? "—"}{server.status === "unavailable" && count != null ? "*" : ""}</span>
    </span>;
}

function serverInitials(alias: string): string {
    const words = alias.trim().split(/\s+/);
    return (words.length > 1 ? words.slice(0, 2).map((word) => Array.from(word)[0]).join("")
        : Array.from(words[0]).slice(0, 2).join("")).toUpperCase();
}

export function RecentServers({view, expanded, onToggle, onReconnect, onError, refreshingServers, onRefreshServers, standalone = false, rail = false, disabled = false, onSelect, onCountChange, selectedAddress, headerAction}: {
    headerAction?: ReactNode;
    rail?: boolean;
    selectedAddress?: string;
    refreshingServers: boolean;
    onRefreshServers: () => void;
    onCountChange?: (count: number) => void;
    standalone?: boolean;
    disabled?: boolean;
    onSelect?: (address: string) => void;
    view: ClientViewDTO;
    expanded: boolean;
    onToggle: () => void;
    onReconnect: (address: string) => Promise<void>;
    onError: (message: string) => void;
}) {
    const {m, htmlLang} = useLocale();
    const [servers, setServers] = useState<RecentServer[]>([]);
    useEffect(() => { onCountChange?.(servers.length); }, [servers.length, onCountChange]);
    const [pending, setPending] = useState(false);
    const [editing, setEditing] = useState<string | null>(null);
    const [alias, setAlias] = useState("");
    const [menu, setMenu] = useState<{server: RecentServer; x: number; y: number} | null>(null);
    const [copied, setCopied] = useState<string | null>(null);
    const copiedTimer = useRef<number | undefined>(undefined);
    const menuRef = useRef<HTMLDivElement>(null);
    const editingRef = useRef<string | null>(null);
    const busy = useRef(false);
    const mounted = useRef(true);
    const loadGeneration = useRef(0);
    useEffect(() => { mounted.current = true; return () => { mounted.current = false; window.clearTimeout(copiedTimer.current); }; }, []);
    useEffect(() => {
        if (!menu) return;
        const outside = (event: PointerEvent) => { if (!menuRef.current?.contains(event.target as Node)) setMenu(null); };
        const escape = (event: KeyboardEvent) => { if (event.key === "Escape") setMenu(null); };
        const close = () => setMenu(null);
        document.addEventListener("pointerdown", outside);
        document.addEventListener("keydown", escape);
        window.addEventListener("resize", close);
        document.addEventListener("scroll", close, true);
        return () => {
            document.removeEventListener("pointerdown", outside);
            document.removeEventListener("keydown", escape);
            window.removeEventListener("resize", close);
            document.removeEventListener("scroll", close, true);
        };
    }, [menu]);
    useEffect(() => { setMenu(null); }, [expanded, view.chatContext, view.connectionStatus]);
    const load = useCallback(async () => {
        const generation = ++loadGeneration.current;
        try {
            const result = await desktopAPI.recentServers();
            if (mounted.current && generation === loadGeneration.current) setServers(result ?? []);
        } catch (error) { if (mounted.current && generation === loadGeneration.current) onError(String(error)); }
    }, [onError]);
    const visibleParticipantCount = expanded ? view.participants?.length : undefined;
    const visibleSnapshotFresh = expanded ? view.snapshotFresh : undefined;
    useEffect(() => { void load(); }, [load, view.chatContext, view.sessionId, view.connectionStatus, visibleParticipantCount, visibleSnapshotFresh, expanded]);
    useEffect(() => desktopAPI.onServerStatusChanged(() => { if (expanded) void load(); }), [load, expanded]);
    const run = async (operation: () => Promise<void>) => {
        if (busy.current) return;
        busy.current = true; setPending(true);
        try { await operation(); await load(); }
        catch (error) { onError(String(error)); }
        finally { busy.current = false; if (mounted.current) setPending(false); }
    };
    const reconnect = (server: RecentServer) => {
        if (disabled || server.current || (!standalone && view.connectionStatus !== "connected")) return;
        void run(() => onReconnect(server.address));
    };
    const rename = (server: RecentServer) => {
        if (busy.current) return;
        editingRef.current = server.address;
        setAlias(server.alias || ""); setEditing(server.address);
    };
    const saveAlias = () => {
        const address = editingRef.current;
        editingRef.current = null; setEditing(null);
        if (address) void run(() => desktopAPI.setServerAlias(address, alias));
    };
    const share = async (address: string) => {
        try {
            await navigator.clipboard.writeText(address);
            setCopied(address);
            window.clearTimeout(copiedTimer.current);
            copiedTimer.current = window.setTimeout(() => setCopied(null), 1500);
        } catch { onError(m.connection.shareFailed); }
    };
    return <section className={`sidebar-section recent-servers ${standalone ? "connection-server-list" : ""} ${rail ? "server-rail-list" : ""}`} aria-label={m.connection.recent}>
        <div className={`servers-heading ${standalone ? "panel-heading" : ""}`}>
        {standalone ? <h2>{m.connection.recentHeading}</h2> :
        <button className="servers-toggle" type="button" aria-expanded={expanded} aria-controls="recent-servers-list" onClick={onToggle}>
            <span className="servers-chevron" aria-hidden="true">{expanded ? "▾" : "▸"}</span><span>{m.connection.servers}</span>
        </button>}
        <button type="button" className="servers-refresh" tabIndex={-1} disabled={refreshingServers || servers.length === 0}
                title={refreshingServers ? m.connection.refreshing : m.connection.refresh} aria-label={m.connection.refresh} aria-busy={refreshingServers}
                onMouseDown={(event) => event.preventDefault()}
                onKeyDown={(event) => { if (event.key === "Enter" || event.key === " ") event.preventDefault(); }}
                onClick={(event) => { if (event.button === 0 && event.detail > 0) onRefreshServers(); }}>
            <Icon name="refresh"/>
        </button>
        <span className="count-badge">{servers.length}</span>
        {headerAction}
        </div>
        <div id="recent-servers-list" className="servers-scroll" hidden={!expanded}>
            {servers.filter((server) => !rail || !server.current).map((server) => <div key={server.address} className={`recent-server${standalone && server.address === selectedAddress ? " selected" : ""}`}>
                {editing === server.address ? <input className="server-alias-input" autoFocus maxLength={64} value={alias}
                    aria-label={m.connection.alias} placeholder={server.address} onChange={(event) => setAlias(event.target.value)}
                    onBlur={saveAlias} onKeyDown={(event) => {
                        if (event.key === "Enter") { event.preventDefault(); event.currentTarget.blur(); }
                        if (event.key === "Escape") { editingRef.current = null; setEditing(null); }
                    }}/> :
                <button type="button" className="recent-server-connect" disabled={pending || disabled || (!standalone && view.connectionStatus !== "connected")}
                        title={`${server.alias ? `${server.alias}\n` : ""}${server.address}${server.version ? `\n${m.connection.serverVersion(server.version)}` : ""}\n${m.connection.serverHint}`}
                        aria-label={rail ? `${server.alias || server.address}. ${m.connection.serverHint}` : undefined}
                        aria-current={server.current || (standalone && server.address === selectedAddress) ? "true" : undefined}
                        onClick={() => onSelect?.(server.address)}
                        onContextMenu={(event) => {
                            event.preventDefault();
                            setMenu({server, x: Math.max(8, Math.min(event.clientX, window.innerWidth - 180)), y: Math.max(8, Math.min(event.clientY, window.innerHeight - 90))});
                        }}
                        onDoubleClick={() => reconnect(server)}
                        onKeyDown={(event) => {
                            if (event.key === "F2") { event.preventDefault(); rename(server); }
                            if (event.key === "Delete") { event.preventDefault(); void run(() => desktopAPI.deleteRecentServer(server.address)); }
                            if (event.key === "Enter" && !event.repeat) { event.preventDefault(); reconnect(server); }
                        }}>
                    {rail ? <span className="rail-server-avatar" aria-hidden="true">{server.alias?.trim() ? serverInitials(server.alias) : <Icon name="globe"/>}<i className={server.status}/></span> : standalone ? <span className="connection-server-avatar">
                        <span aria-hidden="true">{server.alias?.trim() ? serverInitials(server.alias) : <Icon name="globe"/>}</span>
                        <span className={`connection-server-status ${server.status}`} role="img" title={statusTitle(server, m, htmlLang)} aria-label={statusTitle(server, m, htmlLang)}/>
                    </span> : <span className={`server-availability ${server.status}`} role="img" title={statusTitle(server, m, htmlLang)} aria-label={statusTitle(server, m, htmlLang)}/>}
                    <span className="recent-server-label"><span>{server.alias || server.address}</span>{server.alias ? <small>{server.address}</small> : standalone && <small>{server.status === "available" ? m.connection.availableShort : server.status === "unavailable" ? m.connection.unavailableShort : m.connection.unknownShort}</small>}{server.version && <small className="recent-server-version" title={server.current ? m.connection.serverVersion(server.version) : m.connection.lastServerVersion(server.version)}>{m.connection.serverVersion(server.version)}</small>}</span>
                </button>}
                <ServerPopulation server={server}/>
                {standalone && <button type="button" className="server-share" aria-label={m.connection.shareLabel(server.alias || server.address)}
                        title={copied === server.address ? m.connection.shareCopied : m.connection.share}
                        onClick={() => void share(server.address)}>
                    <Icon name={copied === server.address ? "check" : "share"}/>
                </button>}
                <button type="button" className="server-favorite" aria-pressed={server.favorite}
                        aria-label={m.connection.favoriteLabel(server.favorite ? m.connection.unpin : m.connection.pin, server.alias || server.address)}
                        title={server.favorite ? m.connection.favoriteOff : m.connection.favoriteOn} disabled={pending}
                        onClick={() => void run(() => desktopAPI.setServerFavorite(server.address, !server.favorite))}>
                    <svg viewBox="0 0 24 24" stroke="currentColor" strokeWidth="1.6" strokeLinejoin="round" aria-hidden="true"><path d="m12 3 2.8 5.7 6.3.9-4.6 4.5 1.1 6.3-5.6-3-5.6 3 1.1-6.3L2.9 9.6l6.3-.9Z"/></svg>
                </button>
            </div>)}
            {!servers.length && <p className="recent-servers-empty">{m.connection.empty}</p>}
        </div>
        {menu && <div ref={menuRef} className="server-actions-menu" role="group" aria-label={m.connection.serverActions} style={{left: menu.x, top: menu.y}}>
            <button type="button" autoFocus disabled={pending} onClick={() => { rename(menu.server); setMenu(null); }}>{m.connection.rename}</button>
            <button type="button" disabled={pending} onClick={() => {
                const address = menu.server.address;
                setMenu(null);
                void run(() => desktopAPI.deleteRecentServer(address));
            }}>{m.connection.remove}</button>
        </div>}
    </section>;
}
