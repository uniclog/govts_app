import {useEffect, useRef, useState} from "react";
import type {ClientViewDTO} from "../../api";
import {logDiagnostic} from "../../api";
import {ScreenMediaController} from "./screenMedia";
import {defaultScreenProfile, screenProfiles, type ScreenProfileID} from "./screenProfiles";

const descriptions = {
    economy: "Для слабого соединения",
    text: "Для чтения текста",
    standard: "Для движения и видео",
};

export function useScreenSharing(view: ClientViewDTO, controller: ScreenMediaController, onError: (message: string) => void) {
    const [open, setOpen] = useState(false);
    const [pending, setPending] = useState(false);
    const [publishing, setPublishing] = useState(false);
    const [error, setError] = useState("");
    const [profileID, setProfileID] = useState<ScreenProfileID>(() => {
        try {
            const saved = localStorage.getItem("sonoryx.screenProfile");
            return screenProfiles.find((item) => item.id === saved)?.id ?? defaultScreenProfile.id;
        } catch { return defaultScreenProfile.id; }
    });
    const buttonRef = useRef<HTMLButtonElement>(null);
    const busy = useRef(false);
    const context = `${view.sessionId}:${view.channelId}:${view.connectionStatus}`;
    const currentContext = useRef(context);
    currentContext.current = context;
    const available = view.connectionStatus === "connected" && view.channelId !== "0";
    const own = (view.screenStreams ?? []).some((stream) => stream.ownerSessionId === view.sessionId);
    const active = publishing || own;
    const profile = screenProfiles.find((item) => item.id === profileID) ?? defaultScreenProfile;

    useEffect(() => {
        setOpen(false);
        setError("");
        setPublishing(false);
        return () => {
            void controller.stopPublishing().catch((reason) => logDiagnostic("screen_context_cleanup", reason));
        };
    }, [context, controller]);

    const close = () => {
        setOpen(false);
        if (busy.current) void controller.stopPublishing().catch((reason) => logDiagnostic("screen_cancel_cleanup", reason));
    };
    const selectProfile = (id: ScreenProfileID) => {
        setProfileID(id);
        try { localStorage.setItem("sonoryx.screenProfile", id); } catch { /* Keep the choice for this session. */ }
    };
    const start = async () => {
        if (busy.current || !available) return;
        busy.current = true; setPending(true); setError(""); setOpen(false);
        const startedIn = context;
        try {
            await controller.publish(() => { if (currentContext.current === startedIn) setPublishing(false); }, profile, () => currentContext.current === startedIn);
            if (currentContext.current === startedIn) { setPublishing(true); setOpen(false); }
        } catch (reason) {
            const cancelled = reason instanceof DOMException && ["NotAllowedError", "AbortError"].includes(reason.name)
                || /permission denied by user|user cancel(?:led|ed)/i.test(String(reason));
            if (currentContext.current === startedIn) {
                setOpen(true);
                if (!cancelled) setError(reason instanceof Error ? reason.message : String(reason));
            }
        } finally { busy.current = false; setPending(false); }
    };
    const toggle = async () => {
        if (busy.current || !available) return;
        if (!active) { setError(""); setOpen(true); return; }
        busy.current = true; setPending(true);
        try { await controller.stopPublishing(); }
        catch (reason) { onError(reason instanceof Error ? reason.message : String(reason)); }
        finally { setPublishing(false); busy.current = false; setPending(false); }
    };
    return {open, pending, active, available, profile, profileID, error, buttonRef, close, selectProfile, start, toggle};
}

export type ScreenSharingState = ReturnType<typeof useScreenSharing>;

export function ScreenShareDialog({sharing}: {sharing: ScreenSharingState}) {
    const dialogRef = useRef<HTMLDialogElement>(null);
    const restoreFocus = useRef(false);
    useEffect(() => {
        const dialog = dialogRef.current;
        if (!dialog) return;
        if (sharing.open && !dialog.open) dialog.showModal();
        else if (!sharing.open && dialog.open) {
            dialog.close();
            restoreFocus.current = true;
        }
        if (!sharing.open && !sharing.pending && restoreFocus.current) {
            sharing.buttonRef.current?.focus();
            restoreFocus.current = false;
        }
    }, [sharing.open, sharing.pending, sharing.buttonRef]);
    return <dialog ref={dialogRef} className="screen-share-dialog" aria-labelledby="screen-share-title"
        onCancel={(event) => { event.preventDefault(); sharing.close(); }}>
        <h2 id="screen-share-title">Демонстрация экрана</h2>
        <p>Выберите качество. Затем выберите экран или окно для показа.</p>
        <fieldset disabled={sharing.pending}>
            <legend>Качество</legend>
            {screenProfiles.map((profile) => <label className="screen-quality-option" key={profile.id}>
                <input type="radio" name="screen-quality" value={profile.id} checked={sharing.profileID === profile.id}
                    onChange={() => sharing.selectProfile(profile.id)}/>
                <span><strong>{profile.label}</strong><small>{descriptions[profile.id]}</small></span>
            </label>)}
        </fieldset>
        {sharing.error && <p className="screen-error" role="alert">{sharing.error}</p>}
        <div className="screen-dialog-actions">
            <button type="button" onClick={sharing.close}>Отмена</button>
            <button className="primary-button" type="button" disabled={sharing.pending || !sharing.available}
                onClick={() => {
                    // Remove the modal top layer before WebView2 opens its capture
                    // picker, while keeping getDisplayMedia in the click call chain.
                    dialogRef.current?.close();
                    restoreFocus.current = true;
                    void sharing.start();
                }}>{sharing.pending ? "Запуск…" : "Начать демонстрацию"}</button>
        </div>
    </dialog>;
}
