import {useCallback, useEffect, useMemo, useRef, useState} from "react";
import type {Dispatch, SetStateAction} from "react";
import {useReducer} from "react";
import type {ClientEventDTO, ClientViewDTO, ParticipantDTO} from "./api";
import {desktopAPI} from "./api";
import {mergeEventTail} from "./model";
import {ScreenMediaController} from "./features/screen/screenMedia";
import {ScreenStageTile, ScreenViewerWindow} from "./features/screen/ScreenViews";
import {LinkConfirmDialog} from "./features/chat/LinkConfirm";
import {MediaTrustDialog} from "./features/screen/MediaTrustConfirm";
import {ScreenShareDialog, useScreenSharing, type ScreenSharingState} from "./features/screen/ScreenShareDialog";
import {AudioControls, ConnectionPage, LobbyBar, StatusBar, type Page} from "./features/connection/ConnectionViews";
import {ParticipantRow} from "./features/participants/ParticipantRow";
import {ConnectionProgress} from "./features/connection/ConnectionProgress";
import {EventPanel} from "./features/connection/EventPanel";
import {rememberServerAddress} from "./features/connection/ConnectionViews";
import {Icon} from "./components/Icon";
import {avatarStyle} from "./components/avatar";
import {ConnectionStatsPopup} from "./features/connection/ConnectionStatsPopup";
import {ServerSwitcher} from "./features/connection/ServerSwitcher";
import {WindowControls} from "./components/WindowControls";
import {ChatPanel} from "./features/chat/ChatPanel";
import {ChatStore} from "./features/chat/chatStore";
import {channelMuted, channelNotificationScope, messagePreview, setChannelMuted, useMutedChannels} from "./features/chat/channelNotifications";
import {useWorkspaceResize} from "./components/useWorkspaceResize";
import {UpdateButton, UpdateDialog, useUpdates} from "./features/updates/Updates";
import {applyAppearance, loadAppearance, normalizeTheme, saveAppearance, type Appearance, type AppearanceOptions, type StageBackdrop} from "./appearance";
import {attentionLockSeconds, errorCode, errorText, rawError} from "./locales";
import {freshWakeGate, wakeClock, wakeDecision, wakeFailed, wakeRejected, wakeSent, type WakeGate} from "./features/participants/wakeLimit";
import {useLocale} from "./locales/Locale";
import {AdminPanel} from "./features/admin/AdminPanel";
import {StageScene} from "./features/settings/StageScene";
import {ChannelTree} from "./features/channels/ChannelTree";
import {SettingsOverlay} from "./features/settings/SettingsOverlay";
import {SettingsPage} from "./features/settings/SettingsPage";

const contentTabs = ["chat", "events"] as const;
type ContentTab = typeof contentTabs[number] | `direct:${string}`;
type DirectChat = {userId: string; displayName: string};

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
        remember({...previous, theme: mode});
        try { await desktopAPI.setTheme(mode); }
        catch (error) {
            remember(previous);
            throw error;
        }
    };
    const updateAppearance = (patch: AppearanceOptions) => {
        remember({...loadAppearance(), ...patch});
    };
    const params = new URLSearchParams(window.location.search);
    const streamID = params.get("screen");
    if (streamID) return <><ScreenViewerWindow streamID={streamID} ownerName={params.get("owner") || m.screen.participant}/><MediaTrustDialog/></>;
    return <MainApp appearance={appearance} setTheme={setTheme} updateAppearance={updateAppearance}/>;
}

