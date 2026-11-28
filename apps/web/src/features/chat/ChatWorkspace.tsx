import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { messageFor } from "../../api/http";
import type { ChatTarget, ClientEvent, Dialog, Participant, Send } from "../../api/types";
import { Icon } from "../../components/Icon";
import { ChatPanel } from "./ChatPanel";

export function ChatWorkspace({ channelId, userId, revision, send, requested, events }: {
  channelId: string;
  userId: string;
  revision: string;
  send: Send;
  requested?: Participant;
  events: ClientEvent[];
}) {
  const [tab, setTab] = useState("channel");
  const [dialogs, setDialogs] = useState<Dialog[]>([]);
  const [opened, setOpened] = useState<Dialog[]>([]);
  const [cursor, setCursor] = useState("0");
  const [more, setMore] = useState(false);
  const [dialogError, setDialogError] = useState("");
  const pending = useRef(false);
  const paginated = useRef(false);

  const loadDialogs = useCallback(async (from = "0", signal?: AbortSignal) => {
    if (pending.current) return;
    pending.current = true;
    try {
      const page = await send({ type: "chat.dialogs", cursor: from });
      if (!page || signal?.aborted) return;
      const items = page.dialogs ?? [];
      setDialogs((current) => {
        const byUser = new Map(current.map((item) => [item.userId, item]));
        for (const item of items) byUser.set(item.userId, item);
        return [...byUser.values()];
      });
      // Keep the explicit pagination cursor when the first page is refreshed.
      if (from !== "0") paginated.current = true;
      if (from !== "0" || !paginated.current) {
        setCursor(page.cursor);
        setMore(page.hasMore);
      }
      setDialogError("");
    } catch (reason) {
      if (!signal?.aborted) setDialogError(messageFor(reason));
    } finally {
      pending.current = false;
    }
  }, [send]);

  useEffect(() => {
    const controller = new AbortController();
    const refresh = () => void loadDialogs("0", controller.signal);
    const soon = window.setTimeout(refresh, 300);
    const clock = window.setInterval(refresh, 5000);
    return () => { controller.abort(); window.clearTimeout(soon); window.clearInterval(clock); };
  }, [loadDialogs, revision]);

  const open = useCallback((dialog: Dialog) => {
    if (dialog.userId === "0" || dialog.userId === userId) return;
    setOpened((current) => current.some((entry) => entry.userId === dialog.userId) ? current : [...current, dialog]);
    setTab(dialog.userId);
  }, [userId]);

  useEffect(() => {
    if (requested) open({ userId: requested.userId, name: requested.name, unread: 0 });
  }, [requested, open]);

  const channelTarget = useMemo<ChatTarget>(() => ({ channelId }), [channelId]);
  const unread = dialogs.filter((dialog) => dialog.userId !== tab).reduce((sum, dialog) => sum + dialog.unread, 0);
  return <>
    <header className="chat-tabs" aria-label="Разделы чата">
      <button type="button" aria-pressed={tab === "channel"} onClick={() => setTab("channel")}><Icon name="chat" />Чат</button>
      <button type="button" aria-pressed={tab === "events"} onClick={() => setTab("events")}>События</button>
      <button type="button" aria-pressed={tab === "dialogs"} onClick={() => setTab("dialogs")}>Личные {unread > 0 && <span>{unread}</span>}</button>
      {opened.map((dialog) => <div className="direct-tab" key={dialog.userId}>
        <button type="button" aria-pressed={tab === dialog.userId} title={dialog.name} onClick={() => setTab(dialog.userId)}>{dialog.name}</button>
        <button type="button" aria-label={"Закрыть чат с " + dialog.name} onClick={() => {
          setOpened((current) => current.filter((entry) => entry.userId !== dialog.userId));
          if (tab === dialog.userId) setTab("dialogs");
        }}>×</button>
      </div>)}
    </header>
    {tab === "events" && <EventPanel events={events} />}
    {tab === "dialogs" && <section className="dialog-list" aria-label="Личные чаты">
      {dialogError && <p className="notice">{dialogError}</p>}
      {dialogs.length === 0 && <p className="empty">Чтобы написать участнику, нажми значок чата рядом с его именем.</p>}
      {dialogs.map((dialog) => <button key={dialog.userId} type="button" onClick={() => open(dialog)}>
        {dialog.name}{dialog.unread > 0 && <span>{dialog.unread}</span>}
      </button>)}
      {more && <button type="button" onClick={() => void loadDialogs(cursor)}>Загрузить ещё</button>}
    </section>}
    <div className="chat-conversation" hidden={tab !== "channel"}>
      <ChatPanel key={channelId} active={tab === "channel"} target={channelTarget} userId={userId} revision={revision} send={send} />
    </div>
    {opened.map((dialog) => <DirectConversation key={dialog.userId} peerId={dialog.userId}
      active={tab === dialog.userId} userId={userId} revision={revision} send={send} />)}
  </>;
}

function EventPanel({ events }: { events: ClientEvent[] }) {
  const scroller = useRef<HTMLDivElement>(null);
  const follow = useRef(true);
  useEffect(() => {
    if (follow.current && scroller.current) scroller.current.scrollTop = scroller.current.scrollHeight;
  }, [events]);
  return <div className="event-list" aria-label="Журнал событий" ref={scroller} onScroll={(event) => {
    const element = event.currentTarget;
    follow.current = element.scrollHeight - element.scrollTop - element.clientHeight < 12;
  }}>
    {!events.length && <p className="empty">Событий пока нет.</p>}
    {events.map((event) => <div className="event-row" key={event.sequence} data-kind={event.kind}>
      <time dateTime={event.time}>{new Date(event.time).toLocaleTimeString("ru-RU")}</time><span>{event.message}</span>
    </div>)}
  </div>;
}

function DirectConversation({ peerId, active, userId, revision, send }: {
  peerId: string; active: boolean; userId: string; revision: string; send: Send;
}) {
  const target = useMemo<ChatTarget>(() => ({ userId: peerId }), [peerId]);
  return <div className="chat-conversation" hidden={!active}>
    <ChatPanel active={active} target={target} userId={userId} revision={revision} send={send} />
  </div>;
}
