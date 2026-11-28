import {useCallback, useEffect, useRef, useState} from "react";
import type {KeyboardEvent} from "react";
import {desktopAPI, type Hotkey, type HotkeyAction} from "../../api";
import {errorCode, errorText} from "../../locales";
import {useLocale} from "../../locales/Locale";
import {CaptureReleases} from "./captureReleases";

// event.code names the physical key, so the layout and Caps Lock do not matter.
function physicalKey(code: string): string | null {
    const letter = /^Key([A-Z])$/.exec(code);
    if (letter) return letter[1];
    const digit = /^Digit([0-9])$/.exec(code);
    if (digit) return digit[1];
    return /^F([1-9]|1[0-2])$/.test(code) ? code : null;
}

const modifierCodes = /^(Control|Alt|Shift|Meta|OS)(Left|Right)?$|^CapsLock$/;

type CaptureSession = {
    action: HotkeyAction;
    token: number | null;
    pending: {code: string; shortcut: string} | null;
    saving: boolean;
    rightAlt: boolean;
};

// Serialize starts and saves across mounts. Releases use recording IDs and
// may retry independently without cancelling a newer recording.
let captureQueue: Promise<unknown> = Promise.resolve();
function queueCapture<T>(operation: () => Promise<T>): Promise<T> {
    const result = captureQueue.catch(() => undefined).then(operation);
    captureQueue = result.catch(() => undefined);
    return result;
}

const captureReleases = new CaptureReleases((token) => desktopAPI.endHotkeyCapture(token));

