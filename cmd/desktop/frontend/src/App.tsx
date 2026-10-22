import {useCallback, useEffect, useMemo, useRef, useState} from "react";
import type {Dispatch, ReactNode, SetStateAction} from "react";
import {useReducer} from "react";
import type {AudioDeviceDTO, AudioDevicesDTO, ChannelDTO, ClientEventDTO, ClientViewDTO, ParticipantDTO} from "./api";
import {desktopAPI} from "./api";
import {buildChannelGroups, canMoveParticipant, mergeEventTail} from "./model";
import {ScreenMediaController} from "./features/screen/screenMedia";
import {ScreenSharing, ScreenStageTile, ScreenViewerWindow} from "./features/screen/ScreenViews";
import {LinkConfirmDialog} from "./features/chat/LinkConfirm";
import {MediaTrustDialog} from "./features/screen/MediaTrustConfirm";
import {ScreenShareDialog, useScreenSharing, type ScreenSharingState} from "./features/screen/ScreenShareDialog";
import {AudioControls, ConnectionPage, LobbyBar, StatusBar, type Page} from "./features/connection/ConnectionViews";
import {ParticipantRow} from "./features/participants/ParticipantRow";
import {RecentServers} from "./features/connection/RecentServers";
import {rememberServerAddress} from "./features/connection/ConnectionViews";
import {Icon} from "./components/Icon";
import {Select, type SelectOption} from "./components/Select";
import {ChatPanel} from "./features/chat/ChatPanel";
import {ChatStore} from "./features/chat/chatStore";
import {useWorkspaceResize} from "./components/useWorkspaceResize";
import {UpdateButton, UpdateDialog, UpdatesPanel, useUpdates, type UpdatesState} from "./features/updates/Updates";
import {applyAppearance, createCustomAppearance, downloadThemeFile, loadAppearance, maxSavedThemes, normalizeTheme, readThemeFile, saveAppearance, snapshotTheme, themeColor, type Appearance, type SavedTheme, type StageBackdrop} from "./appearance";
import {attentionLockSeconds, errorCode, errorText, rawError, type Messages} from "./locales";
import {freshWakeGate, wakeClock, wakeDecision, wakeFailed, wakeRejected, wakeSent, type WakeGate} from "./features/participants/wakeLimit";
import {LocaleSwitch, useLocale} from "./locales/Locale";
import {InterfaceSettings} from "./features/settings/InterfaceSettings";
import {AdminPanel} from "./features/admin/AdminPanel";
import {StageScene} from "./features/settings/StageScene";
import {HotkeySettings} from "./features/settings/HotkeySettings";

const contentTabs = ["chat", "screens", "events"] as const;
type ContentTab = typeof contentTabs[number] | `direct:${string}`;
type DirectChat = {userId: string; displayName: string};

const participantDragType = "application/x-sonoryx-participant";

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
    if (streamID) return <ScreenViewerWindow streamID={streamID} ownerName={params.get("owner") || m.screen.participant}/>;
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
    useEffect(() => { chatStore?.invalidate(); }, [chatStore, view.chatRevision]);
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

    return <div className="app-shell">
        <main className="main-area" ref={mainRef}>
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
            <StatusBar view={view} onPageChange={setPage} sharing={sharing} screenMedia={screenMedia.current} updateAction={<UpdateButton updates={updates}/>} invoke={invoke}/>
            {actionError && <div className="error-banner" role="alert">{errorText(actionError, m)}</div>}
            {wakeUntil > wakeNow && <WakeNotice until={wakeUntil} now={wakeNow} label={wakeLabel}/>}
            <ChannelsPage view={view} screenMedia={screenMedia.current} events={events} invoke={invoke} onWake={wakeParticipant} refreshingServers={refreshingServers} onRefreshServers={refreshServers}
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

function WakeNotice({until, now, label}: {until: number; now: number; label: string}) {
    const {m} = useLocale();
    const left = wakeClock(Math.max(1, (until - now) / 1000));
    return <div className="wake-notice">
        <span className="wake-notice-icon" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round"><circle cx="12" cy="12" r="8"/><path d="M12 8v4.5l3 2"/></svg></span>
        <div>
            <strong>{m.people.wakeBlockedTitle}</strong>
            <p>{m.people.wakeBlockedBody}</p>
            <p className="wake-notice-time" aria-hidden="true">{m.people.wakeBlockedIn(left)}</p>
            <span className="wake-notice-live" role="status">{m.people.wakeBlockedTitle}. {m.people.wakeBlockedBody} {m.people.wakeBlockedIn(label || left)}</span>
        </div>
    </div>;
}

