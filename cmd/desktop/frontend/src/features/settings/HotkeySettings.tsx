import {useCallback, useEffect, useState} from "react";
import type {KeyboardEvent} from "react";
import {desktopAPI, type Hotkey, type HotkeyAction} from "../../api";
import {errorCode, errorText} from "../../locales";
import {useLocale} from "../../locales/Locale";

// event.code names the physical key, so the layout and Caps Lock do not matter.
function physicalKey(code: string): string | null {
    const letter = /^Key([A-Z])$/.exec(code);
    if (letter) return letter[1];
    const digit = /^Digit([0-9])$/.exec(code);
    return digit ? digit[1] : null;
}

const modifierCodes = /^(Control|Alt|Shift|Meta|OS)(Left|Right)?$|^CapsLock$/;

export function HotkeySettings() {
    const {m} = useLocale();
    const [hotkeys, setHotkeys] = useState<Hotkey[]>([]);
    const [loadError, setLoadError] = useState(false);
    const [editing, setEditing] = useState<HotkeyAction | null>(null);
    const [message, setMessage] = useState<{action: HotkeyAction; text: string} | null>(null);
    const load = useCallback(async () => {
        try {
            const list = await desktopAPI.hotkeys();
            if (!list) throw new Error("hotkeys");
            setHotkeys(list);
            setLoadError(false);
        } catch {
            setLoadError(true);
        }
    }, []);
    useEffect(() => { void load(); }, [load]);
    const [enabled, setEnabled] = useState(false);
    const [notifications, setNotifications] = useState(true);
    useEffect(() => {
        let active = true;
        void desktopAPI.hotkeysEnabled().then((value) => {
            if (active) setEnabled(value);
        }).catch(() => undefined);
        void desktopAPI.hotkeyNotifications().then((value) => {
            if (active) setNotifications(value);
        }).catch(() => undefined);
        return () => { active = false; };
    }, []);
    const toggleEnabled = async (value: boolean) => {
        setEnabled(value);
        try {
            await desktopAPI.setHotkeysEnabled(value);
            await load();
        } catch {
            setEnabled(!value);
        }
    };
    const toggleNotifications = async (value: boolean) => {
        setNotifications(value);
        try {
            await desktopAPI.setHotkeyNotifications(value);
        } catch {
            setNotifications(!value);
        }
    };
    const capture = async (action: HotkeyAction, event: KeyboardEvent<HTMLButtonElement>) => {
        if (event.key === "Tab") return;
        event.preventDefault();
        if (event.key === "Escape") {
            setEditing(null);
            setMessage(null);
            return;
        }
        if (modifierCodes.test(event.code)) return;
        const key = physicalKey(event.code);
        if (!key) {
            setMessage({action, text: errorText(errorCode("hotkey-key-invalid"), m)});
            return;
        }
        setEditing(null);
        setMessage(null);
        try {
            await desktopAPI.setHotkey(action, key);
        } catch (error) {
            setMessage({action, text: errorText(error, m)});
        }
        await load();
    };
    return <section className="settings-card"><h3>{m.settings.hotkeys}</h3>
        <p className="device-hint">{m.settings.hotkeysHint}</p>
        {loadError && <p className="device-hint monitor-warning" role="alert">{m.settings.hotkeysLoadFailed} <button type="button" className="text-button" onClick={() => void load()}>{m.settings.retry}</button></p>}
        <div className="setting-toggle">
            <div><strong>{m.settings.hotkeysEnabled}</strong><p>{m.settings.hotkeysEnabledHint}</p></div>
            <label className="switch"><input type="checkbox" checked={enabled}
                                             onChange={(event) => void toggleEnabled(event.target.checked)}/><span/></label>
        </div>
        {hotkeys.map((hotkey) => {
            const active = editing === hotkey.action;
            return <div key={hotkey.action} className="setting-toggle hotkey-setting">
                <div>
                    <strong>{m.settings.hotkeyActions[hotkey.action]}</strong>
                    {message?.action === hotkey.action
                        ? <p className="monitor-warning" role="alert">{message.text}</p>
                        : hotkey.status === "failed"
                            ? <p className="monitor-warning">{m.settings.hotkeyFailed}</p>
                            : <p>{active ? m.settings.hotkeyPress : m.settings.hotkeyChange}</p>}
                </div>
                <span className="hotkey-combo">
                    <kbd>Ctrl</kbd>+<kbd>Alt</kbd>+
                    <button type="button" className={`monitor-button hotkey-key ${active ? "active" : ""}`}
                            aria-pressed={active} aria-label={m.settings.hotkeyEdit(m.settings.hotkeyActions[hotkey.action])}
                            onClick={() => { setEditing(active ? null : hotkey.action); setMessage(null); }}
                            onBlur={() => { if (active) setEditing(null); }}
                            onKeyDown={(event) => { if (active) void capture(hotkey.action, event); }}>
                        {active ? "…" : hotkey.key}
                    </button>
                </span>
            </div>;
        })}
        <div className="setting-toggle">
            <div><strong>{m.settings.hotkeyNotifications}</strong><p>{m.settings.hotkeyNotificationsHint}</p></div>
            <label className="switch"><input type="checkbox" checked={notifications}
                                             onChange={(event) => void toggleNotifications(event.target.checked)}/><span/></label>
        </div>
    </section>;
}
