import { useCallback, useEffect, useRef, useState } from "react";
import { APIError, currentSession } from "./http";
import type { ChatPage, ClientEvent, Send, ServerEvent, Snapshot, WebSession } from "./types";

type Pending = {
  resolve: (page?: ChatPage) => void;
  reject: (error: Error) => void;
  timer: number;
};

export function useGateway(session: WebSession, expired: () => void) {
  const [view, setView] = useState<Snapshot>();
  const [events, setEvents] = useState<ClientEvent[]>([]);
  const [socketState, setSocketState] = useState<"connecting" | "online" | "retrying" | "duplicate">("connecting");
  const [connection, setConnection] = useState(0);
  const socket = useRef<WebSocket>();
  const pending = useRef(new Map<string, Pending>());

  useEffect(() => {
    const lifetime = new AbortController();
    let retry = 0;
    let retryTimer = 0;
    let openingTimer = 0;
    const timers = pending.current;
    const failPending = () => {
      for (const request of timers.values()) {
        window.clearTimeout(request.timer);
        request.reject(new Error("Связь со шлюзом потеряна"));
      }
      timers.clear();
    };

    const reconnect = async () => {
      try {
        await currentSession(AbortSignal.any([lifetime.signal, AbortSignal.timeout(5000)]));
      } catch (error) {
        if (error instanceof APIError && error.status === 401) {
          if (!lifetime.signal.aborted) expired();
          return;
        }
      }
      if (!lifetime.signal.aborted) open();
    };

    const open = () => {
      const url = new URL("/api/events", window.location.href);
      url.protocol = "wss:";
      const ws = new WebSocket(url);
      socket.current = ws;
      openingTimer = window.setTimeout(() => ws.close(), 6000);
      ws.onmessage = ({ data }: MessageEvent<string>) => {
        const event = JSON.parse(data) as ServerEvent;
        if (event.type === "result") {
          const request = timers.get(event.id);
          if (!request) return;
          timers.delete(event.id);
          window.clearTimeout(request.timer);
          if (event.ok) request.resolve(event.page);
          else request.reject(new Error(event.error ?? "Команда отклонена"));
          return;
        }
        if (event.type === "events") {
          setEvents((current) => [...current, ...event.events].slice(-200));
          return;
        }
        if (event.type === "snapshot") {
          window.clearTimeout(openingTimer);
          retry = 0;
          setEvents([]);
          setConnection((value) => value + 1);
          setSocketState("online");
        }
        setView(event);
      };
      ws.onclose = ({ code }) => {
        if (socket.current !== ws || lifetime.signal.aborted) return;
        socket.current = undefined;
        window.clearTimeout(openingTimer);
        failPending();
        setSocketState(code === 4409 ? "duplicate" : "retrying");
        setView(undefined);
        retryTimer = window.setTimeout(() => void reconnect(), Math.min(750 * 2 ** retry++, 4000));
      };
    };
    open();
    return () => {
      lifetime.abort();
      window.clearTimeout(retryTimer);
      window.clearTimeout(openingTimer);
      const ws = socket.current;
      socket.current = undefined;
      ws?.close();
      failPending();
    };
  }, [session, expired]);

  const send = useCallback<Send>((command) => {
    const ws = socket.current;
    if (!ws || ws.readyState !== WebSocket.OPEN) return Promise.reject(new Error("Связь со шлюзом потеряна"));
    const id = crypto.randomUUID();
    return new Promise((resolve, reject) => {
      const timer = window.setTimeout(() => {
        pending.current.delete(id);
        reject(new Error("Команда не получила ответа"));
      }, 20000);
      try {
        ws.send(JSON.stringify({ id, ...command }));
        pending.current.set(id, { resolve, reject, timer });
      } catch (error) {
        window.clearTimeout(timer);
        reject(error);
      }
    });
  }, []);

  return { view, events, socketState, connection, send };
}