function ChannelsPage({view, events, invoke, sharing, screenMedia, onError, onReconnect, onWake, contentTab, setContentTab, directChats, setDirectChats, chatStore, refreshingServers, onRefreshServers, stageBackdrop}: {
    screenMedia: ScreenMediaController;
    refreshingServers: boolean;
    onRefreshServers: () => void;
    onWake: (sessionId: string) => void;
    onReconnect: (address: string) => Promise<void>;
    view: ClientViewDTO;
    events: ClientEventDTO[];
    invoke: (operation: () => Promise<unknown>) => Promise<void>
    sharing: ScreenSharingState;
    onError: (message: string) => void;
    contentTab: ContentTab;
    setContentTab: Dispatch<SetStateAction<ContentTab>>;
    directChats: DirectChat[];
    setDirectChats: Dispatch<SetStateAction<DirectChat[]>>;
    chatStore: ChatStore | null;
    stageBackdrop: StageBackdrop;
}) {
    const {m} = useLocale();
    const channels = view.channels ?? [];
    const participants = view.participants ?? [];
    const localChatName = participants.find((p) => p.sessionId === view.sessionId)?.displayName ?? participants.find((p) => p.userId === chatStore?.userId)?.displayName ?? m.channels.you;
    const lastChatChannel = useRef(view.channelId);
    if (view.channelId !== "0") lastChatChannel.current = view.channelId;
    const chatChannelID = view.channelId !== "0" ? view.channelId : lastChatChannel.current;
    const [, refreshChats] = useReducer((n: number) => n + 1, 0);
    useEffect(() => chatStore?.subscribe(refreshChats), [chatStore]);
    const dialogs = chatStore?.dialogs ?? [];
    useEffect(() => {
        setDirectChats((current) => {
            const next = [...current];
            for (const d of dialogs) {
                if (chatStore?.shouldOpenDialog(d) && !next.some((chat) => chat.userId === d.userId) && next.length < 16) next.push({userId: d.userId, displayName: d.displayName});
            }
            return next.length === current.length ? current : next;
        });
    }, [dialogs, setDirectChats, chatStore]);
    useEffect(() => {
        if (!chatStore || view.connectionStatus !== "connected" || view.channelId === "0") return;
        return chatStore.watch({kind: "channel", id: view.channelId});
    }, [chatStore, view.connectionStatus, view.channelId]);
    const screenOwners = useMemo(() => new Set((view.screenStreams ?? []).map((stream) => stream.ownerSessionId)), [view.screenStreams]);
    const selectedID = view.channelId;
    const selected = channels.find((channel) => channel.id === selectedID);
    const [showInfo, setShowInfo] = useState(false);
    const [serversExpanded, setServersExpanded] = useState(false);
    const channelStreams = (view.screenStreams ?? []).filter((stream) => stream.channelId === view.channelId);
    const hasScreens = channelStreams.length > 0;
    const allTabs: ContentTab[] = [...contentTabs.filter((tab) => tab !== "screens" || hasScreens), ...directChats.map((chat): ContentTab => `direct:${chat.userId}`)];
    useEffect(() => {
        if (!hasScreens && contentTab === "screens") {
            const focused = document.activeElement?.id === "screens-tab";
            setContentTab("chat");
            if (focused) requestAnimationFrame(() => document.getElementById("chat-tab")?.focus());
        }
    }, [hasScreens, contentTab, setContentTab]);
    const openDirect = (userId: string, displayName: string) => {
        if (!chatStore || userId === "0" || userId === chatStore.userId) return;
        if (directChats.length >= 16 && !directChats.some((chat) => chat.userId === userId)) { onError(errorCode("direct-chat-limit")); return; }
        setDirectChats((current) => current.some((chat) => chat.userId === userId) ? current :
            [...current, {userId, displayName}]);
        const tab: ContentTab = `direct:${userId}`;
        setContentTab(tab);
        requestAnimationFrame(() => {
            const element = document.getElementById(`${tab}-tab`);
            element?.focus();
            element?.scrollIntoView({block: "nearest", inline: "nearest"});
        });
    };
    const openMessage = (participant: ParticipantDTO) => openDirect(participant.userId ?? "0", participant.displayName);
    const closeMessage = (userId: string) => {
        chatStore?.dismissDialog(userId);
        const tab: ContentTab = `direct:${userId}`;
        const next = contentTab === tab ? allTabs[Math.max(0, allTabs.indexOf(tab) - 1)] : contentTab;
        setDirectChats((current) => current.filter((chat) => chat.userId !== userId));
        setContentTab(next);
        requestAnimationFrame(() => document.getElementById(`${next}-tab`)?.focus());
    };
    const infoVisible = showInfo && Boolean(selected);
    const workspaceResize = useWorkspaceResize(infoVisible);
    const members = participants.filter((item) => item.channelId === selectedID);
    return <section className="channels-page">
        <div ref={workspaceResize.layoutRef} style={workspaceResize.style} className={`channels-layout ${infoVisible ? "" : "info-hidden"} ${workspaceResize.resizing ? "is-resizing" : ""}`}>
            <section id="channel-browser-pane" className={`panel channel-browser ${serversExpanded ? "servers-expanded" : ""}`}>
                <section className="sidebar-section" aria-label={m.channels.browser}>
                <div className="panel-heading">
                    <div><h2>{m.channels.title}</h2></div>
                    <span className="count-badge">{participants.length}</span></div>
                <div className="channel-scroll"><ChannelTree channels={channels} participants={participants} screenOwners={screenOwners}
                                                             canKick={view.canKick} canBan={view.canBan}
                                                             canDrag={view.canDrag && view.connectionStatus === "connected"}
                                                             wakeEnabled={view.connectionStatus === "connected"}
                                                             currentID={view.channelId}
                                                             onError={onError} onMessage={openMessage}
                                                             onWake={(participant) => { onWake(participant.sessionId); }}
                                                             onJoin={(id) => {
                    if (id !== view.channelId && view.connectionStatus === "connected") void invoke(() => desktopAPI.joinChannel(id));
                }} onMoveParticipant={(sessionID, channelID) => {
                    void invoke(() => desktopAPI.drag(sessionID, channelID));
                }}/></div>
                </section>
                <RecentServers view={view} expanded={serversExpanded} onToggle={() => setServersExpanded((value) => !value)} onReconnect={onReconnect} onError={onError} refreshingServers={refreshingServers} onRefreshServers={onRefreshServers}/>
            </section>
            <div className="workspace-divider" {...workspaceResize.separatorProps("browser")}/>
            <section className="channel-stage">{selected ? <>
                <header className="channel-stage-heading"><span className="channel-heading-icon"><Icon name="sound"/></span><div><h2 title={selected.name}>{selected.name}</h2><p title={selected.topic || m.channels.voice}>{selected.topic || m.channels.voice}</p></div>
                    <button className="info-toggle" aria-label={m.channels.info} aria-expanded={showInfo} onClick={() => setShowInfo(!showInfo)}><Icon name="info"/></button>
                </header>
                <div className="participants-stage" data-backdrop={stageBackdrop} role="region" tabIndex={0} aria-label={m.channels.stage}><StageScene backdrop={stageBackdrop}/><div className="participants-strip">
                    {members.length ? members.map((participant) => <div className="stage-participant-group" key={participant.sessionId}><ParticipantRow
                        participant={participant} variant="stage" sharingScreen={screenOwners.has(participant.sessionId)} canKick={view.canKick} canBan={view.canBan} canDrag={false}
                        canWake={view.connectionStatus === "connected" && participant.channelId === view.channelId}
                        onWake={(person) => { onWake(person.sessionId); }}
                        depth={0} onError={onError} onMessage={openMessage} onDragStart={(event) => event.preventDefault()} onDragEnd={() => {}}/>
                        {channelStreams.filter((stream) => stream.ownerSessionId === participant.sessionId).map((stream) => <ScreenStageTile key={stream.id} stream={stream} ownerName={participant.displayName} local={participant.local} connected={view.connectionStatus === "connected"} onError={onError}/>)}</div>) : <div className="stage-empty">{m.channels.emptyStage}</div>}
                </div></div>
                <section className="channel-content">
                    <div className="channel-tabs" role="tablist" aria-label={m.channels.content} onKeyDown={(event) => {
                        if ((event.target as HTMLElement).getAttribute("role") !== "tab") return;
                        if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
                        event.preventDefault();
                        const index = allTabs.indexOf(contentTab);
                        const next = event.key === "Home" ? allTabs[0] : event.key === "End" ? allTabs[allTabs.length - 1] : allTabs[(index + (event.key === "ArrowRight" ? 1 : -1) + allTabs.length) % allTabs.length];
                        setContentTab(next);
                        document.getElementById(`${next}-tab`)?.focus();
                    }}>
                        <button id="chat-tab" role="tab" tabIndex={contentTab === "chat" ? 0 : -1} aria-selected={contentTab === "chat"} aria-controls="chat-panel" onClick={() => setContentTab("chat")}><Icon name="chat"/>{m.channels.chat}{chatStore && chatChannelID !== "0" && chatStore.conversation({kind: "channel", id: chatChannelID}).unread > 0 && <span className="count-badge">{chatStore.conversation({kind: "channel", id: chatChannelID}).unread}</span>}</button>
                        {hasScreens && <button id="screens-tab" role="tab" tabIndex={contentTab === "screens" ? 0 : -1} aria-selected={contentTab === "screens"} aria-controls="screens-panel" onClick={() => setContentTab("screens")}><Icon name="screen"/>{m.channels.screens}<span className="count-badge">{channelStreams.length}</span></button>}
                        <button id="events-tab" role="tab" tabIndex={contentTab === "events" ? 0 : -1} aria-selected={contentTab === "events"} aria-controls="events-panel" onClick={() => setContentTab("events")}>{m.channels.events}</button>
                        {directChats.map((chat) => {
                            const tab: ContentTab = `direct:${chat.userId}`;
                            const dialog = dialogs.find((d) => d.userId === chat.userId);
                            const name = participants.find((participant) => participant.userId === chat.userId)?.displayName ?? dialog?.displayName ?? chat.displayName;
                            return <div className="direct-tab" role="presentation" key={chat.userId} data-active={contentTab === tab}>
                                <button id={`${tab}-tab`} role="tab" tabIndex={contentTab === tab ? 0 : -1} aria-selected={contentTab === tab} aria-controls={`${tab}-panel`} title={name} onClick={() => setContentTab(tab)}><span>{name}</span>{!!dialog?.unread && <span className="count-badge">{dialog.unread}</span>}</button>
                                <button className="close-chat-tab" type="button" aria-label={m.channels.closeDirect(name)} title={m.channels.closeTab} onClick={() => closeMessage(chat.userId)}>×</button>
                            </div>;
                        })}
                    </div>
                    <div id="chat-panel" role="tabpanel" aria-labelledby="chat-tab" hidden={contentTab !== "chat"}>{chatStore && chatChannelID !== "0" ? <ChatPanel key={chatChannelID} store={chatStore} localName={localChatName} target={{kind: "channel", id: chatChannelID}} title={m.channels.channelChat(channels.find((c) => c.id === chatChannelID)?.name ?? "")} active={contentTab === "chat"} connected={view.connectionStatus === "connected" && view.channelId === chatChannelID}/> : <div className="chat-placeholder">{m.channels.waitingChannel}</div>}</div>
                    {hasScreens && <div id="screens-panel" role="tabpanel" aria-labelledby="screens-tab" hidden={contentTab !== "screens"}><ScreenSharing view={view} channelID={selected.id} participants={participants} screenMedia={screenMedia}/></div>}
                    <div id="events-panel" role="tabpanel" aria-labelledby="events-tab" hidden={contentTab !== "events"}><EventPanel events={events} active={contentTab === "events"}/></div>
                    {directChats.map((chat) => <div key={chat.userId} id={`direct:${chat.userId}-panel`} role="tabpanel" aria-labelledby={`direct:${chat.userId}-tab`} hidden={contentTab !== `direct:${chat.userId}`}>
                        {chatStore && <ChatPanel store={chatStore} localName={localChatName} target={{kind: "direct", id: chat.userId}} title={m.channels.direct(participants.find((participant) => participant.userId === chat.userId)?.displayName ?? dialogs.find((d) => d.userId === chat.userId)?.displayName ?? chat.displayName)} online={participants.some((p) => p.userId === chat.userId)} active={contentTab === `direct:${chat.userId}`} connected={view.connectionStatus === "connected"}/>}
                    </div>)}
                </section>
            </> : <div className="empty-state">{m.channels.waitingChannel}</div>}
                <div className="channel-audio-dock"><AudioControls view={view} invoke={invoke} sharing={sharing}/></div>
            </section>
            {infoVisible && <div className="workspace-divider info-divider" {...workspaceResize.separatorProps("info")}/>}
            {showInfo && selected && <section id="channel-info-pane" className="panel channel-detail">{selected ? <><div className="channel-info-heading"><span>{m.channels.info}</span><button aria-label={m.channels.closeInfo} onClick={() => setShowInfo(false)}>×</button></div>
                <h2>{selected.name}</h2>
                <p className="channel-topic">{selected.topic || m.channels.voice}</p><p
                    className="channel-description">{selected.description || m.channels.noDescription}</p>
                <div className="detail-grid">
                    <div>
                        <span>{m.channels.members}</span><strong>{participants.filter((item) => item.channelId === selected.id).length}{selected.maxUsers ? ` / ${selected.maxUsers}` : ""}</strong>
                    </div>
                    <div><span>{m.channels.codec}</span><strong>{selected.audio.codec.toUpperCase()}</strong></div>
                    <div><span>{m.channels.rate}</span><strong>{m.channels.khz(selected.audio.sampleRate / 1000)}</strong></div>
                    <div><span>{m.channels.bitrate}</span><strong>{m.channels.kbps(selected.audio.bitrate / 1000)}</strong></div>
                </div>
            </> : <div className="empty-state">{m.channels.pickChannel}</div>}</section>}
        </div>
    </section>;
}

