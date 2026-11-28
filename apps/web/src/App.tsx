import { useCallback, useEffect, useState } from "react";
import { APIError, currentSession, logout, messageFor, post, publicConfig } from "./api/http";
import { useGateway } from "./api/useGateway";
import type { LoginMode, Participant, WebSession } from "./api/types";
import { Icon } from "./components/Icon";
import { useWorkspaceResize } from "./components/useWorkspaceResize";
import symbol from "./assets/sonoryx-symbol.svg?no-inline";
import { useVoice } from "./audio/useVoice";
import { Login } from "./features/auth/Login";
import { ChannelList } from "./features/channels/ChannelList";
import { Avatar, ChannelStage } from "./features/channels/ChannelStage";
import { ChatWorkspace } from "./features/chat/ChatWorkspace";
import { useScreenShare } from "./features/screen/useScreenShare";
import { ScreenTile, ScreenViewer } from "./features/screen/ScreenTile";
import { AudioSettings } from "./features/settings/AudioSettings";
import { AdminPanel } from "./features/admin/AdminPanel";

function savedDevice(kind: "capture" | "playback") {
  try { return localStorage.getItem("sonoryx.web." + kind) ?? ""; } catch { return ""; }
}

function saveDevice(kind: "capture" | "playback", value: string) {
  try { localStorage.setItem("sonoryx.web." + kind, value); } catch { /* Keep this tab's selection. */ }
}