function MainApp({appearance, setTheme, updateAppearance}: {
    appearance: Appearance;
    setTheme: (value: string) => Promise<void>;
    updateAppearance: (patch: AppearanceOptions) => void;
}) {
    const {m} = useLocale();
    const [view, setView] = useState<ClientViewDTO>(emptyView);
    const [page, setPage] = useState<Page>("channels");
    const [contentTab, setContentTab] = useState<ContentTab>("chat");
    const [directChats, setDirectChats] = useState<DirectChat[]>([]);
    const [chatStore, setChatStore] = useState<ChatStore | null>(null);
    const chatStoreRef = useRef<ChatStore | null>(null);
    // The handler outlives renders, so it reads the current names from here.
    const noticeRef = useRef({channels: view.channels, scope: "", m});
    noticeRef.current = {channels: view.channels, scope: channelNotificationScope(view.chatContext), m};
    useEffect(() => {
        if (view.userId === "0" || view.connectionStatus !== "connected") return;
        if (chatStoreRef.current?.context === view.chatContext) return;
        chatStoreRef.current?.dispose();
        const store = new ChatStore(view.chatContext, view.userId);
        store.setMessageReceivedHandler((messages) => {
            const {channels, scope, m} = noticeRef.current;
            const heard = messages.filter((message) => message.channelId === "0" || !channelMuted(scope, message.channelId));
            if (!heard.length) return;
            const latest = heard.reduce((a, b) => BigInt(b.id) > BigInt(a.id) ? b : a);
            const channel = channels?.find((item) => item.id === latest.channelId)?.name;
            const title = latest.channelId === "0" ? m.channels.direct(latest.senderName) : channel ? `${latest.senderName} · ${channel}` : latest.senderName;
            void desktopAPI.messageReceived(title, messagePreview(latest, m.chat.pinAttachment)).catch(() => undefined);
        });
        chatStoreRef.current = store;
        setChatStore(store); setDirectChats([]); setContentTab("chat");
    }, [view.chatContext, view.userId, view.connectionStatus]);
    useEffect(() => () => chatStoreRef.current?.dispose(), []);
    useEffect(() => {
        chatStore?.setConnected(view.connectionStatus === "connected");
    }, [chatStore, view.connectionStatus]);
    useEffect(() => { chatStore?.changed(); }, [chatStore, view.chatRevision]);
    // The chat of the current channel is followed even when it is not on screen
    // or the window is minimised, so its messages still ring and pop up.
    useEffect(() => {
        if (!chatStore || view.connectionStatus !== "connected" || view.channelId === "0") return;
        return chatStore.watch({kind: "channel", id: view.channelId});
    }, [chatStore, view.connectionStatus, view.channelId]);
    const [events, setEvents] = useState<ClientEventDTO[]>([]);
    const [actionError, setActionErrorState] = useState("");
    const [actionErrorTick, setActionErrorTick] = useState(0);
    const setActionError = useCallback((message: string) => {
        setActionErrorState(message);
        if (message) setActionErrorTick((tick) => tick + 1);
    }, []);
    const [refreshingServers, setRefreshingServers] = useState(false);
    const refreshingServersRef = useRef(false);
    const lastServersRefresh = useRef(0);
    // quiet: an automatic refresh shows the spinner but no error banner.
    const refreshServers = useCallback(async (quiet = false) => {
        if (refreshingServersRef.current) return;
        refreshingServersRef.current = true;
        lastServersRefresh.current = Date.now();
        setRefreshingServers(true);
        if (!quiet) setActionError("");
        try { await desktopAPI.refreshServerStatuses(); }
        catch (error) { if (!quiet) setActionError(rawError(error)); }
        finally { refreshingServersRef.current = false; setRefreshingServers(false); }
    }, []);
    // Opening the window from the tray, the taskbar or the Start menu refreshes
    // the saved servers, but not within a minute of the previous refresh.
    useEffect(() => desktopAPI.onMainWindowShown(() => {
        if (Date.now() - lastServersRefresh.current >= 60_000) void refreshServers(true);
    }), [refreshServers]);
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
                    onRefreshServers={() => void refreshServers()}
                    onClearEvents={clearEvents}
                    onConnected={() => setPage("channels")}
                />
            </> : <>
            <StatusBar view={view} updateAction={<UpdateButton updates={updates}/>}/>
            {actionError && <div className="error-banner" role="alert">{errorText(actionError, m)}</div>}
            {wakeUntil > wakeNow && <WakeNotice until={wakeUntil} now={wakeNow} label={wakeLabel}/>}
            <ChannelsPage onPageChange={setPage} view={view} screenMedia={screenMedia.current} events={events} invoke={invoke} onWake={wakeParticipant} refreshingServers={refreshingServers} onRefreshServers={() => void refreshServers()}
                          stageBackdrop={appearance.stageBackdrop}
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
            <SettingsPage view={view} invoke={invoke} appearance={appearance} setTheme={setTheme} updateAppearance={updateAppearance} updates={updates}/>
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

function ChannelsPage({view, events, invoke, sharing, screenMedia, onError, onReconnect, onWake, contentTab, setContentTab, directChats, setDirectChats, chatStore, refreshingServers, onRefreshServers, stageBackdrop, onPageChange}: {
    onPageChange: (page: Page) => void;
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
    const [channelQuery, setChannelQuery] = useState("");
    const channelStreams = (view.screenStreams ?? []).filter((stream) => stream.channelId === view.channelId);
    const hasScreens = channelStreams.length > 0;
    const allTabs: ContentTab[] = [...contentTabs, ...directChats.map((chat): ContentTab => `direct:${chat.userId}`)];
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
    const infoVisible = showInfo && Boolean(selected) && view.connectionStatus === "connected";
    const noticeScope = channelNotificationScope(view.chatContext);
    const mutedChannels = useMutedChannels(noticeScope);
    const selectedMuted = !!selected && mutedChannels.has(selected.id);
    const workspaceResize = useWorkspaceResize(infoVisible);
    const members = participants.filter((item) => item.channelId === selectedID);
    const localParticipant = participants.find((person) => person.local);
    const speakingMembers = members.filter((person) => person.speaking && !person.muted && !person.deafened && !person.away);
    return <section className="channels-page">
        <div ref={workspaceResize.layoutRef} style={workspaceResize.style} className={`channels-layout ${infoVisible ? "" : "info-hidden"} ${workspaceResize.resizing ? "is-resizing" : ""}`}>
            <section id="channel-browser-pane" className="panel channel-browser">
                <ServerSwitcher view={view} refreshingServers={refreshingServers} onRefreshServers={onRefreshServers} onReconnect={onReconnect} onError={onError}/>
                <label className="channel-search"><Icon name="search"/><input id="server-channel-search" value={channelQuery} onChange={(event) => setChannelQuery(event.target.value)} placeholder={m.channels.search} aria-label={m.channels.search}/></label>
                <section className="sidebar-section" aria-label={m.channels.browser}>
                    <div className="panel-heading"><h2>{m.channels.voiceChannels}</h2></div>
                    <div className="channel-scroll"><ChannelTree query={channelQuery} channels={channels} participants={participants} screenOwners={screenOwners}
                                                             canKick={view.canKick} canBan={view.canBan}
                                                             canDrag={view.canDrag && view.connectionStatus === "connected"}
                                                             wakeEnabled={view.connectionStatus === "connected"}
                                                             currentID={view.channelId}
                                                             mutedIDs={mutedChannels}
                                                             onError={onError} onMessage={openMessage}
                                                             onWake={(participant) => { onWake(participant.sessionId); }}
                                                             onJoin={(id) => {
                    if (id !== view.channelId && view.connectionStatus === "connected") void invoke(() => desktopAPI.joinChannel(id));
                }} onMoveParticipant={(sessionID, channelID) => {
                    void invoke(() => desktopAPI.drag(sessionID, channelID));
                }}/></div>
                </section>
                <footer className="workspace-user-footer">
                    <span className={`footer-avatar ${localParticipant?.speaking && !view.audio.muted ? "speaking" : ""}`} style={avatarStyle(localChatName)}>{localChatName.slice(0, 1).toUpperCase()}</span>
                    <div className="workspace-user-identity"><strong title={localChatName}>{localChatName}</strong><ConnectionStatsPopup sessionId={view.sessionId} status={view.connectionStatus} channelId={view.channelId} sharing={sharing} screenMedia={screenMedia}/></div>
                    {view.canAdmin && view.connectionStatus === "connected" && <button id="open-admin" type="button" aria-label={m.admin.open} title={m.admin.open} onClick={() => onPageChange("admin")}><Icon name="shield"/></button>}
                    <button id="open-settings" type="button" aria-label={m.connection.settings} title={m.connection.settings} onClick={() => onPageChange("settings")}><Icon name="settings"/></button>
                </footer>
            </section>
            <div className="workspace-divider workspace-browser-divider" {...workspaceResize.separatorProps("browser")}/>
            <section className={`channel-stage${hasScreens ? " has-screen-stage" : ""} ${view.connectionStatus !== "connected" ? "connection-pending" : ""}`}>
                {selected && view.connectionStatus === "connected" ? <>
                    <header className="channel-stage-heading">
                        <span className="channel-heading-icon"><Icon name="voice"/></span>
                        <div><h2 title={selected.name}>{selected.name}</h2><p title={selected.topic}>{selected.topic || m.channels.voice} · {members.length}{selected.maxUsers ? ` / ${selected.maxUsers}` : ""} · {selected.audio.codec} {m.channels.kbps(selected.audio.bitrate / 1000)}</p></div>
                        <span className="channel-member-stack" aria-label={m.channels.members}>{members.slice(0, 4).map((person) => <span key={person.sessionId} style={avatarStyle(person.displayName)} title={person.displayName}>{person.displayName.slice(0, 1).toUpperCase()}</span>)}{members.length > 4 && <small>+{members.length - 4}</small>}</span>
                        <button className={`info-toggle channel-notify-toggle${selectedMuted ? " is-muted" : ""}`} type="button" title={selectedMuted ? m.channels.unmuteChannel : m.channels.muteChannel} aria-label={selectedMuted ? m.channels.unmuteChannel : m.channels.muteChannel} aria-pressed={selectedMuted} onClick={() => setChannelMuted(noticeScope, selected.id, !selectedMuted)}><Icon name={selectedMuted ? "bellOff" : "bell"}/></button>
                        <button className="info-toggle" title={m.channels.info} aria-label={m.channels.info} aria-expanded={showInfo} onClick={() => setShowInfo(!showInfo)}><Icon name="info"/></button>
                    </header>
                    <div className="participants-stage" data-backdrop={stageBackdrop} role="region" tabIndex={0} aria-label={m.channels.stage}>
                        <StageScene backdrop={stageBackdrop}/>
                        <div className={`stage-speaking-summary ${speakingMembers.length ? "is-speaking" : ""}`}><Icon name="voice"/><span>{speakingMembers.length ? `${m.channels.speakingNow} ${speakingMembers.map((person) => person.displayName).join(", ")}` : m.channels.quiet}</span></div>
                        <div className="stage-member-summary"><Icon name="users"/>{members.length}{selected.maxUsers ? ` / ${selected.maxUsers}` : ""}</div>
                        <div className="participants-strip" data-single={members.length + channelStreams.length === 1}>
                            {members.map((participant) => <div className="stage-participant-group" key={participant.sessionId}><ParticipantRow participant={participant} variant="stage" sharingScreen={screenOwners.has(participant.sessionId)} canKick={view.canKick} canBan={view.canBan} canDrag={false} canWake={participant.channelId === view.channelId} onWake={(person) => onWake(person.sessionId)} depth={0} onError={onError} onMessage={openMessage} onDragStart={(event) => event.preventDefault()} onDragEnd={() => {}}/></div>)}
                            {channelStreams.map((stream) => <ScreenStageTile key={stream.id} stream={stream} ownerName={participants.find((person) => person.sessionId === stream.ownerSessionId)?.displayName || m.screen.participant} local={stream.ownerSessionId === view.sessionId} connected={view.connectionStatus === "connected"} onError={onError} screenMedia={screenMedia}/>)}
                            {!members.length && !channelStreams.length && <div className="stage-empty">{m.channels.emptyStage}</div>}
                        </div>
                    </div>
                </> : <ConnectionProgress view={view} events={events} onDisconnect={() => invoke(() => desktopAPI.disconnect())}/>}
                {view.connectionStatus === "connected" && <div className="channel-audio-dock"><AudioControls view={view} invoke={invoke} sharing={sharing}/></div>}
            </section>
            <div className="workspace-divider workspace-chat-divider" {...workspaceResize.separatorProps("chat")}/>
            {selected && view.connectionStatus === "connected" ? <section id="server-content-pane" className="channel-content" hidden={infoVisible}>
                    <div className="channel-tabs" role="tablist" aria-label={m.channels.content} onKeyDown={(event) => {
                        if ((event.target as HTMLElement).getAttribute("role") !== "tab") return;
                        if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
                        event.preventDefault();
                        const index = allTabs.indexOf(contentTab);
                        const next = event.key === "Home" ? allTabs[0] : event.key === "End" ? allTabs[allTabs.length - 1] : allTabs[(index + (event.key === "ArrowRight" ? 1 : -1) + allTabs.length) % allTabs.length];
                        setContentTab(next);
                        document.getElementById(`${next}-tab`)?.focus();
                    }}>
                        <div className="workspace-primary-tabs" role="presentation">
                        <button id="chat-tab" role="tab" tabIndex={contentTab === "chat" ? 0 : -1} aria-selected={contentTab === "chat"} aria-controls="chat-panel" onClick={() => setContentTab("chat")}><Icon name="chat"/>{m.channels.chat}{chatStore && chatChannelID !== "0" && chatStore.conversation({kind: "channel", id: chatChannelID}).unread > 0 && <span className="count-badge">{chatStore.conversation({kind: "channel", id: chatChannelID}).unread}</span>}</button>
                        <button id="events-tab" role="tab" tabIndex={contentTab === "events" ? 0 : -1} aria-selected={contentTab === "events"} aria-controls="events-panel" onClick={() => setContentTab("events")}>{m.channels.events}</button>
                        </div>
                        {directChats.length > 0 && <div className="workspace-direct-tabs" role="presentation">
                        {directChats.map((chat) => {
                            const tab: ContentTab = `direct:${chat.userId}`;
                            const dialog = dialogs.find((d) => d.userId === chat.userId);
                            const name = participants.find((participant) => participant.userId === chat.userId)?.displayName ?? dialog?.displayName ?? chat.displayName;
                            return <div className="direct-tab" role="presentation" key={chat.userId} data-active={contentTab === tab}>
                                <button id={`${tab}-tab`} role="tab" tabIndex={contentTab === tab ? 0 : -1} aria-selected={contentTab === tab} aria-controls={`${tab}-panel`} title={name} onClick={() => setContentTab(tab)}><span>{name}</span>{!!dialog?.unread && <span className="count-badge">{dialog.unread}</span>}</button>
                                <button className="close-chat-tab" type="button" aria-label={m.channels.closeDirect(name)} title={m.channels.closeTab} onClick={() => closeMessage(chat.userId)}>×</button>
                            </div>;
                        })}</div>}
                    </div>
                    <div id="chat-panel" role="tabpanel" aria-labelledby="chat-tab" hidden={contentTab !== "chat"}>{chatStore && chatChannelID !== "0" ? <ChatPanel key={chatChannelID} store={chatStore} localName={localChatName} target={{kind: "channel", id: chatChannelID}} title={m.channels.channelChat(channels.find((c) => c.id === chatChannelID)?.name ?? "")} active={contentTab === "chat" && !infoVisible} connected={view.connectionStatus === "connected" && view.channelId === chatChannelID}/> : <div className="chat-placeholder">{m.channels.waitingChannel}</div>}</div>
                    <div id="events-panel" role="tabpanel" aria-labelledby="events-tab" hidden={contentTab !== "events"}><EventPanel events={events} active={contentTab === "events" && !infoVisible}/></div>
                    {directChats.map((chat) => <div key={chat.userId} id={`direct:${chat.userId}-panel`} role="tabpanel" aria-labelledby={`direct:${chat.userId}-tab`} hidden={contentTab !== `direct:${chat.userId}`}>
                        {chatStore && <ChatPanel store={chatStore} localName={localChatName} target={{kind: "direct", id: chat.userId}} title={m.channels.direct(participants.find((participant) => participant.userId === chat.userId)?.displayName ?? dialogs.find((d) => d.userId === chat.userId)?.displayName ?? chat.displayName)} online={participants.some((p) => p.userId === chat.userId)} active={contentTab === `direct:${chat.userId}` && !infoVisible} connected={view.connectionStatus === "connected"}/>}
                    </div>)}
                </section> : <section id="server-content-pane" className="channel-content"><div className="chat-placeholder">{m.channels.waitingChannel}</div></section>}
            {infoVisible && selected && <section id="channel-info-pane" className="panel channel-detail">{selected ? <><div className="channel-info-heading"><span>{m.channels.info}</span><button aria-label={m.channels.closeInfo} onClick={() => setShowInfo(false)}>×</button></div>
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

export default App;