function ChannelTree({channels, participants, screenOwners, canKick, canBan, canDrag, wakeEnabled, currentID, onJoin, onMoveParticipant, onError, onMessage, onWake}: {
    channels: ChannelDTO[];
    participants: ParticipantDTO[];
    screenOwners: Set<string>;
    canKick: boolean;
    canBan: boolean;
    canDrag: boolean;
    wakeEnabled: boolean;
    currentID: string;
    onJoin: (id: string) => void
    onMoveParticipant: (sessionID: string, channelID: string) => void;
    onError: (message: string) => void;
    onMessage: (participant: ParticipantDTO) => void;
    onWake: (participant: ParticipantDTO) => void;
}) {
    const {m} = useLocale();
    const [draggingSessionID, setDraggingSessionID] = useState<string | null>(null);
    const [dropTargetID, setDropTargetID] = useState<string | null>(null);
    const children = useMemo(() => {
        return buildChannelGroups(channels);
    }, [channels]);
    const draggingParticipant = participants.find((participant) => participant.sessionId === draggingSessionID);
    const renderLevel = (parentID: string, depth: number): React.ReactNode => (children.get(parentID) ?? []).map((channel) => {
        const members = participants.filter((participant) => participant.channelId === channel.id);
        const canDrop = canMoveParticipant(draggingParticipant, channel.id, canDrag);
        return <div key={channel.id}>
            <button
                className={`channel-row ${currentID === channel.id ? "selected current" : ""} ${canDrop && dropTargetID === channel.id ? "drop-target" : ""}`}
                style={{paddingLeft: 14 + depth * 18}}
                title={canDrop ? m.channels.dropHere : !channel.canJoin ? m.channels.noAccess : undefined}
                onDragOver={(event) => {
                    if (!canDrop) return;
                    event.preventDefault();
                    event.dataTransfer.dropEffect = "move";
                    if (dropTargetID !== channel.id) setDropTargetID(channel.id);
                }}
                onDragLeave={(event) => {
                    if (!(event.relatedTarget instanceof Node) || !event.currentTarget.contains(event.relatedTarget)) {
                        setDropTargetID((current) => current === channel.id ? null : current);
                    }
                }}
                onDrop={(event) => {
                    if (!canDrop || !draggingSessionID || event.dataTransfer.getData(participantDragType) !== draggingSessionID) return;
                    event.preventDefault();
                    setDropTargetID(null);
                    setDraggingSessionID(null);
                    onMoveParticipant(draggingSessionID, channel.id);
                }}
                onDoubleClick={() => { if (channel.canJoin) onJoin(channel.id); }}><span className="channel-icon" aria-hidden="true"><Icon name="sound"/></span><span
                className="channel-name">{channel.name}</span><span className="channel-count">{members.length}{channel.maxUsers ? `/${channel.maxUsers}` : ""}</span>
            </button>
            {members.map((participant) => <ParticipantRow key={participant.sessionId} participant={participant}
                                                          sharingScreen={screenOwners.has(participant.sessionId)}
                                                          canKick={canKick} canBan={canBan} canDrag={canDrag}
                                                          canWake={wakeEnabled && participant.channelId === currentID}
                                                          onWake={onWake}
                                                          depth={depth} onError={onError} onMessage={onMessage}
                                                          onDragStart={(event, source) => {
                                                              if (!canDrag) {
                                                                  event.preventDefault();
                                                                  return;
                                                              }
                                                              event.dataTransfer.setData(participantDragType, source.sessionId);
                                                              event.dataTransfer.effectAllowed = "move";
                                                              setDraggingSessionID(source.sessionId);
                                                          }}
                                                          onDragEnd={() => { setDraggingSessionID(null); setDropTargetID(null); }}/>)}{renderLevel(channel.id, depth + 1)}</div>;
    });
    return <div className="channel-tree">{channels.length ? renderLevel("0", 0) :
        <div className="empty-state">{m.channels.noChannels}</div>}</div>;
}

