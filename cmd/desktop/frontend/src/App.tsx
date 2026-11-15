import {useCallback, useEffect, useRef, useState} from "react";
import type {ClientEventDTO, ClientViewDTO} from "./api";
import {desktopAPI} from "./api";
import {mergeEventTail} from "./model";
import {ScreenMediaController} from "./features/screen/screenMedia";
import {ScreenViewerWindow} from "./features/screen/ScreenViews";
import {LinkConfirmDialog} from "./features/chat/LinkConfirm";
import {MediaTrustDialog} from "./features/screen/MediaTrustConfirm";
import {ScreenShareDialog, useScreenSharing} from "./features/screen/ScreenShareDialog";
import {ConnectionPage, LobbyBar, StatusBar, type Page} from "./features/connection/ConnectionViews";
import {rememberServerAddress} from "./features/connection/ConnectionViews";
import {WindowControls} from "./components/WindowControls";
import {ChatStore} from "./features/chat/chatStore";
import {UpdateButton, UpdateDialog, useUpdates} from "./features/updates/Updates";
import {applyAppearance, createCustomAppearance, downloadThemeFile, loadAppearance, maxSavedThemes, normalizeTheme, readThemeFile, saveAppearance, snapshotTheme, type Appearance, type SavedTheme} from "./appearance";
import {attentionLockSeconds, errorCode, errorText, rawError} from "./locales";
import {freshWakeGate, wakeClock, wakeDecision, wakeFailed, wakeRejected, wakeSent, type WakeGate} from "./features/participants/wakeLimit";
import {useLocale} from "./locales/Locale";
import {AdminPanel} from "./features/admin/AdminPanel";
import {ChannelsPage, type ContentTab, type DirectChat} from "./features/channels/ChannelsPage";
import {WakeNotice} from "./features/participants/WakeNotice";
import {SettingsOverlay, SettingsPage} from "./features/settings/SettingsPage";

const emptyView: ClientViewDTO = {
    chatContext: "", userId: "0", chatRevision: "0",
    connectionStatus: "disconnected",
    server: {name: ""}, revision: "0", sessionId: "0", channelId: "0", snapshotFresh: false,
    canKick: false, canBan: false, canDrag: false, canAdmin: false, isOwner: false, joinLevel: 0,
    channels: [], participants: [], screenStreams: [],
    audio: {
        muted: false,
        deafened: false,
        captureAvailable: true,
        rnnoiseEnabled: true,
        rnnoiseSensitivity: 1,
        microphoneGain: 1,
        vadEnabled: false,
        vadMode: "hybrid",
        vadSensitivity: 0.5,
        vadOpen: false
    },
    soundPack: "soft",
    soundVolume: 0.8,
    muteOnAway: true,
    deafenOnAway: false,
    soundCues: null,
};

