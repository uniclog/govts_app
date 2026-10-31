import {useEffect, useRef, useState} from "react";
import type {ClientViewDTO} from "../../api";
import {Icon} from "../../components/Icon";
import {useLocale} from "../../locales/Locale";
import {RecentServers} from "./RecentServers";
import {VoiceMark} from "./ConnectionViews";

export function ServerSwitcher({view, refreshingServers, onRefreshServers, onReconnect, onError}: {
    view: ClientViewDTO;
    refreshingServers: boolean;
    onRefreshServers: () => void;
    onReconnect: (address: string) => Promise<void>;
    onError: (message: string) => void;
}) {
    const {m} = useLocale();
    const [open, setOpen] = useState(false);
    const headerRef = useRef<HTMLElement>(null);
    const triggerRef = useRef<HTMLButtonElement>(null);
    const close = () => { setOpen(false); triggerRef.current?.focus(); };
    useEffect(() => { setOpen(false); }, [view.sessionId]);
    useEffect(() => {
        if (!open) return;
        const outside = (event: PointerEvent) => {
            if (!headerRef.current?.contains(event.target as Node)) setOpen(false);
        };
        const escape = (event: KeyboardEvent) => {
            if (event.key === "Escape") { event.preventDefault(); setOpen(false); triggerRef.current?.focus(); }
        };
        document.addEventListener("pointerdown", outside);
        document.addEventListener("keydown", escape);
        return () => {
            document.removeEventListener("pointerdown", outside);
            document.removeEventListener("keydown", escape);
        };
    }, [open]);
    return <header className="workspace-server-header server-switcher" ref={headerRef}>
        <button ref={triggerRef} type="button" className="server-switcher-trigger" title={m.connection.switchServer}
            aria-label={`${m.connection.switchServer}: ${view.server.name || m.connection.server}`}
            aria-expanded={open} aria-controls="server-switcher-panel" onClick={() => setOpen((value) => !value)}>
            <span className="server-switcher-brand"><VoiceMark/></span>
            <strong>{view.server.name || m.connection.server}</strong><Icon name="chevronDown"/>
        </button>
        {view.server.version && <small className="workspace-server-version">{view.server.version}</small>}
        {open && <div id="server-switcher-panel" className="server-switcher-popover" role="region" aria-label={m.connection.servers}>
            <button className="server-switcher-close" type="button" title={m.connection.closeServers} aria-label={m.connection.closeServers} onClick={close}><Icon name="close"/></button>
            <RecentServers standalone view={view} expanded onToggle={() => {}} disabled={view.connectionStatus !== "connected"}
                refreshingServers={refreshingServers} onRefreshServers={onRefreshServers} onError={onError}
                onReconnect={async (address) => { await onReconnect(address); setOpen(false); }}/>
        </div>}
    </header>;
}