function EventPanel({events, active}: {events: ClientEventDTO[]; active: boolean}) {
    const {m, htmlLang} = useLocale();
    const listRef = useRef<HTMLDivElement>(null);
    const stickToBottom = useRef(true);
    useEffect(() => {
        if (active && stickToBottom.current && listRef.current) listRef.current.scrollTop = listRef.current.scrollHeight;
    }, [events, active]);
    return <section className="panel event-panel" aria-label={m.channels.eventLog}>
        <div className="event-list" ref={listRef} onScroll={(event) => {
            const element = event.currentTarget;
            stickToBottom.current = element.scrollHeight - element.scrollTop - element.clientHeight < 12;
        }}>{events.length === 0 ? <div className="empty-event">{m.channels.eventsEmpty}</div> :
            events.map((item) => <div className={`event-row kind-${item.kind}`} key={item.sequence}>
                <time>{new Date(item.time).toLocaleTimeString(htmlLang, {
                    hour: "2-digit", minute: "2-digit", second: "2-digit"
                })}</time>
                <span className="event-marker"/><span>{item.message}</span>
            </div>)}</div>
    </section>;
}

function vadModeOptions(m: Messages): SelectOption[] {
    return [
        {value: "level", label: m.settings.vadLevel},
        {value: "vad", label: m.settings.vadSpeech},
        {value: "hybrid", label: m.settings.vadHybrid},
    ];
}

