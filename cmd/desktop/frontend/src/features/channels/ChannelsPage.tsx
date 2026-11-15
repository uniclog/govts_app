import {useEffect, useMemo, useRef, useState} from "react";
import type {Dispatch, SetStateAction} from "react";
import {useReducer} from "react";
import type {ClientEventDTO, ClientViewDTO, ParticipantDTO} from "../../api";
import {desktopAPI} from "../../api";
import {ScreenMediaController} from "../screen/screenMedia";
import {ScreenStageTile} from "../screen/ScreenViews";
import {type ScreenSharingState} from "../screen/ScreenShareDialog";
import {AudioControls, type Page} from "../connection/ConnectionViews";
import {ParticipantRow} from "../participants/ParticipantRow";
import {ConnectionProgress} from "../connection/ConnectionProgress";
import {EventPanel} from "../connection/EventPanel";
import {Icon} from "../../components/Icon";
import {avatarStyle} from "../../components/avatar";
import {ConnectionStatsPopup} from "../connection/ConnectionStatsPopup";
import {ServerSwitcher} from "../connection/ServerSwitcher";
import {ChatPanel} from "../chat/ChatPanel";
import {ChatStore} from "../chat/chatStore";
import {useWorkspaceResize} from "../../components/useWorkspaceResize";
import {type StageBackdrop} from "../../appearance";
import {errorCode} from "../../locales";
import {useLocale} from "../../locales/Locale";
import {StageScene} from "../settings/StageScene";
import {ChannelTree} from "./ChannelTree";

const contentTabs = ["chat", "events"] as const;
export type ContentTab = typeof contentTabs[number] | `direct:${string}`;
export type DirectChat = {userId: string; displayName: string};

export function ChannelsPage({view, events, invoke, sharing, screenMedia, onError, onReconnect, onWake, contentTab, setContentTab, directChats, setDirectChats, chatStore, refreshingServers, onRefreshServers, stageBackdrop, onPageChange, referenceBackdrop}: {
    onPageChange: (page: Page) => void;
    referenceBackdrop: boolean;
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
                        <button className="info-toggle" title={m.channels.info} aria-label={m.channels.info} aria-expanded={showInfo} onClick={() => setShowInfo(!showInfo)}><Icon name="info"/></button>
                    </header>
                    <div className="participants-stage" data-backdrop={stageBackdrop} data-reference-backdrop={referenceBackdrop} role="region" tabIndex={0} aria-label={m.channels.stage}>
                        {referenceBackdrop ? <div className="workspace-stage-art" aria-hidden="true"><i/><i/><i/><i/></div> : <StageScene backdrop={stageBackdrop}/>}
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
