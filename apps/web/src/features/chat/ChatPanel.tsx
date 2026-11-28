import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { messageFor } from "../../api/http";
import type { ChatMessage, ChatTarget, Send } from "../../api/types";
import { Icon } from "../../components/Icon";
import { Avatar } from "../channels/ChannelStage";
import { useUploads } from "./useUploads";
import { Attachment } from "./Attachment";
import { notifyWhenHidden } from "./notifications";

const timestamp = new Intl.DateTimeFormat("ru", { dateStyle: "short", timeStyle: "short" });
const bytes = new TextEncoder();
const reactions = ["👍", "❤️", "😮", "😂", "😢", "🔥", "👏", "🎉"];

function merge(current: ChatMessage[], incoming: ChatMessage[]) {
  const messages = new Map(current.map((message) => [message.id, message]));
  for (const message of incoming) {
    const previous = messages.get(message.id);
    if (!previous || BigInt(previous.revision) <= BigInt(message.revision)) messages.set(message.id, message);
  }
  return [...messages.values()].sort((a, b) => BigInt(a.id) < BigInt(b.id) ? -1 : 1);
}

interface Props {
  active: boolean;
  target: ChatTarget;
  userId: string;
  revision: string;
  send: Send;
}

export function ChatPanel({ active, target, userId, revision, send }: Props) {
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [history, setHistory] = useState({ cursor: "0", hasMore: false });
  const [loaded, setLoaded] = useState(false);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [draft, setDraft] = useState("");
  const [sending, setSending] = useState(false);
  const [error, setError] = useState("");
  const [pinned, setPinned] = useState<string[]>([]);
  const [pinPanel, setPinPanel] = useState<ChatMessage[]>();
  const [actionMenu, setActionMenu] = useState("");
  const uploads = useUploads(target, active);
  const [loadAttempt, setLoadAttempt] = useState(0);
  const [refreshAttempt, setRefreshAttempt] = useState(0);
  const changeCursor = useRef("0");
  const messageCursor = useRef("0");
  const historyFloor = useRef("0");
  const readCursor = useRef("0");
  const lifetime = useRef<AbortController>();
  const dirty = useRef(false);
  const updating = useRef(false);
  const lastRefresh = useRef(0);
  const pendingSend = useRef<{ key: string; id: string }>();
  const scrollArea = useRef<HTMLDivElement>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const follow = useRef(true);
  const restoreScroll = useRef<{ top: number; height: number }>();

  useEffect(() => {
    const controller = new AbortController();
    lifetime.current = controller;
    setLoaded(false);
    setError("");
    const initial = async () => {
      const watermark = await send({ type: "chat.watermark", ...target });
      if (controller.signal.aborted) return;
      const page = await send({ type: "chat.history", ...target, cursor: "0" });
      if (!page || !watermark || controller.signal.aborted) return;
      changeCursor.current = watermark.cursor;
      messageCursor.current = page.messages.reduce((latest, message) =>
        BigInt(message.id) > BigInt(latest) ? message.id : latest, "0");
      historyFloor.current = page.hasMore ? page.cursor : "0";
      setMessages(page.messages.reverse());
      setHistory(page);
      setLoaded(true);
    };
    void initial().catch((reason) => {
      if (!controller.signal.aborted) setError(messageFor(reason));
    });
    return () => controller.abort();
  }, [send, target, loadAttempt]);

  const refresh = useCallback(async () => {
    const controller = lifetime.current;
    if (!controller || controller.signal.aborted || updating.current) return;
    updating.current = true;
    lastRefresh.current = Date.now();
    try {
      do {
        dirty.current = false;
        let more = false;
        do {
          const cursor = messageCursor.current;
          const page = await send({ type: "chat.history", ...target, cursor, forward: true });
          if (!page || controller.signal.aborted) return;
          if (BigInt(page.cursor) > BigInt(cursor)) messageCursor.current = page.cursor;
          if (page.messages.length) setMessages((current) => merge(current, page.messages));
          const incoming = [...page.messages].reverse().find((message) => !message.deleted && message.senderId !== userId && BigInt(message.id) > BigInt(cursor));
          if (incoming) notifyWhenHidden(incoming.senderName, incoming.text.slice(0, 180) || "Вложение", "sonoryx-chat-" + (target.channelId ?? target.userId));
          more = page.hasMore && cursor !== messageCursor.current;
        } while (more);
        do {
          const cursor = changeCursor.current;
          const page = await send({ type: "chat.changes", ...target, cursor });
          if (!page || controller.signal.aborted) return;
          if (BigInt(page.cursor) > BigInt(cursor)) changeCursor.current = page.cursor;
          if (page.messages.length) setMessages((current) => merge(current, page.messages.filter((message) =>
            BigInt(message.id) >= BigInt(historyFloor.current))));
          more = page.hasMore && cursor !== changeCursor.current;
        } while (more);
      } while (dirty.current && !controller.signal.aborted);
    } catch (reason) {
      if (!controller.signal.aborted) setError(messageFor(reason));
    } finally {
      updating.current = false;
    }
  }, [send, target, userId]);

  useEffect(() => {
    if (!loaded || !active) return;
    let alive = true;
    const getPins = () => send({ type: "chat.pins", ...target, cursor: "0", forward: true })
      .then((page) => { if (alive) setPinned(page?.pinnedIds ?? []); })
      .catch(() => undefined);
    const timer = window.setTimeout(() => void getPins(), 200);
    const poll = window.setInterval(() => void getPins(), 10000);
    return () => { alive = false; window.clearTimeout(timer); window.clearInterval(poll); };
  }, [active, loaded, revision, send, target]);

  useEffect(() => {
    if (!active) return;
    const markRead = () => {
      const latest = messages.at(-1)?.id;
      if (!latest || document.visibilityState !== "visible" || BigInt(latest) <= BigInt(readCursor.current)) return;
      readCursor.current = latest;
      void send({ type: "chat.read", ...target, cursor: latest }).catch(() => {
        if (readCursor.current === latest) readCursor.current = "0";
      });
    };
    markRead();
    document.addEventListener("visibilitychange", markRead);
    return () => document.removeEventListener("visibilitychange", markRead);
  }, [active, messages, send, target]);

  useEffect(() => {
    if (!active || !loaded) return;
    const timer = window.setTimeout(() => {
      dirty.current = true;
      void refresh();
    }, Math.max(0, lastRefresh.current + 2000 - Date.now()));
    return () => window.clearTimeout(timer);
  }, [active, loaded, revision, refreshAttempt, refresh]);

  useEffect(() => {
    if (!active || !loaded) return;
    const clock = window.setInterval(() => {
      dirty.current = true;
      void refresh();
    }, 5000);
    return () => window.clearInterval(clock);
  }, [active, loaded, refresh]);

  useEffect(() => {
    const element = scrollArea.current;
    if (!active || !element) return;
    if (restoreScroll.current) {
      element.scrollTop = restoreScroll.current.top + element.scrollHeight - restoreScroll.current.height;
      restoreScroll.current = undefined;
    } else if (follow.current) {
      element.scrollTop = element.scrollHeight;
    }
  }, [active, messages]);

  async function loadOlder() {
    setLoadingOlder(true);
    setError("");
    try {
      const page = await send({ type: "chat.history", ...target, cursor: history.cursor });
      if (!page) return;
      const element = scrollArea.current;
      if (element) restoreScroll.current = { top: element.scrollTop, height: element.scrollHeight };
      historyFloor.current = page.hasMore ? page.cursor : "0";
      setMessages((current) => merge(current, page.messages));
      setHistory(page);
    } catch (reason) {
      setError(messageFor(reason));
    } finally {
      setLoadingOlder(false);
    }
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const text = draft.trim();
    const attachmentIds = uploads.ready;
    if (sending || (!text && !attachmentIds.length) || attachmentIds.length !== uploads.uploads.length || bytes.encode(text).length > 1000) return;
    const key = JSON.stringify([text, attachmentIds]);
    if (pendingSend.current?.key !== key) pendingSend.current = { key, id: crypto.randomUUID().replaceAll("-", "") };
    const clientId = pendingSend.current.id;
    setSending(true);
    setError("");
    try {
      const page = await send({ type: "chat.send", ...target, text, clientId, attachmentIds });
      if (page) {
        follow.current = true;
        setMessages((current) => merge(current, page.messages));
      }
      setDraft((value) => value.trim() === text ? "" : value);
      uploads.clearSent(attachmentIds);
      pendingSend.current = undefined;
    } catch (reason) {
      setError(messageFor(reason));
    } finally {
      setSending(false);
    }
  }

  async function messageAction(command: Parameters<Send>[0]) {
    setActionMenu("");
    try {
      const page = await send(command);
      if (page?.messages.length) setMessages((current) => merge(current, page.messages));
      if (command.type === "chat.pin") {
        setPinned((current) => command.forward ? [command.cursor, ...current.filter((id) => id !== command.cursor)] : current.filter((id) => id !== command.cursor));
        setPinPanel(undefined);
      }
    } catch (reason) {
      setError(messageFor(reason));
    }
  }

  async function showPins() {
    if (pinPanel) { setPinPanel(undefined); return; }
    try {
      const pins: ChatMessage[] = [];
      let cursor = "0";
      for (;;) {
        const page = await send({ type: "chat.pins", ...target, cursor });
        if (!page) break;
        pins.push(...page.messages);
        if (!page.hasMore || cursor === page.cursor) break;
        cursor = page.cursor;
      }
      setPinPanel(pins);
    } catch (reason) {
      setError(messageFor(reason));
    }
  }

  const tooLong = bytes.encode(draft.trim()).length > 1000;
  return (
    <div className="chat" onDragOver={(event) => {
      if (uploads.enabled && event.dataTransfer.types.includes("Files")) event.preventDefault();
    }} onDrop={(event) => {
      if (!uploads.enabled || !event.dataTransfer.files.length) return;
      event.preventDefault();
      uploads.add(event.dataTransfer.files);
    }}>
      {pinned.length > 0 && <div className="chat-pins">
        <button type="button" className="subtle" aria-expanded={!!pinPanel} onClick={() => void showPins()}>Закреплённые · {pinned.length}</button>
        {pinPanel && <ul>{pinPanel.map((message) => <li key={message.id}>
          <strong>{message.senderName}</strong><span>{message.text || "Вложение"}</span>
        </li>)}</ul>}
      </div>}
      <div className="chat-history" ref={scrollArea} onScroll={(event) => {
        const element = event.currentTarget;
        follow.current = element.scrollHeight - element.scrollTop - element.clientHeight < 48;
      }}>
        {history.hasMore && <button className="subtle history-more" onClick={() => void loadOlder()} disabled={loadingOlder}>Загрузить предыдущие</button>}
        {!loaded && !error && <p className="empty">Загрузка истории…</p>}
        {loaded && messages.length === 0 && <p className="empty">В этом чате пока нет сообщений.</p>}
        <ul className="messages" aria-label="Сообщения">
          {messages.map((message) => (
            <li key={message.id}>
              <Avatar name={message.senderName} />
              <div className="message-content">
                <div className="message-meta"><strong>{message.senderName}</strong><time dateTime={new Date(message.sentAtMs).toISOString()}>{timestamp.format(message.sentAtMs)}</time></div>
                {(message.deleted || message.text) && <p className={message.deleted ? "empty" : undefined}>{message.deleted ? "Сообщение удалено" : message.text}</p>}
                {!message.deleted && message.attachmentIds?.map((id) => <Attachment key={id} id={id} />)}
                {!message.deleted && <>
                  {!!message.reactions?.length && <div className="message-reactions" aria-label="Реакции">{message.reactions.map(({ emoji, count, mine }) =>
                    <button type="button" key={emoji} aria-pressed={mine} onClick={() => void messageAction({ type: "chat.react", ...target, text: emoji, cursor: message.id, forward: !mine })}>{emoji} {count}</button>)}</div>}
                  <button type="button" className="subtle message-menu" aria-label="Действия с сообщением" aria-expanded={actionMenu === message.id}
                    onClick={() => setActionMenu(actionMenu === message.id ? "" : message.id)}>···</button>
                  {actionMenu === message.id && <div className="message-actions">
                    <div className="message-reactions" aria-label="Добавить реакцию">{reactions.map((emoji) => <button type="button" key={emoji}
                      onClick={() => void messageAction({ type: "chat.react", ...target, text: emoji, cursor: message.id, forward: !message.reactions?.some((item) => item.emoji === emoji && item.mine) })}>{emoji}</button>)}</div>
                    <button type="button" onClick={() => void messageAction({ type: "chat.pin", ...target, cursor: message.id, forward: !pinned.includes(message.id) })}>
                      {pinned.includes(message.id) ? "Открепить" : "Закрепить"}
                    </button>
                    {userId === message.senderId && <button type="button" onClick={() => {
                      if (window.confirm("Удалить сообщение?")) void messageAction({ type: "chat.delete", ...target, cursor: message.id });
                    }}>Удалить</button>}
                  </div>}
                </>}
              </div>
            </li>
          ))}
        </ul>
      </div>
      {error && <div className="notice" role="status">
        {error} <button className="subtle" onClick={() => { setError(""); if (loaded) setRefreshAttempt((value) => value + 1); else setLoadAttempt((value) => value + 1); }}>Повторить загрузку</button>
      </div>}
      {(uploads.error || uploads.uploads.length > 0) && <section className="upload-list" aria-label="Вложения">
        {uploads.error && <p className="notice">{uploads.error}</p>}
        {uploads.uploads.map((upload) => <div className="upload-row" key={upload.requestId}>
          <strong title={upload.file.name}>{upload.file.name}</strong>
          <small>{upload.state === "ready" ? "Готово" : upload.state === "hashing" ? "Подготовка" : upload.state === "verifying" ? "Проверка на сервере" :
            upload.state === "paused" ? "Пауза" : upload.state === "error" ? upload.error : Math.floor(100 * upload.offset / Math.max(1, upload.file.size)) + "%"}</small>
          {upload.state === "paused" || upload.state === "error" ?
            <button type="button" onClick={() => void uploads.start(upload)}>Продолжить</button> :
            upload.state !== "ready" && <button type="button" onClick={() => uploads.pause(upload)}>Пауза</button>}
          <button type="button" aria-label={"Убрать " + upload.file.name} title="Убрать вложение" disabled={sending} onClick={() => void uploads.remove(upload)}>×</button>
        </div>)}
      </section>}
      <form className={"composer" + (uploads.enabled ? " with-files" : "")} onSubmit={(event) => void submit(event)}>
        {uploads.enabled && <>
          <input type="file" multiple hidden ref={fileInput} onChange={(event) => {
            if (event.currentTarget.files) uploads.add(event.currentTarget.files);
            event.currentTarget.value = "";
          }} />
          <button type="button" aria-label="Прикрепить файл" title="Прикрепить файл" disabled={sending} onClick={() => fileInput.current?.click()}><Icon name="attachment" /></button>
        </>}
        <textarea value={draft} onChange={(event) => setDraft(event.target.value)} placeholder="Сообщение" rows={2} aria-label="Сообщение"
          onPaste={(event) => {
            if (uploads.enabled && event.clipboardData.files.length) {
              event.preventDefault();
              uploads.add(event.clipboardData.files);
            }
          }}
          onKeyDown={(event) => { if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); event.currentTarget.form?.requestSubmit(); } }} />
        <button className="primary" disabled={(!draft.trim() && !uploads.ready.length) || uploads.ready.length !== uploads.uploads.length || tooLong || sending} type="submit" aria-label="Отправить" title="Отправить"><Icon name="send" /></button>
        {tooLong && <small className="notice">Сообщение превышает 1000 байт</small>}
      </form>
    </div>
  );
}