function deviceOptions(devices: AudioDeviceDTO[], m: Messages): SelectOption[] {
    return [
        {value: "", label: m.settings.systemDevice},
        ...devices.map((device) => ({
            value: device.id,
            label: device.name,
            detail: device.isDefault ? m.settings.systemDefault : undefined,
            selectedLabel: device.isDefault ? m.settings.systemNamed(device.name) : device.name,
        })),
    ];
}

function SettingsOverlay({onClose, children, label, closeLabel}: {onClose: () => void; children: ReactNode; label?: string; closeLabel?: string}) {
    const {m} = useLocale();
    label ??= m.settings.title;
    closeLabel ??= m.settings.close;
    useEffect(() => {
        const onKey = (event: KeyboardEvent) => {
            if (event.key !== "Escape" || event.defaultPrevented) return;
            const target = event.target;
            if (target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement || target instanceof HTMLSelectElement) return;
            onClose();
        };
        window.addEventListener("keydown", onKey);
        return () => window.removeEventListener("keydown", onKey);
    }, [onClose]);
    return <div className="settings-overlay" onMouseDown={(event) => {
        if (event.button === 0 && event.target === event.currentTarget) onClose();
    }}>
        <div className="settings-window" role="dialog" aria-modal="true" aria-label={label}>
            <button type="button" className="settings-close" title={closeLabel} aria-label={closeLabel} autoFocus onClick={onClose}><Icon name="close"/></button>
            {children}
        </div>
    </div>;
}