function App() {
    const {m} = useLocale();
    const [appearance, setAppearanceState] = useState(loadAppearance);
    useEffect(() => {
        applyAppearance(loadAppearance());
        let active = true;
        void desktopAPI.theme().then((value) => {
            if (!active) return;
            const current = loadAppearance();
            if (current.theme === "custom") {
                setAppearanceState(current);
                return;
            }
            const mode = normalizeTheme(value);
            if (current.theme === mode) {
                setAppearanceState(current);
                return;
            }
            const next = saveAppearance({...current, theme: mode});
            applyAppearance(next);
            setAppearanceState(next);
        }).catch(() => undefined);
        return () => { active = false; };
    }, []);
    const remember = (next: Appearance) => {
        const saved = saveAppearance(next);
        applyAppearance(saved);
        setAppearanceState(saved);
        return saved;
    };
    const setTheme = async (value: string) => {
        const previous = loadAppearance();
        const mode = normalizeTheme(value);
        if (mode === "custom") {
            if (!previous.hasCustom && !previous.hasDraft) return;
            remember({...previous, theme: "custom", activeSavedId: previous.hasDraft ? "" : previous.activeSavedId});
            return;
        }
        remember({...previous, theme: mode});
        try { await desktopAPI.setTheme(mode); }
        catch (error) {
            remember(previous);
            throw error;
        }
    };
    const createCustomTheme = () => {
        remember(createCustomAppearance(loadAppearance()));
    };
    const updateAppearance = (patch: Partial<Appearance>) => {
        const current = loadAppearance();
        const next: Appearance = {...current, ...patch, custom: patch.custom ?? current.custom, savedThemes: current.savedThemes};
        if (next.theme === "custom" && next.activeSavedId) {
            next.savedThemes = next.savedThemes.map((theme) => theme.id === next.activeSavedId ? {
                ...theme,
                palette: {...next.custom},
                fontFamily: next.fontFamily,
                fontSize: next.fontSize,
                stageBackdrop: next.stageBackdrop,
                stageImage: next.stageBackdrop === "image" ? next.stageImage : "",
            } : theme);
        }
        remember(next);
    };
    const openSavedTheme = (id: string) => {
        const current = loadAppearance();
        const saved = current.savedThemes.find((theme) => theme.id === id);
        if (!saved) return;
        remember({
            ...current,
            theme: "custom",
            hasCustom: true,
            hasDraft: false,
            activeSavedId: saved.id,
            customName: saved.name,
            custom: {...saved.palette},
            fontFamily: saved.fontFamily,
            fontSize: saved.fontSize,
            stageBackdrop: saved.stageBackdrop,
            stageImage: saved.stageImage,
        });
    };
    const saveNamedTheme = (name: string) => {
        const current = loadAppearance();
        if (current.theme !== "custom") return;
        const snapshot = snapshotTheme({...current, customName: name}, name);
        if (current.activeSavedId) {
            remember({
                ...current,
                customName: snapshot.name,
                hasDraft: false,
                savedThemes: current.savedThemes.map((theme) => theme.id === current.activeSavedId ? {...snapshot, id: current.activeSavedId} : theme),
            });
            return;
        }
        if (current.savedThemes.length >= maxSavedThemes) throw new Error(errorCode("theme-limit"));
        remember({
            ...current,
            theme: "custom",
            hasCustom: true,
            hasDraft: false,
            activeSavedId: snapshot.id,
            customName: snapshot.name,
            savedThemes: [...current.savedThemes, snapshot],
        });
    };
    const importThemeFile = async (file: File) => {
        const theme = await readThemeFile(file);
        const current = loadAppearance();
        if (current.savedThemes.length >= maxSavedThemes) throw new Error(errorCode("theme-limit"));
        remember({
            ...current,
            theme: "custom",
            hasCustom: true,
            hasDraft: false,
            activeSavedId: theme.id,
            customName: theme.name,
            custom: {...theme.palette},
            fontFamily: theme.fontFamily,
            fontSize: theme.fontSize,
            stageBackdrop: theme.stageBackdrop,
            stageImage: theme.stageImage,
            savedThemes: [...current.savedThemes, theme],
        });
    };
    const deleteSavedTheme = (id: string) => {
        const current = loadAppearance();
        const savedThemes = current.savedThemes.filter((theme) => theme.id !== id);
        const active = current.activeSavedId === id;
        remember({
            ...current,
            savedThemes,
            hasCustom: savedThemes.length > 0 || current.theme === "custom" || current.hasDraft,
            activeSavedId: active ? "" : current.activeSavedId,
            customName: active ? "" : current.customName,
            hasDraft: active || current.hasDraft,
        });
    };
    const exportTheme = (theme: SavedTheme) => {
        if (theme.id === "draft" && loadAppearance().theme !== "custom") return;
        downloadThemeFile(theme);
    };
    const params = new URLSearchParams(window.location.search);
    const streamID = params.get("screen");
    if (streamID) return <><ScreenViewerWindow streamID={streamID} ownerName={params.get("owner") || m.screen.participant}/><MediaTrustDialog/></>;
    return <MainApp appearance={appearance} setTheme={setTheme} createCustomTheme={createCustomTheme} updateAppearance={updateAppearance} openSavedTheme={openSavedTheme} saveNamedTheme={saveNamedTheme} importThemeFile={importThemeFile} deleteSavedTheme={deleteSavedTheme} exportTheme={exportTheme}/>;
}