export function HotkeySettings() {
    const {m} = useLocale();
    const [hotkeys, setHotkeys] = useState<Hotkey[]>([]);
    const [loadError, setLoadError] = useState(false);
    const [releaseError, setReleaseError] = useState(false);
    const [editing, setEditing] = useState<{action: HotkeyAction; ready: boolean} | null>(null);
    const captureSession = useRef<CaptureSession | null>(null);
    const mounted = useRef(true);
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
    const cancelCapture = useCallback((): Promise<void> => {
        const session = captureSession.current;
        captureSession.current = null;
        if (mounted.current) setEditing(null);
        const token = session?.token;
        if (token != null) {
            return captureReleases.release(token);
        }
        // An in-flight Begin releases its own token when it sees cancellation.
        return Promise.resolve();
    }, []);
    const reportReleaseFailure = useCallback(() => {
        if (mounted.current) setReleaseError(true);
    }, []);
    useEffect(() => {
        mounted.current = true;
        const cancel = () => { void cancelCapture().catch(reportReleaseFailure); };
        const visibility = () => { if (document.hidden) cancel(); };
        const unsubscribe = captureReleases.onRecovered(() => {
            if (mounted.current) setReleaseError(false);
        });
        window.addEventListener("blur", cancel);
        document.addEventListener("visibilitychange", visibility);
        return () => {
            mounted.current = false;
            window.removeEventListener("blur", cancel);
            document.removeEventListener("visibilitychange", visibility);
            unsubscribe();
            cancel();
        };
    }, [cancelCapture, reportReleaseFailure]);
    useEffect(() => {
        const session = captureSession.current;
        if (!editing?.ready || !session || session.token === null) return;
        const token = session.token;
        let renewing = false;
        const timer = setInterval(() => {
            if (renewing || captureSession.current !== session) return;
            renewing = true;
            void desktopAPI.renewHotkeyCapture(token).catch((error) => {
                if (mounted.current && captureSession.current === session) {
                    setMessage({action: session.action, text: errorText(error, m)});
                    void cancelCapture().catch(reportReleaseFailure);
                }
            }).finally(() => { renewing = false; });
        }, 3000);
        return () => clearInterval(timer);
    }, [editing, m, cancelCapture, reportReleaseFailure]);
    const beginCapture = async (action: HotkeyAction, button: HTMLButtonElement) => {
        const previous = captureSession.current;
        void cancelCapture().catch(reportReleaseFailure);
        if (previous?.action === action) return;
        const session: CaptureSession = {action, token: null, pending: null, saving: false, rightAlt: false};
        captureSession.current = session;
        setEditing({action, ready: false});
        setMessage(null);
        try {
            await queueCapture(async () => {
                if (captureSession.current !== session) return;
                const token = await desktopAPI.beginHotkeyCapture();
                session.token = token;
                if (captureSession.current !== session || !mounted.current ||
                    document.hidden || !document.hasFocus() || document.activeElement !== button) {
                    await captureReleases.release(token);
                    if (captureSession.current === session) {
                        captureSession.current = null;
                        if (mounted.current) setEditing(null);
                    }
                    return;
                }
                setEditing({action, ready: true});
            });
        } catch (error) {
            if (mounted.current && captureSession.current === session) {
                setMessage({action, text: errorText(error, m)});
                await cancelCapture().catch(reportReleaseFailure);
            }
        }
    };
    const capture = (action: HotkeyAction, event: KeyboardEvent<HTMLButtonElement>) => {
        if (event.key === "Tab") return;
        event.preventDefault();
        const session = captureSession.current;
        if (!session || session.action !== action || session.saving) return;
        if (event.key === "Escape") {
            void cancelCapture().catch(reportReleaseFailure);
            setMessage(null);
            return;
        }
        if (event.code === "AltRight") session.rightAlt = true;
        if (session.token === null) return;
        if (modifierCodes.test(event.code)) return;
        if (event.repeat || session.pending || session.rightAlt || event.getModifierState("AltGraph")) return;
        const key = physicalKey(event.code);
        if (!key) {
            setMessage({action, text: errorText(errorCode("hotkey-key-invalid"), m)});
            return;
        }
        setMessage(null);
        session.pending = {code: event.code, shortcut: [
            event.ctrlKey && "Ctrl", event.altKey && "Alt",
            event.shiftKey && "Shift", event.metaKey && "Win", key,
        ].filter(Boolean).join("+")};
    };
    const finishCapture = async (event: KeyboardEvent<HTMLButtonElement>) => {
        const session = captureSession.current;
        if (!session) return;
        if (event.code === "AltRight") session.rightAlt = false;
        const value = session.pending;
        const token = session.token;
        if (!value || token === null || event.code !== value.code || session.saving) return;
        event.preventDefault();
        session.pending = null;
        session.saving = true;
        try {
            await queueCapture(async () => {
                if (captureSession.current === session) {
                    await desktopAPI.setCapturedHotkey(token, session.action, value.shortcut);
                }
            });
        } catch (error) {
            if (mounted.current && captureSession.current === session) {
                setMessage({action: session.action, text: errorText(error, m)});
            }
        } finally {
            // Rearm after saving and release so the recorded key cannot run an action.
            if (captureSession.current === session) await cancelCapture().catch(reportReleaseFailure);
        }
        if (mounted.current && !captureSession.current) await load();
    };
    return <section className="settings-card"><h3>{m.settings.hotkeys}</h3>
        <p className="device-hint">{m.settings.hotkeysHint}</p>
        {releaseError && <p className="device-hint monitor-warning" role="alert">{m.errors["hotkey-release-failed"]}</p>}
        {loadError && <p className="device-hint monitor-warning" role="alert">{m.settings.hotkeysLoadFailed} <button type="button" className="text-button" onClick={() => void load()}>{m.settings.retry}</button></p>}
        <div className="setting-toggle">
            <div><strong>{m.settings.hotkeysEnabled}</strong><p>{m.settings.hotkeysEnabledHint}</p></div>
            <label className="switch"><input type="checkbox" checked={enabled}
                                             onChange={(event) => void toggleEnabled(event.target.checked)}/><span/></label>
        </div>
        {hotkeys.map((hotkey) => {
            const active = editing?.action === hotkey.action;
            return <div key={hotkey.action} className="setting-toggle hotkey-setting">
                <div>
                    <strong>{m.settings.hotkeyActions[hotkey.action]}</strong>
                    {message?.action === hotkey.action
                        ? <p className="monitor-warning" role="alert">{message.text}</p>
                        : active
                            ? <p>{editing.ready ? m.settings.hotkeyPress : m.settings.hotkeyPreparing}</p>
                            : hotkey.status === "failed"
                            ? <p className="monitor-warning">{m.settings.hotkeyFailed}</p>
                            : <p>{m.settings.hotkeyChange}</p>}
                </div>
                <span className="hotkey-combo">
                    <button type="button" className={`monitor-button hotkey-key ${active ? "active" : ""}`}
                            aria-pressed={active} aria-label={m.settings.hotkeyEdit(m.settings.hotkeyActions[hotkey.action])}
                            aria-busy={active && !editing.ready}
                            onClick={(event) => { void beginCapture(hotkey.action, event.currentTarget); }}
                            onBlur={() => { if (captureSession.current?.action === hotkey.action) void cancelCapture().catch(reportReleaseFailure); }}
                            onKeyDown={(event) => { if (active) capture(hotkey.action, event); }}
                            onKeyUp={(event) => { if (active) void finishCapture(event); }}>
                        {active ? "…" : hotkey.key.split("+").join(" + ")}
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