function SettingsPage({view, invoke, appearance, setTheme, createCustomTheme, updateAppearance, openSavedTheme, saveNamedTheme, importThemeFile, deleteSavedTheme, exportTheme, updates}: {
    view: ClientViewDTO;
    invoke: (operation: () => Promise<unknown>) => Promise<void>;
    appearance: Appearance;
    setTheme: (value: string) => Promise<void>;
    createCustomTheme: () => void;
    updateAppearance: (patch: Partial<Appearance>) => void;
    openSavedTheme: (id: string) => void;
    saveNamedTheme: (name: string) => void;
    importThemeFile: (file: File) => Promise<void>;
    deleteSavedTheme: (id: string) => void;
    exportTheme: (theme: SavedTheme) => void;
    updates: UpdatesState;
}) {
    const {m} = useLocale();
    const [sensitivity, setSensitivity] = useState(view.audio.vadSensitivity);
    useEffect(() => setSensitivity(view.audio.vadSensitivity), [view.audio.vadSensitivity]);
    const [rnnoiseSensitivity, setRNNoiseSensitivity] = useState(view.audio.rnnoiseSensitivity);
    useEffect(() => setRNNoiseSensitivity(view.audio.rnnoiseSensitivity), [view.audio.rnnoiseSensitivity]);
    const [microphoneGain, setMicrophoneGain] = useState(view.audio.microphoneGain);
    useEffect(() => setMicrophoneGain(view.audio.microphoneGain), [view.audio.microphoneGain]);
    const [devices, setDevices] = useState<AudioDevicesDTO | null>(null);
    const [devicesError, setDevicesError] = useState("");
    const [devicePending, setDevicePending] = useState(false);
    const [section, setSection] = useState<"microphone" | "playback" | "interface" | "behavior" | "updates">("microphone");
    const [soundVolume, setSoundVolume] = useState(view.soundVolume);
    useEffect(() => setSoundVolume(view.soundVolume), [view.soundVolume]);
    const [closeToTray, setCloseToTrayState] = useState(false);
    useEffect(() => {
        let active = true;
        void desktopAPI.closeToTray().then((value) => {
            if (active) setCloseToTrayState(value);
        }).catch(() => undefined);
        return () => {
            active = false;
        };
    }, []);
    const loadDevices = useCallback(async () => {
        try {
            const next = await desktopAPI.audioDevices();
            next.capture ??= [];
            next.playback ??= [];
            setDevices(next);
            setDevicesError("");
        } catch (error) {
            setDevicesError(rawError(error));
        }
    }, []);
    useEffect(() => {
        void loadDevices();
    }, [loadDevices]);
    // Before joining a server nothing captures the microphone; the backend
    // runs a preview pipeline for the meter while the microphone section is open.
    useEffect(() => {
        if (section !== "microphone") return;
        void desktopAPI.setMicrophonePreview(true).catch(() => undefined);
        return () => { void desktopAPI.setMicrophonePreview(false).catch(() => undefined); };
    }, [section]);
    // Disabling the preview also turns the monitor off on the backend, and so
    // does starting a connection; in a session the monitor blocks sending.
    const [monitor, setMonitor] = useState(false);
    const inSession = view.connectionStatus !== "disconnected";
    const wasInSession = useRef(inSession);
    useEffect(() => {
        if (inSession && !wasInSession.current) setMonitor(false);
        wasInSession.current = inSession;
    }, [inSession]);
    useEffect(() => { if (section !== "microphone") setMonitor(false); }, [section]);
    const toggleMonitor = () => {
        const next = !monitor;
        setMonitor(next);
        void desktopAPI.setMicrophoneMonitor(next).catch(() => setMonitor(!next));
    };
    const selectDevice = async (kind: "capture" | "playback", id: string) => {
        if (devicePending) return;
        setDevicePending(true);
        setDevicesError("");
        try {
            if (kind === "capture") await desktopAPI.setCaptureDevice(id);
            else await desktopAPI.setPlaybackDevice(id);
            await loadDevices();
        } catch (error) {
            setDevicesError(rawError(error));
        } finally {
            setDevicePending(false);
        }
    };
    return <section className="settings-page">
        <div className="settings-heading"><p className="eyebrow">{m.settings.eyebrow}</p><h2>{m.settings.title}</h2></div>
        <div className="settings-layout">
            <nav className="settings-nav" aria-label={m.settings.sections}>
                <button type="button" aria-current={section === "microphone" ? "page" : undefined} onClick={() => setSection("microphone")}>{m.settings.microphone}</button>
                <button type="button" aria-current={section === "playback" ? "page" : undefined} onClick={() => setSection("playback")}>{m.settings.playback}</button>
                <button type="button" aria-current={section === "interface" ? "page" : undefined} onClick={() => setSection("interface")}>{m.settings.interface}</button>
                <button type="button" aria-current={section === "behavior" ? "page" : undefined} onClick={() => setSection("behavior")}>{m.settings.behavior}</button>
                <button type="button" aria-current={section === "updates" ? "page" : undefined} onClick={() => setSection("updates")}>{m.settings.updates}</button>
            </nav>
            <div className="settings-content">
                {(section === "microphone" || section === "playback") && devicesError && <div className="device-error" role="alert">{m.settings.devicesFailed}: {errorText(devicesError, m)}
                    <button className="text-button" onClick={() => void loadDevices()}>{m.settings.retry}</button>
                </div>}
                {section === "microphone" &&
                    <section className="settings-card"><h3>{m.settings.microphone}</h3><Select id="capture-device"
                                                                                 label={m.settings.capture}
                                                                                 value={devices?.selectedCapture ?? ""}
                                                                                 disabled={!devices || devicePending}
                                                                                 options={deviceOptions(devices?.capture ?? [], m)}
                                                                                 onChange={(id) => void selectDevice("capture", id)}/>
                        {!view.audio.captureAvailable && <p className="device-hint">{m.settings.noMic}</p>}
                        <SensitivitySlider id="microphone-gain" max={2} label={m.settings.gain}
                                           description={m.settings.gainHint}
                                           value={microphoneGain} disabled={false}
                                           onChange={setMicrophoneGain}
                                           onCommit={(value) => invoke(() => desktopAPI.setMicrophoneGain(value))}/>
                        <SettingToggle title={m.settings.denoise}
                                       description={m.settings.denoiseHint}
                                       checked={view.audio.rnnoiseEnabled}
                                       onChange={(value) => invoke(() => desktopAPI.setRNNoiseEnabled(value))}/>
                        <SensitivitySlider label={m.settings.denoiseAmount}
                                           description={m.settings.denoiseAmountHint}
                                           value={rnnoiseSensitivity} disabled={!view.audio.rnnoiseEnabled}
                                           onChange={setRNNoiseSensitivity}
                                           onCommit={(value) => invoke(() => desktopAPI.setRNNoiseSensitivity(value))}/>
                        <SettingToggle title={m.settings.vad}
                                       description={m.settings.vadHint}
                                       checked={view.audio.vadEnabled}
                                       onChange={(value) => invoke(() => desktopAPI.setVADEnabled(value))}/>
                        <Select id="vad-mode" className="mode-select" label={m.settings.mode} value={view.audio.vadMode}
                                disabled={!view.audio.vadEnabled}
                                options={vadModeOptions(m)}
                                labelExtra={<span className={`gate-indicator ${view.audio.vadOpen ? "open" : ""}`}>{view.audio.vadOpen ? m.settings.transmitting : m.settings.waitingSpeech}</span>}
                                onChange={(value) => void invoke(() => desktopAPI.setVADMode(value))}/>
                        <div className="sensitivity-setting">
                            <div><span className="setting-label">{m.settings.gate}</span>
                                <output>{Math.round((1 - sensitivity) * 100)}%</output>
                            </div>
                            <p>{m.settings.gateHint}</p></div>
                        <AudioWaveform sensitivity={sensitivity} disabled={!view.audio.vadEnabled}
                                       onSensitivityChange={setSensitivity}
                                       onSensitivityCommit={(value) => invoke(() => desktopAPI.setVADSensitivity(value))}/>
                        <div className="monitor-setting">
                            <button type="button" className={`monitor-button ${monitor ? "active" : ""}`} aria-pressed={monitor}
                                    onClick={toggleMonitor}>
                                <Icon name="sound"/>{monitor ? m.settings.stopMonitor : m.settings.monitor}
                            </button>
                            <p className="device-hint">{m.settings.monitorOn}</p>
                            {inSession && <p className={`device-hint ${monitor ? "monitor-warning" : ""}`}>{monitor
                                ? m.settings.monitorActive
                                : m.settings.monitorBlocked}</p>}
                        </div>
                    </section>}
                {section === "playback" &&
                    <section className="settings-card compact-card"><h3>{m.settings.playback}</h3><Select id="playback-device"
                                                                                                         label={m.settings.output}
                                                                                                         value={devices?.selectedPlayback ?? ""}
                                                                                                         disabled={!devices || devicePending}
                                                                                                         options={deviceOptions(devices?.playback ?? [], m)}
                                                                                                         onChange={(id) => void selectDevice("playback", id)}/><SettingToggle
                        title={m.settings.deafen} description={m.settings.deafenHint}
                        checked={view.audio.deafened} onChange={(value) => invoke(() => desktopAPI.setDeafened(value))}/></section>}
                {section === "interface" && <><section className="settings-card"><h3>{m.settings.interface}</h3>
                    <div className="locale-setting">
                        <span className="setting-label" id="locale-label">{m.language.label}</span>
                        <p>{m.language.hint}</p>
                        <LocaleSwitch labelledBy="locale-label"/>
                    </div>
                    <InterfaceSettings appearance={appearance} onTheme={(value) => invoke(() => setTheme(value))} onCreate={createCustomTheme} onChange={updateAppearance} onOpenSaved={openSavedTheme} onSave={saveNamedTheme} onImport={importThemeFile} onExport={exportTheme} onDelete={deleteSavedTheme}/>
                </section>
                <section className="settings-card"><h3>{m.settings.sounds}</h3>
                    <Select id="sound-pack" label={m.settings.soundPack} hint={m.settings.soundPackHint}
                            value={view.soundPack || "soft"}
                            options={[
                                {value: "short", label: m.settings.packShort},
                                {value: "soft", label: m.settings.packSoft},
                                {value: "low", label: m.settings.packLow},
                                {value: "double", label: m.settings.packDouble},
                                {value: "signal", label: m.settings.packSignal},
                            ]}
                            onChange={(id) => invoke(() => desktopAPI.setSoundPack(id))}/>
                    <SensitivitySlider id="sound-volume" max={1} label={m.settings.soundVolume}
                                       description={m.settings.soundVolumeHint}
                                       value={soundVolume} disabled={false}
                                       onChange={setSoundVolume}
                                       onCommit={(value) => invoke(() => desktopAPI.setSoundVolume(value))}/>
                    <SoundPackPreview/>
                    <div className="sound-cues">
                        <h4>{m.settings.soundCues}</h4>
                        <p className="device-hint">{m.settings.soundCuesHint}</p>
                        {(Object.keys(m.settings.cues) as (keyof typeof m.settings.cues)[]).map((id) => <SettingToggle
                            key={id} title={m.settings.cues[id]} description=""
                            checked={view.soundCues?.[id] !== false}
                            onChange={(value) => invoke(() => desktopAPI.setSoundCue(id, value))}/>)}
                    </div>
                </section></>}
                {section === "behavior" && <><section className="settings-card"><h3>{m.settings.window}</h3>
                    <SettingToggle title={m.settings.tray}
                                   description={m.settings.trayHint}
                                   checked={closeToTray}
                                   onChange={(value) => invoke(async () => {
                                       const previous = closeToTray;
                                       setCloseToTrayState(value);
                                       try {
                                           await desktopAPI.setCloseToTray(value);
                                       } catch (error) {
                                           setCloseToTrayState(previous);
                                           throw error;
                                       }
                                   })}/>
                </section>
                <section className="settings-card"><h3>{m.settings.awayAction}</h3>
                    <SettingToggle title={m.settings.muteOnAway} description={m.settings.muteOnAwayHint}
                                   checked={view.muteOnAway}
                                   onChange={(value) => invoke(() => desktopAPI.setMuteOnAway(value))}/>
                    <SettingToggle title={m.settings.deafenOnAway} description={m.settings.deafenOnAwayHint}
                                   checked={view.deafenOnAway}
                                   onChange={(value) => invoke(() => desktopAPI.setDeafenOnAway(value))}/>
                </section>
                <HotkeySettings/></>}
                {section === "updates" && <UpdatesPanel updates={updates}/>}
            </div>
        </div>
    </section>;
}