function Workspace({ session, expired, signedOut }: { session: WebSession; expired: () => void; signedOut: () => void }) {
  const { view, events, socketState, connection, send } = useGateway(session, expired);
  const workspaceResize = useWorkspaceResize();
  const [busy, setBusy] = useState(false);
  const [leaving, setLeaving] = useState(false);
  const [issue, setIssue] = useState("");
  const [messagePeer, setMessagePeer] = useState<Participant>();
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [adminOpen, setAdminOpen] = useState(false);
  const [captureDevice, setCaptureDevice] = useState(() => savedDevice("capture"));
  const [playbackDevice, setPlaybackDevice] = useState(() => savedDevice("playback"));
  const online = socketState === "online";
  const connected = online && view?.status === "connected";
  const voice = useVoice(online, connection, session.iceServers, view?.muted ?? true, view?.deafened ?? false, send, captureDevice, playbackDevice);
  const room = view?.channels.find(({ id }) => id === view.channelId);
  const screen = useScreenShare(connected && room ? view.sessionId + ":" + room.id : "", session.iceServers);
  const streams = (view?.screenStreams ?? []).filter((stream) => stream.channelId === room?.id);
  const members = view?.participants.filter(({ channelId }) => channelId === room?.id) ?? [];
  const local = view?.participants.find(({ id }) => id === view.sessionId);
  const channelStreams = streams.map(({ id, ownerId }) => ({
    id, own: ownerId === view?.sessionId, owner: members.find((person) => person.id === ownerId)?.name ?? "Участник",
  }));
  const watchableStreams = channelStreams.filter(({ own }) => !own);
  const watchedStream = watchableStreams.find(({ id }) => id === screen.viewerID);
  const ownPreview = screen.local;
  const screenTiles = channelStreams.map(({ id, own, owner }) => <ScreenTile key={id} owner={owner}
    own={own} advertised canWatch={screen.enabled} preview={own && ownPreview?.id === id ? ownPreview.preview : undefined}
    onWatch={() => void screen.watch(id)} />);
  if (ownPreview && !channelStreams.some(({ id }) => id === ownPreview.id)) {
    screenTiles.push(<ScreenTile key={ownPreview.id} owner={local?.name ?? "Вы"} own advertised={false}
      canWatch={false} preview={ownPreview.preview} />);
  }
  const viewerEnded = screen.viewerID !== "" && !watchedStream;
  const closeScreenView = screen.closeView;
  useEffect(() => {
    if (viewerEnded) closeScreenView();
  }, [viewerEnded, closeScreenView]);
  const micOff = view?.muted || voice.mic !== "ready";
  const micLabel = voice.mic === "requesting" ? "Ожидание разрешения…" : micOff ? "Включить микрофон" : "Выключить микрофон";
  const soundLabel = view?.deafened ? "Включить звук" : "Выключить звук";
  const offlineStatus = {
    online: "", connecting: "Подключение к шлюзу…",
    retrying: "Связь со шлюзом потеряна", duplicate: "Веб-сессия открыта в другой вкладке",
  };
  const status = !online ? offlineStatus[socketState]
    : view?.status === "reconnecting" ? "Восстанавливаем связь с Sonoryx…"
      : view?.status === "disconnected" ? "Связь с Sonoryx завершена"
        : connected ? "Подключено" : "Подключение к Sonoryx…";

  const change = useCallback(async (action: Parameters<typeof send>[0]) => {
    setIssue("");
    setBusy(true);
    try {
      await send(action);
    } catch (reason) {
      setIssue(messageFor(reason));
    } finally {
      setBusy(false);
    }
  }, [send]);

  useEffect(() => {
    if (!connected) return;
    const onKeyDown = (event: KeyboardEvent) => {
      const target = event.target;
      if (event.repeat || event.defaultPrevented || event.isComposing || event.getModifierState("AltGraph") ||
          !event.ctrlKey || !event.altKey || event.shiftKey || event.metaKey ||
          (target instanceof HTMLElement && (target.isContentEditable || target.closest("input, textarea, select")))) return;
      if (event.code === "KeyM" && !voice.micBusy) {
        event.preventDefault();
        void voice.toggleMicrophone();
      } else if (event.code === "KeyD" && !busy) {
        event.preventDefault();
        void change({ type: "deafen", value: !view?.deafened });
      } else if (event.code === "KeyE" && !busy) {
        event.preventDefault();
        void change({ type: "away", value: !local?.away });
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [connected, busy, voice.micBusy, voice.toggleMicrophone, view?.deafened, local?.away, change]);

  async function leave() {
    setLeaving(true);
    setIssue("");
    try {
      await logout();
      signedOut();
    } catch (reason) {
      setIssue(messageFor(reason));
      setLeaving(false);
    }
  }

  return (
    <main className="workspace">
      <header className="titlebar">
        <img src={symbol} alt="" /><h1>SRX — {room?.name || view?.serverName || "Sonoryx"}</h1>
      </header>
      <div ref={workspaceResize.layoutRef} style={workspaceResize.style} className={workspaceResize.resizing ? "layout is-resizing" : "layout"}>
        <aside className="channel-browser" id="channel-browser-pane">
          <div className="server-entry"><span className="channel-heading-icon"><Icon name="server" /></span>
            <div><strong>{view?.serverName || "Sonoryx"}</strong><small>{window.location.host}</small></div>
          </div>
          <ChannelList channels={view?.channels ?? []} participants={view?.participants ?? []} current={view?.channelId ?? "0"}
            busy={!connected || busy} enter={(channelId) => void change({ type: "join", channelId })} />
          <footer className="user-footer">
            <Avatar name={local?.name ?? ""} speaking={!!local?.speaking && !micOff} />
            <div><strong>{local?.name || "Веб-сессия"}</strong>
              <span className={connected ? "status online" : "status"} role="status">{status}</span>
            </div>
            <button className="settings-button" type="button" aria-label="Настройки" title="Настройки" onClick={() => setSettingsOpen(true)}>
              <Icon name="settings" />
            </button>
            {connected && view.canAdmin && <button className="admin-button" type="button" aria-label="Управление сервером"
              title="Управление сервером" onClick={() => setAdminOpen(true)}><Icon name="shield" /></button>}
          </footer>
        </aside>
        <div className="workspace-divider" {...workspaceResize.separatorProps("browser")} />
        <section className="room" aria-label="Голосовой канал">
          {connected && room ? <ChannelStage channel={room} members={members} screens={screenTiles} sessionId={view.sessionId} userId={view.userId}
            viewer={watchedStream && <ScreenViewer current={watchedStream} streams={watchableStreams} preview={screen.remote}
              onWatch={(id) => void screen.watch(id)} onClose={screen.closeView} />}
            moderation={view} channels={view.channels}
            onMessage={(person) => setMessagePeer({ ...person })}
            onWake={(participantId) => void change({ type: "wake", participantId })}
            onModerate={(action, participantId, channelId) => {
              setIssue("");
              void post<void>("/api/moderation", { action, participantId, channelId })
                .catch((reason) => setIssue(messageFor(reason)));
            }} />
            : <div className="stage-placeholder"><Icon name="voice" /><p>{connected ? "Выберите канал." : status}</p></div>}
          {(issue || view?.error) && <p className="notice room-notice" role="status">{issue || view?.error}</p>}
          {screen.error && <p className="notice room-notice" role="status">{screen.error}</p>}
          <div className="audio-dock">
            {(voice.deviceNote || voice.error) && <p className="notice" role="status">{voice.deviceNote || voice.error}</p>}
            {online && voice.status !== "connected" && (
              <small role="status">{voice.status === "offline" ? "Голос недоступен" : "Подключение голоса…"}</small>
            )}
            <div className="audio-controls" role="group" aria-label="Управление голосом">
              <button className={micOff ? "is-off" : ""} type="button" aria-label={micLabel} title={micLabel}
                aria-pressed={!!micOff} disabled={!connected || voice.micBusy} onClick={() => void voice.toggleMicrophone()}><Icon name="mic" /><span>Микрофон</span></button>
              <button className={view?.deafened ? "is-off" : ""} type="button" aria-label={soundLabel} title={soundLabel}
                aria-pressed={!!view?.deafened} disabled={!connected || busy} onClick={() => void change({ type: "deafen", value: !view?.deafened })}><Icon name="headphones" /><span>Звук</span></button>
              <button className={local?.away ? "is-away" : ""} type="button" aria-label={local?.away ? "Я вернулся" : "Отошёл"}
                title={local?.away ? "Я вернулся" : "Отошёл"} aria-pressed={!!local?.away} disabled={!connected || busy}
                onClick={() => void change({ type: "away", value: !local?.away })}><Icon name="away" /><span>Отошёл</span></button>
              {screen.enabled && <button type="button" className={screen.publishing ? "is-sharing" : ""} aria-pressed={screen.publishing}
                aria-label={screen.publishing ? "Остановить демонстрацию" : "Показать экран"} title={screen.publishing ? "Остановить демонстрацию" : "Показать экран"}
                disabled={screen.busy} onClick={() => screen.publishing ? screen.stop() : void screen.publish()}><Icon name="screen" /><span>Экран</span></button>}
              {voice.playBlocked && <button type="button" aria-label="Начать прослушивание" title="Начать прослушивание" onClick={() => void voice.play()}><Icon name="play" /></button>}
              {voice.status === "offline" && online && <button type="button" aria-label="Переподключить голос" title="Переподключить голос" onClick={voice.retry}><Icon name="refresh" /></button>}
              <button className="hangup" type="button" aria-label="Отключиться" title="Отключиться" onClick={() => void leave()} disabled={leaving}><Icon name="hangup" /><span>Отключиться</span></button>
            </div>
          </div>
        </section>
        <div className="workspace-divider" {...workspaceResize.separatorProps("chat")} />
        <section className="chat-pane" id="chat-pane" aria-label="Чат канала">
          {connected && room
            ? <ChatWorkspace key={connection + ":" + view.sessionId} channelId={room.id} userId={view.userId}
              revision={view.chatRevision} send={send} requested={messagePeer} events={events} />
            : <div className="empty room-empty">{connected ? "Выберите канал." : "Чат появится после подключения."}</div>}
        </section>
      </div>
      <audio ref={voice.audio} autoPlay playsInline muted={voice.deafened} />
      {settingsOpen && <AudioSettings close={() => setSettingsOpen(false)} capture={captureDevice} playback={playbackDevice}
        onCapture={(id) => { setCaptureDevice(id); saveDevice("capture", id); }}
        onPlayback={(id) => { setPlaybackDevice(id); saveDevice("playback", id); }} />}
      {adminOpen && connected && view.canAdmin && <AdminPanel channels={view.channels} userId={view.userId} isOwner={view.isOwner} close={() => setAdminOpen(false)} />}
    </main>
  );
}

export default function App() {
  const [session, setSession] = useState<WebSession>();
  const [mode, setMode] = useState<LoginMode>();
  const [loadError, setLoadError] = useState("");
  const [loadAttempt, setLoadAttempt] = useState(0);
  const [notice, setNotice] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(6000)]);
    setLoadError("");
    const previousSession = currentSession(signal).catch((error: unknown) => {
      if (error instanceof APIError && error.status === 401) return undefined;
      throw error;
    });
    void Promise.all([publicConfig(signal), previousSession]).then(([config, previous]) => {
      if (controller.signal.aborted) return;
      setSession(previous);
      setMode(config.loginMode);
    }).catch((error) => { if (!controller.signal.aborted) setLoadError(messageFor(error)); });
    return () => controller.abort();
  }, [loadAttempt]);
  const expired = useCallback(() => {
    setSession(undefined);
    setNotice("Веб-сессия завершена. Войдите заново.");
  }, []);
  const signedOut = useCallback(() => {
    setSession(undefined);
    setNotice("");
  }, []);
  if (!mode) return <main className="loading"><p role="status">{loadError || "Подключение к шлюзу…"}</p>
    {loadError && <button type="button" onClick={() => setLoadAttempt((value) => value + 1)}>Повторить</button>}</main>;
  if (!session) return <Login mode={mode} connected={setSession} notice={notice} />;
  return <Workspace session={session} expired={expired} signedOut={signedOut} />;
}