function MainApp({appearance, setTheme, createCustomTheme, updateAppearance, openSavedTheme, saveNamedTheme, importThemeFile, deleteSavedTheme, exportTheme}: {
    appearance: Appearance;
    setTheme: (value: string) => Promise<void>;
    createCustomTheme: () => void;
    updateAppearance: (patch: Partial<Appearance>) => void;
    openSavedTheme: (id: string) => void;
    saveNamedTheme: (name: string) => void;
    importThemeFile: (file: File) => Promise<void>;
    deleteSavedTheme: (id: string) => void;
    exportTheme: (theme: SavedTheme) => void;
}) {
    const {m} = useLocale();
    const [view, setView] = useState<ClientViewDTO>(emptyView);
    const [page, setPage] = useState<Page>("channels");
    const [contentTab, setContentTab] = useState<ContentTab>("chat");
    const [directChats, setDirectChats] = useState<DirectChat[]>([]);
    const [chatStore, setChatStore] = useState<ChatStore | null>(null);
    const chatStoreRef = useRef<ChatStore | null>(null);
    useEffect(() => {
        if (view.userId === "0" || view.connectionStatus !== "connected") return;
        if (chatStoreRef.current?.context === view.chatContext) return;
        chatStoreRef.current?.dispose();
        const store = new ChatStore(view.chatContext, view.userId);
        store.setMessageReceivedHandler(() => { void desktopAPI.messageReceived(); });
        chatStoreRef.current = store;
        setChatStore(store); setDirectChats([]); setContentTab("chat");
    }, [view.chatContext, view.userId, view.connectionStatus]);
    useEffect(() => () => chatStoreRef.current?.dispose(), []);
    useEffect(() => {
        chatStore?.setConnected(view.connectionStatus === "connected");
    }, [chatStore, view.connectionStatus]);
    useEffect(() => { chatStore?.changed(); }, [chatStore, view.chatRevision]);
    const [events, setEvents] = useState<ClientEventDTO[]>([]);
    const [actionError, setActionErrorState] = useState("");
    const [actionErrorTick, setActionErrorTick] = useState(0);
    const setActionError = useCallback((message: string) => {
        setActionErrorState(message);
        if (message) setActionErrorTick((tick) => tick + 1);
    }, []);
    const [refreshingServers, setRefreshingServers] = useState(false);
    const refreshingServersRef = useRef(false);
    const refreshServers = useCallback(async () => {
        if (refreshingServersRef.current) return;
        refreshingServersRef.current = true;
        setRefreshingServers(true);
        setActionError("");
        try { await desktopAPI.refreshServerStatuses(); }
        catch (error) { setActionError(rawError(error)); }
        finally { refreshingServersRef.current = false; setRefreshingServers(false); }
    }, []);
    const startupStatusesRequested = useRef(false);
    useEffect(() => {
        if (startupStatusesRequested.current) return;
        startupStatusesRequested.current = true;
        void refreshServers();
    }, [refreshServers]);
    const lastSequence = useRef("0");
    const screenMedia = useRef<ScreenMediaController | null>(null);
    if (!screenMedia.current) screenMedia.current = new ScreenMediaController();
    const sharing = useScreenSharing(view, screenMedia.current, setActionError);
    const sharingRef = useRef(sharing);
    sharingRef.current = sharing;
    useEffect(() => desktopAPI.onTrayScreenShare(() => { void sharingRef.current.toggle(); }), []);
    const updates = useUpdates(() => {
        if (sharing.pending) throw new Error(errorCode("screen-picker-update"));
        chatStore?.prepareUpdate();
    });

    const refresh = useCallback(async () => {
        try {
            const next = await desktopAPI.snapshot();
            next.channels ??= [];
            next.participants ??= [];
            next.screenStreams ??= [];
            setView(next);
        } catch (error) {
            setActionError(rawError(error));
        }
    }, []);

    const refreshEvents = useCallback(async () => {
        try {
            const next = (await desktopAPI.eventsAfter(lastSequence.current)) ?? [];
            if (!next.length) return;
            const newestSequence = next[next.length - 1].sequence;
            if (BigInt(newestSequence) > BigInt(lastSequence.current)) {
                lastSequence.current = newestSequence;
            }
            setEvents((current) => mergeEventTail(current, next));
        } catch (error) {
            setActionError(rawError(error));
        }
    }, []);

    const clearEvents = useCallback(() => {
        setEvents([]);
        setDirectChats([]);
        chatStoreRef.current?.dispose(); chatStoreRef.current = null; setChatStore(null);
        setContentTab("chat");
        lastSequence.current = "0";
    }, []);

    useEffect(() => {
        void refresh();
        void refreshEvents();
        const offState = desktopAPI.onStateChanged(() => void refresh());
        const offEvents = desktopAPI.onEventLogChanged(() => void refreshEvents());
        return () => {
            offState();
            offEvents();
        };
    }, [refresh, refreshEvents]);
    useEffect(() => {
        if (view.connectionStatus === "disconnected") void screenMedia.current?.close();
    }, [view.connectionStatus]);
    useEffect(() => {
        const dispose = () => screenMedia.current?.dispose();
        window.addEventListener("beforeunload", dispose);
        return () => {
            window.removeEventListener("beforeunload", dispose);
            dispose();
        };
    }, []);

    const invoke = useCallback(async (operation: () => Promise<unknown>) => {
        setActionError("");
        try {
            await operation();
            await refresh();
        } catch (error) {
            setActionError(rawError(error));
        }
    }, [refresh]);

    const wakeGate = useRef<WakeGate>(freshWakeGate());
    const [wakeUntil, setWakeUntil] = useState(0);
    const [wakeNow, setWakeNow] = useState(() => Date.now());
    const [wakeLabel, setWakeLabel] = useState("");
    const wakeSession = view.connectionStatus === "connected" ? view.sessionId : "";
    useEffect(() => {
        wakeGate.current = freshWakeGate();
        setWakeUntil(0);
        setWakeLabel("");
    }, [wakeSession]);
    useEffect(() => {
        if (!wakeUntil) return;
        const timer = window.setInterval(() => {
            const now = Date.now();
            if (now >= wakeGate.current.until) {
                wakeGate.current = freshWakeGate();
                setWakeUntil(0);
                return;
            }
            setWakeNow(now);
        }, 1000);
        return () => window.clearInterval(timer);
    }, [wakeUntil]);
    const showWakeLock = useCallback((until: number) => {
        const now = Date.now();
        setWakeNow(now);
        setWakeUntil(until);
        setWakeLabel(wakeClock(Math.max(1, (until - now) / 1000)));
    }, []);
    const wakeParticipant = useCallback(async (sessionId: string) => {
        const now = Date.now();
        const decision = wakeDecision(wakeGate.current, now);
        if (decision !== "send") {
            showWakeLock(wakeGate.current.until);
            return;
        }
        try {
            await desktopAPI.wake(sessionId);
            wakeSent(wakeGate.current, Date.now());
            void refresh();
        } catch (error) {
            const lockedFor = attentionLockSeconds(error);
            if (lockedFor != null) {
                wakeRejected(wakeGate.current, Date.now(), lockedFor);
                showWakeLock(wakeGate.current.until);
                return;
            }
            wakeFailed(wakeGate.current);
            setActionError(rawError(error));
        }
    }, [refresh, showWakeLock]);
    const lobby = view.connectionStatus === "disconnected";
    useEffect(() => {
        if (!actionError || lobby) return;
        const timer = window.setTimeout(() => setActionErrorState(""), 5000);
        return () => window.clearTimeout(timer);
    }, [actionError, actionErrorTick, lobby]);
    const mainRef = useRef<HTMLElement>(null);
    const overlayWasOpen = useRef<Page | null>(null);
    useEffect(() => {
        if (mainRef.current) mainRef.current.inert = page !== "channels";
        if (page !== "channels") overlayWasOpen.current = page;
        else if (overlayWasOpen.current) {
            document.getElementById(overlayWasOpen.current === "admin" ? "open-admin" : "open-settings")?.focus();
            overlayWasOpen.current = null;
        }
    }, [page]);
    const closeSettings = () => setPage("channels");
    const adminAvailable = view.canAdmin && view.connectionStatus === "connected";
    useEffect(() => {
        if (page === "admin" && !adminAvailable) setPage("channels");
    }, [page, adminAvailable]);

    return <div className={`app-shell${lobby ? " lobby-shell" : " server-shell"}`}>
        <WindowControls overlayOpen={page !== "channels"} onError={setActionError}/>
        <main className={`main-area${lobby ? " lobby-main" : " server-main"}`} ref={mainRef}>
            {lobby ? <>
                <LobbyBar version={updates.view?.current} updateAction={<UpdateButton updates={updates}/>} onPageChange={setPage}/>
                <ConnectionPage
                    view={view}
                    error={actionError}
                    onError={setActionError}
                    onRefresh={refresh}
                    refreshingServers={refreshingServers}
                    onRefreshServers={refreshServers}
                    onClearEvents={clearEvents}
                    onConnected={() => setPage("channels")}
                />
            </> : <>
            <StatusBar view={view} updateAction={<UpdateButton updates={updates}/>}/>
            {actionError && <div className="error-banner" role="alert">{errorText(actionError, m)}</div>}
            {wakeUntil > wakeNow && <WakeNotice until={wakeUntil} now={wakeNow} label={wakeLabel}/>}
            <ChannelsPage onPageChange={setPage} referenceBackdrop={appearance.theme !== "custom"} view={view} screenMedia={screenMedia.current} events={events} invoke={invoke} onWake={wakeParticipant} refreshingServers={refreshingServers} onRefreshServers={refreshServers}
                          stageBackdrop={appearance.theme === "custom" ? appearance.stageBackdrop : "mountains"}
                          onReconnect={(address) => invoke(async () => {
                              if (sharing.pending) throw new Error(errorCode("screen-picker-switch"));
                              await screenMedia.current?.close();
                              clearEvents();
                              await desktopAPI.reconnectServer(address);
                              rememberServerAddress(address);
                          })}
                          sharing={sharing} onError={setActionError} contentTab={contentTab} setContentTab={setContentTab} directChats={directChats} setDirectChats={setDirectChats} chatStore={chatStore}/>
            </>}
        </main>
        {page === "admin" && adminAvailable && <SettingsOverlay onClose={closeSettings} label={m.admin.title} closeLabel={m.admin.close}>
            <AdminPanel view={view}/>
        </SettingsOverlay>}
        {page === "settings" && <SettingsOverlay onClose={closeSettings}>
            <SettingsPage view={view} invoke={invoke} appearance={appearance} setTheme={setTheme} createCustomTheme={createCustomTheme} updateAppearance={updateAppearance} openSavedTheme={openSavedTheme} saveNamedTheme={saveNamedTheme} importThemeFile={importThemeFile} deleteSavedTheme={deleteSavedTheme} exportTheme={exportTheme} updates={updates}/>
        </SettingsOverlay>}
        <ScreenShareDialog sharing={sharing}/>
        <UpdateDialog updates={updates}/>
        <LinkConfirmDialog/>
        <MediaTrustDialog/>
    </div>;
}

export default App;