function SoundPackPreview() {
    const {m} = useLocale();
    const [playing, setPlaying] = useState(false);
    const [error, setError] = useState("");
    const generation = useRef(0);
    const play = async () => {
        if (playing) {
            generation.current += 1;
            setPlaying(false);
            await desktopAPI.stopSoundPreview().catch(() => undefined);
            return;
        }
        const token = ++generation.current;
        setPlaying(true);
        setError("");
        try {
            await desktopAPI.previewSoundPack();
        } catch (cause) {
            if (generation.current === token) setError(rawError(cause));
        } finally {
            if (generation.current === token) setPlaying(false);
        }
    };
    return <div className="monitor-setting">
        <button type="button" className={`monitor-button ${playing ? "active" : ""}`} aria-pressed={playing} onClick={() => void play()}>
            <Icon name="sound"/>{playing ? m.settings.previewPackStop : m.settings.previewPack}
        </button>
        <p className="device-hint">{m.settings.previewPackHint}</p>
        {error && <p className="device-hint" role="alert">{error}</p>}
    </div>;
}

function SensitivitySlider({id = "rnnoise-sensitivity", max = 1, label, description, value, disabled, onChange, onCommit}: {
    id?: string;
    max?: number;
    label: string;
    description: string;
    value: number;
    disabled: boolean;
    onChange: (value: number) => void;
    onCommit: (value: number) => Promise<void>;
}) {
    const commit = (value: string) => void onCommit(Number(value));
    return <div className={`sensitivity-setting slider-setting ${disabled ? "disabled" : ""}`}>
        <div><label className="setting-label" htmlFor={id}>{label}</label>
            <output htmlFor={id}>{Math.round(value * 100)}%</output>
        </div>
        <p>{description}</p>
        <input id={id} aria-label={label} type="range" min="0" max={max} step="0.01"
               value={value} disabled={disabled}
               onChange={(event) => onChange(Number(event.target.value))}
               onPointerUp={(event) => commit(event.currentTarget.value)}
               onKeyUp={(event) => commit(event.currentTarget.value)}/>
    </div>;
}

