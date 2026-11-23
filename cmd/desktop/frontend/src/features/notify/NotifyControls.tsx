import {useEffect, useId, useRef, useState, useSyncExternalStore} from "react";
import type {CSSProperties} from "react";
import {desktopAPI, type PresenceStatus} from "../../api";
import {Icon} from "../../components/Icon";
import {useLocale} from "../../locales/Locale";

// One copy of each switch for every view: the footer, the start screen, the
// server header and settings show the same value, and the tray and the hotkey
// change it from Go, which announces "notifications-changed".
function sharedSetting<T>(initial: T, load: () => Promise<T>, save: (value: T) => Promise<void>) {
    let value = initial;
    const listeners = new Set<() => void>();
    const emit = () => { for (const listener of listeners) listener(); };
    const refresh = () => { void load().then((next) => { if (next !== value) { value = next; emit(); } }).catch(() => undefined); };
    let loaded = false;
    return {
        subscribe(listener: () => void) {
            listeners.add(listener);
            if (!loaded) { loaded = true; refresh(); }
            return () => { listeners.delete(listener); };
        },
        get: () => value,
        refresh,
        set(next: T) {
            const previous = value;
            value = next; emit();
            void save(next).catch(() => { value = previous; emit(); });
        },
    };
}

const presence = sharedSetting<PresenceStatus>("online", () => desktopAPI.presenceStatus(), (value) => desktopAPI.setPresenceStatus(value));
const muted = sharedSetting(false, () => desktopAPI.notificationsMuted(), (value) => desktopAPI.setNotificationsMuted(value));
try { desktopAPI.onNotificationsChanged(() => muted.refresh()); } catch { /* Browser preview without the runtime. */ }

export function usePresence(): [PresenceStatus, (value: PresenceStatus) => void] {
    return [useSyncExternalStore(presence.subscribe, presence.get), presence.set];
}

export function useNotificationsMuted(): [boolean, (value: boolean) => void] {
    return [useSyncExternalStore(muted.subscribe, muted.get), muted.set];
}

// NotificationsToggle is the quick "do not disturb" bell of the headers.
export function NotificationsToggle({className = ""}: {className?: string}) {
    const {m} = useLocale();
    const [isMuted, setMuted] = useNotificationsMuted();
    const label = isMuted ? m.notify.unmute : m.notify.mute;
    return <button type="button" className={`notify-toggle${isMuted ? " is-muted" : ""} ${className}`} aria-pressed={isMuted}
        title={isMuted ? `${m.notify.mutedNow}. ${m.notify.unmute}` : label} aria-label={label} onClick={() => setMuted(!isMuted)}>
        <Icon name={isMuted ? "bellOff" : "bell"}/>
    </button>;
}

function PresenceOptions({value, onPick}: {value: PresenceStatus; onPick: (value: PresenceStatus) => void}) {
    const {m} = useLocale();
    return <>{(["online", "invisible"] as const).map((option) => <button key={option} type="button" role="menuitemradio" aria-checked={value === option}
        className={`presence-option${value === option ? " is-selected" : ""}`} onClick={() => onPick(option)}>
        <span className={`presence-dot ${option}`} aria-hidden="true"/>
        <span className="presence-option-text"><strong>{option === "online" ? m.notify.online : m.notify.invisible}</strong><small>{option === "online" ? m.notify.onlineHint : m.notify.invisibleHint}</small></span>
        {value === option && <Icon name="check"/>}
    </button>)}</>;
}

// PresenceAvatar is the footer avatar: its badge shows the status and a click
// opens the status menu above it.
export function PresenceAvatar({initial, style, speaking}: {initial: string; style: CSSProperties; speaking: boolean}) {
    const {m} = useLocale();
    const [value, setValue] = usePresence();
    const [open, setOpen] = useState(false);
    const root = useRef<HTMLDivElement>(null);
    const trigger = useRef<HTMLButtonElement>(null);
    const menuId = useId();
    useEffect(() => {
        if (!open) return;
        const outside = (event: PointerEvent) => { if (!root.current?.contains(event.target as Node)) setOpen(false); };
        const escape = (event: KeyboardEvent) => { if (event.key === "Escape") { event.preventDefault(); setOpen(false); trigger.current?.focus(); } };
        document.addEventListener("pointerdown", outside);
        document.addEventListener("keydown", escape);
        return () => { document.removeEventListener("pointerdown", outside); document.removeEventListener("keydown", escape); };
    }, [open]);
    const status = value === "online" ? m.notify.online : m.notify.invisible;
    return <div ref={root} className="presence-anchor">
        <button ref={trigger} type="button" className={`footer-avatar presence-avatar ${speaking ? "speaking" : ""}`} style={style}
            title={`${m.notify.status}: ${status}. ${m.notify.changeStatus}`} aria-label={`${m.notify.changeStatus}: ${status}`}
            aria-haspopup="menu" aria-expanded={open} aria-controls={open ? menuId : undefined} onClick={() => setOpen((current) => !current)}>
            {initial}<span className={`presence-badge ${value}`} aria-hidden="true"/>
        </button>
        {open && <div id={menuId} className="presence-menu" role="menu" aria-label={m.notify.status}>
            <PresenceOptions value={value} onPick={(next) => { setValue(next); setOpen(false); trigger.current?.focus(); }}/>
        </div>}
    </div>;
}

// PresencePicker is the status block of the start screen, above the recent
// servers: a two-way switch and the meaning of the chosen status.
export function PresencePicker() {
    const {m} = useLocale();
    const [value, setValue] = usePresence();
    const headingId = useId();
    return <section className="connection-presence" aria-labelledby={headingId}>
        <h2 id={headingId}>{m.notify.status}</h2>
        <div className="presence-segments" role="radiogroup" aria-labelledby={headingId}>
            {(["online", "invisible"] as const).map((option) => <button key={option} type="button" role="radio" aria-checked={value === option}
                className={value === option ? "is-selected" : ""} onClick={() => setValue(option)}>
                <span className={`presence-dot ${option}`} aria-hidden="true"/>{option === "online" ? m.notify.online : m.notify.invisible}
            </button>)}
        </div>
        <p className="presence-hint">{value === "online" ? m.notify.onlineHint : m.notify.invisibleHint}</p>
    </section>;
}

// PresenceSelect is the status row of the settings page.
export function PresenceSelect() {
    const {m} = useLocale();
    const [value, setValue] = usePresence();
    return <div className="presence-menu presence-inline" role="menu" aria-label={m.notify.status}>
        <PresenceOptions value={value} onPick={setValue}/>
    </div>;
}

// NotificationsCard is the notification block of the settings page.
export function NotificationsCard() {
    const {m} = useLocale();
    const [isMuted, setMuted] = useNotificationsMuted();
    return <section className="settings-card notify-settings"><h3>{m.notify.settings}</h3>
        <p className="settings-card-hint">{m.notify.aboutHint}</p>
        <div className="setting-toggle">
            <div><strong>{m.notify.mutedSetting}</strong><p>{m.notify.mutedSettingHint}</p></div>
            <label className="switch"><input type="checkbox" checked={isMuted} onChange={(event) => setMuted(event.target.checked)}/><span/></label>
        </div>
        <div className="notify-status-setting">
            <div><strong>{m.notify.status}</strong><p>{m.notify.statusSettingHint}</p></div>
            <PresenceSelect/>
        </div>
    </section>;
}
