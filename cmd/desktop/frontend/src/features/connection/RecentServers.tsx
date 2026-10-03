import {useCallback, useEffect, useRef, useState} from "react";
import {desktopAPI, type ClientViewDTO, type RecentServer, type ServerPopulation} from "../../api";

function clientCountLabel(count: number): string {
    const n = Math.abs(count) % 100;
    const last = n % 10;
    if (n > 10 && n < 20) return `${count} клиентов`;
    if (last === 1) return `${count} клиент`;
    if (last >= 2 && last <= 4) return `${count} клиента`;
    return `${count} клиентов`;
}

function populationLabel(value: ServerPopulation | undefined): string {
    if (!value) return "…";
    return value.online ? clientCountLabel(value.clients) : "нет связи";
}

export function RecentServers({view, expanded, onToggle, onReconnect, onError, standalone = false, disabled = false, onSelect, onCountChange}: {
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
    const [servers, setServers] = useState<RecentServer[]>([]);
    const [populations, setPopulations] = useState<Record<string, ServerPopulation>>({});
    useEffect(() => { onCountChange?.(servers.length); }, [servers.length, onCountChange]);
    const addressKey = servers.map((server) => server.address).join("\n");
    useEffect(() => {
        if (!standalone || !addressKey) return;
        let active = true;
        const addresses = addressKey.split("\n");
        const refresh = () => {
            void desktopAPI.serverPopulations(addresses).then((result) => {
                if (!active) return;
                const next: Record<string, ServerPopulation> = {};
                for (const item of result ?? []) next[item.address] = item;
                setPopulations(next);
            }).catch(() => undefined);
        };
        refresh();
        const timer = window.setInterval(refresh, 10000);
        return () => { active = false; window.clearInterval(timer); };
    }, [standalone, addressKey]);
    const [pending, setPending] = useState(false);
    const [editing, setEditing] = useState<string | null>(null);
    const [alias, setAlias] = useState("");
    const [menu, setMenu] = useState<{server: RecentServer; x: number; y: number} | null>(null);
    const menuRef = useRef<HTMLDivElement>(null);
    const editingRef = useRef<string | null>(null);
    const busy = useRef(false);
    const mounted = useRef(true);
    useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
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
        try {
            const result = await desktopAPI.recentServers();
            if (mounted.current) setServers(result ?? []);
        } catch (error) { if (mounted.current) onError(String(error)); }
    }, [onError]);
    useEffect(() => { void load(); }, [load, view.chatContext, view.sessionId, view.connectionStatus]);
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
    return <section className={`sidebar-section recent-servers ${standalone ? "connection-server-list" : ""}`} aria-label="Последние серверы">
        {standalone ? <div className="panel-heading"><h2>Серверы</h2><span className="count-badge">{servers.length}</span></div> :
        <button className="servers-toggle" type="button" aria-expanded={expanded} aria-controls="recent-servers-list" onClick={onToggle}>
            <span className="servers-chevron" aria-hidden="true">{expanded ? "▾" : "▸"}</span><span>Серверы</span><span className="count-badge">{servers.length}</span>
        </button>}
        <div id="recent-servers-list" className="servers-scroll" hidden={!expanded}>
            {servers.map((server) => <div key={server.address} className={`recent-server ${!standalone && server.current ? "current" : ""}`}>
                {editing === server.address ? <input className="server-alias-input" autoFocus maxLength={64} value={alias}
                    aria-label="Название сервера" placeholder={server.address} onChange={(event) => setAlias(event.target.value)}
                    onBlur={saveAlias} onKeyDown={(event) => {
                        if (event.key === "Enter") { event.preventDefault(); event.currentTarget.blur(); }
                        if (event.key === "Escape") { editingRef.current = null; setEditing(null); }
                    }}/> :
                <button type="button" className="recent-server-connect" disabled={pending || disabled || (!standalone && view.connectionStatus !== "connected")}
                        title={`${server.address}${standalone ? `\n${populationLabel(populations[server.address])}` : ""}${!standalone && server.current ? "\nТекущий сервер" : ""}\nДвойной клик — подключиться; правая кнопка мыши — действия; F2 — задать имя`}
                        aria-current={!standalone && server.current ? "true" : undefined}
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
                    <span className="recent-server-label">
                        <span className="recent-server-title"><span>{server.alias || server.address}</span>{!standalone && server.current && <span className="recent-server-current-mark">Текущий</span>}</span>
                        {server.alias && <small>{standalone ? `${server.address} · ${populationLabel(populations[server.address])}` : server.address}</small>}
                        {standalone && !server.alias && <small>{populationLabel(populations[server.address])}</small>}
                    </span>
                </button>}
                <button type="button" className="server-favorite" aria-pressed={server.favorite}
                        aria-label={`${server.favorite ? "Открепить" : "Закрепить"} сервер ${server.alias || server.address}`}
                        title={server.favorite ? "Убрать из избранного" : "Закрепить в избранном"} disabled={pending}
                        onClick={() => void run(() => desktopAPI.setServerFavorite(server.address, !server.favorite))}>
                    <span aria-hidden="true">{server.favorite ? "★" : "☆"}</span>
                </button>
            </div>)}
            {!servers.length && <p className="recent-servers-empty">Здесь появятся посещённые серверы.</p>}
        </div>
        {menu && <div ref={menuRef} className="server-actions-menu" role="group" aria-label="Действия с сервером" style={{left: menu.x, top: menu.y}}>
            <button type="button" autoFocus disabled={pending} onClick={() => { rename(menu.server); setMenu(null); }}>Задать имя</button>
            <button type="button" disabled={pending} onClick={() => {
                const address = menu.server.address;
                setMenu(null);
                void run(() => desktopAPI.deleteRecentServer(address));
            }}>Удалить из списка</button>
        </div>}
    </section>;
}