function AudioWaveform({sensitivity, disabled, onSensitivityChange, onSensitivityCommit}: {
    sensitivity: number;
    disabled: boolean;
    onSensitivityChange: (value: number) => void;
    onSensitivityCommit: (value: number) => Promise<void>
}) {
    const {m} = useLocale();
    const canvasRef = useRef<HTMLCanvasElement>(null);
    const thresholdRef = useRef<HTMLDivElement>(null);
    const targetRef = useRef({input: 0, processed: 0, transmitted: 0, updatedAt: 0});
    const thresholdPosition = 1 - sensitivity;
    const thresholdLevel = (40 - 35 * sensitivity) / 60;
    const thresholdLevelRef = useRef(thresholdLevel);
    const sensitivityFromPosition = (position: number) => 1 - position;

    useEffect(() => {
        thresholdLevelRef.current = thresholdLevel;
        thresholdRef.current?.style.setProperty("--threshold", `${thresholdPosition * 100}%`);
    }, [thresholdLevel, thresholdPosition]);

    useEffect(() => desktopAPI.onAudioMeter((sample) => {
        targetRef.current = {
            input: Math.max(0, Math.min(1, sample.input)),
            processed: Math.max(0, Math.min(1, sample.processed ?? sample.input)),
            transmitted: Math.max(0, Math.min(1, sample.transmitted)),
            updatedAt: performance.now(),
        };
    }), []);

    useEffect(() => {
        const canvas = canvasRef.current;
        if (!canvas) return;
        const context = canvas.getContext("2d");
        if (!context) return;
        const points = 96;
        const rejectedHistory = Array<number>(points).fill(0);
        const transmittedHistory = Array<number>(points).fill(0);
        let currentInput = 0;
        let currentProcessed = 0;
        let currentTransmitted = 0;
        let previousSample = performance.now();
        let animationFrame = 0;

        const resize = () => {
            const bounds = canvas.getBoundingClientRect();
            const scale = window.devicePixelRatio || 1;
            canvas.width = Math.max(1, Math.round(bounds.width * scale));
            canvas.height = Math.max(1, Math.round(bounds.height * scale));
            context.setTransform(scale, 0, 0, scale, 0, 0);
        };
        const observer = new ResizeObserver(resize);
        observer.observe(canvas);
        resize();

        const ribbon = (history: number[], center: number, height: number, fill: string, stroke: string) => {
            const width = canvas.clientWidth;
            const step = width / Math.max(1, history.length - 1);
            context.beginPath();
            history.forEach((value, index) => {
                const y = center - Math.max(0.6, value * height);
                if (index === 0) context.moveTo(0, y);
                else context.lineTo(index * step, y);
            });
            for (let index = history.length - 1; index >= 0; index--) {
                context.lineTo(index * step, center + Math.max(0.6, history[index] * height));
            }
            context.closePath();
            context.fillStyle = fill;
            context.fill();
            context.strokeStyle = stroke;
            context.lineWidth = 1;
            context.stroke();
        };

        const draw = (now: number) => {
            const target = targetRef.current;
            const stale = now - target.updatedAt > 120;
            const inputTarget = stale ? 0 : target.input;
            const processedTarget = stale ? 0 : target.processed;
            const transmittedTarget = stale ? 0 : target.transmitted;
            currentInput += (inputTarget - currentInput) * (inputTarget > currentInput ? 0.42 : 0.16);
            currentProcessed += (processedTarget - currentProcessed) * (processedTarget > currentProcessed ? 0.45 : 0.035);
            currentTransmitted += (transmittedTarget - currentTransmitted) * (transmittedTarget > currentTransmitted ? 0.48 : 0.2);
            if (thresholdRef.current) {
                const levelOnThresholdScale = Math.max(0, Math.min(1, (currentProcessed - 1 / 12) / (7 / 12)));
                thresholdRef.current.style.setProperty("--level", `${levelOnThresholdScale * 100}%`);
                thresholdRef.current.classList.toggle("open", processedTarget >= thresholdLevelRef.current && processedTarget > 0.003);
            }
            if (now - previousSample >= 25) {
                const rejected = Math.max(0, currentInput - currentTransmitted);
                rejectedHistory.shift();
                rejectedHistory.push(rejected);
                transmittedHistory.shift();
                transmittedHistory.push(currentTransmitted);
                previousSample = now;
            }
            const width = canvas.clientWidth;
            const height = canvas.clientHeight;
            context.clearRect(0, 0, width, height);
            const waveCenter = height * 0.5;
            context.beginPath();
            context.moveTo(0, waveCenter);
            context.lineTo(width, waveCenter);
            context.strokeStyle = themeColor("--muted", 0.1);
            context.lineWidth = 1;
            context.stroke();
            ribbon(rejectedHistory, waveCenter, height * 0.38, themeColor("--muted", 0.18), themeColor("--muted", 0.34));
            ribbon(transmittedHistory, waveCenter, height * 0.38, themeColor("--accent", 0.24), themeColor("--accent", 0.9));
            animationFrame = requestAnimationFrame(draw);
        };
        animationFrame = requestAnimationFrame(draw);
        return () => {
            cancelAnimationFrame(animationFrame);
            observer.disconnect();
        };
    }, []);

    const updateThreshold = (position: number, commit: boolean) => {
        const value = sensitivityFromPosition(position);
        onSensitivityChange(value);
        if (commit) void onSensitivityCommit(value);
    };
    return <>
        <div className={`threshold-meter ${disabled ? "disabled" : ""}`} ref={thresholdRef}><span
            className="threshold-track"><span className="threshold-level"/></span><span className="threshold-marker"
                                                                                        aria-hidden="true"/><input
            aria-label={m.settings.threshold} type="range" min="0" max="1" step="0.01" value={thresholdPosition}
            disabled={disabled} onChange={(event) => updateThreshold(Number(event.target.value), false)}
            onPointerUp={(event) => updateThreshold(Number(event.currentTarget.value), true)}
            onKeyUp={(event) => updateThreshold(Number(event.currentTarget.value), true)}/></div>
        <div className="audio-waveform">
            <div className="audio-waveform-heading"><span className="setting-label">{m.settings.activity}</span><span
                className="wave-legend"><i className="transmitted"/>{m.settings.transmitted}<i className="rejected"/>{m.settings.rejected}</span>
            </div>
            <canvas ref={canvasRef} role="img" aria-label={m.settings.waveform}/>
        </div>
    </>;
}

function SettingToggle({title, description, checked, onChange}: {
    title: string;
    description: string;
    checked: boolean;
    onChange: (value: boolean) => Promise<void>
}) {
    return <div className="setting-toggle">
        <div><strong>{title}</strong><p>{description}</p></div>
        <label className="switch"><input type="checkbox" checked={checked}
                                         onChange={(event) => void onChange(event.target.checked)}/><span/></label>
    </div>;
}

export default App;
