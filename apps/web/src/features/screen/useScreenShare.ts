import { useCallback, useEffect, useRef, useState } from "react";
import { APIError, messageFor, post, request } from "../../api/http";
import { localOffer } from "../../api/webrtc";

type MediaCaps = { legacy?: boolean; audio?: unknown[]; codecs?: { mimeType: string }[] };
type MediaAnswer = { answer: RTCSessionDescriptionInit };
type PublishAnswer = MediaAnswer & { streamId: string };
type ActivePeer = { pc: RTCPeerConnection; controller: AbortController };
type Publisher = ActivePeer & { capture: MediaStream; streamId?: string };
type Viewer = ActivePeer & { streamId: string; subscriberId: string };

function end(path: "stop" | "unsubscribe", data: { streamId: string; subscriberId?: string }) {
  return post<void>("/api/media/" + path, data, AbortSignal.timeout(6000)).catch(() => undefined);
}

async function signalMedia<T>(path: "publish" | "subscribe", data: unknown): Promise<T> {
  try {
    return await post<T>("/api/media/" + path, data, AbortSignal.timeout(22000));
  } catch (reason) {
    if (reason instanceof DOMException && reason.name === "TimeoutError") {
      throw new Error("Шлюз не ответил на запрос демонстрации экрана");
    }
    throw reason;
  }
}

function closePeer(handle: ActivePeer) {
  handle.controller.abort();
  handle.pc.close();
}

function preferVideo(transceiver: RTCRtpTransceiver, capabilities: MediaCaps, direction: "send" | "receive") {
  const codecs = (direction === "send" ? RTCRtpSender : RTCRtpReceiver).getCapabilities("video")?.codecs;
  if (!codecs?.length) return;
  const supported = new Set((capabilities.codecs ?? []).map(({ mimeType }) => mimeType.toLowerCase()));
  supported.add("video/vp8");
  const selected = codecs.filter(({ mimeType }) => supported.has(mimeType.toLowerCase()) || mimeType.toLowerCase() === "video/rtx");
  if (selected.length) transceiver.setCodecPreferences(selected);
}

export function useScreenShare(scope: string, iceServers: RTCIceServer[]) {
  const [capabilities, setCapabilities] = useState<MediaCaps>();
  const [local, setLocal] = useState<{ scope: string; id: string; preview: MediaStream }>();
  const [busy, setBusy] = useState(false);
  const [viewerID, setViewerID] = useState("");
  const [remote, setRemote] = useState<MediaStream>();
  const [error, setError] = useState("");
  const publisher = useRef<Publisher>();
  const viewer = useRef<Viewer>();
  const preparing = useRef<AbortController>();

  function detachPublisher() {
    preparing.current?.abort();
    preparing.current = undefined;
    const active = publisher.current;
    publisher.current = undefined;
    if (!active) return;
    closePeer(active);
    active.capture.getTracks().forEach((track) => track.stop());
    if (active.streamId) void end("stop", { streamId: active.streamId });
  }

  const detachViewer = useCallback(() => {
    const active = viewer.current;
    viewer.current = undefined;
    if (!active) return;
    closePeer(active);
    void end("unsubscribe", { streamId: active.streamId, subscriberId: active.subscriberId });
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    setCapabilities(undefined);
    setLocal(undefined);
    setViewerID("");
    setRemote(undefined);
    setError("");
    setBusy(false);
    let retry: number | undefined;
    const loadCapabilities = async (attempt: number) => {
      try {
        const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(10000)]);
        const value = await request<MediaCaps>("/api/media/capabilities", { signal });
        if (!controller.signal.aborted) setCapabilities(value);
      } catch (reason) {
        if (controller.signal.aborted) return;
        if (attempt < 2 && (!(reason instanceof APIError) || reason.status !== 404)) {
          retry = window.setTimeout(() => void loadCapabilities(attempt + 1), 1000);
        }
      }
    };
    if (scope) void loadCapabilities(0);
    return () => {
      controller.abort();
      window.clearTimeout(retry);
      detachPublisher();
      detachViewer();
    };
  }, [scope, detachViewer]);

  function stop() {
    detachPublisher();
    setLocal(undefined);
    setBusy(false);
  }

  async function publish() {
    if (!capabilities || preparing.current || publisher.current) return;
    const controller = new AbortController();
    preparing.current = controller;
    setBusy(true);
    setError("");
    try {
      const capture = await navigator.mediaDevices.getDisplayMedia({ video: true, audio: !!capabilities.audio?.length });
      if (controller.signal.aborted) {
        capture.getTracks().forEach((track) => track.stop());
        return;
      }
      const [video] = capture.getVideoTracks();
      if (!video) {
        capture.getTracks().forEach((track) => track.stop());
        throw new Error("Браузер не предоставил видео экрана");
      }
      let pc: RTCPeerConnection;
      try { pc = new RTCPeerConnection({ iceServers }); }
      catch (reason) { capture.getTracks().forEach((track) => track.stop()); throw reason; }
      const active = { pc, controller, capture } as Publisher;
      publisher.current = active;
      video.addEventListener("ended", () => { if (publisher.current === active) stop(); }, { once: true });
      video.contentHint = "detail";
      const sender = pc.addTransceiver(video, { direction: "sendonly", streams: [capture] });
      preferVideo(sender, capabilities, "send");
      for (const track of capture.getAudioTracks()) pc.addTransceiver(track, { direction: "sendonly", streams: [capture] });
      const offer = await localOffer(pc, controller.signal, "complete");
      const answer = await signalMedia<PublishAnswer>("publish", { offer });
      if (controller.signal.aborted) {
        void end("stop", { streamId: answer.streamId });
        return;
      }
      active.streamId = answer.streamId;
      await pc.setRemoteDescription(answer.answer);
      pc.onconnectionstatechange = () => {
        if (pc.connectionState === "failed" && publisher.current === active) {
          setError("Соединение демонстрации потеряно");
          stop();
        }
      };
      if (!controller.signal.aborted) setLocal({ scope, id: answer.streamId, preview: new MediaStream([video]) });
    } catch (reason) {
      if (!controller.signal.aborted) {
        stop();
        setError(messageFor(reason));
      }
    } finally {
      if (preparing.current === controller) {
        preparing.current = undefined;
        setBusy(false);
      }
    }
  }

  const closeView = useCallback(() => {
    detachViewer();
    setViewerID("");
    setRemote(undefined);
  }, [detachViewer]);

  async function watch(streamId: string) {
    if (!capabilities || viewer.current?.streamId === streamId) return;
    closeView();
    setError("");
    const pc = new RTCPeerConnection({ iceServers });
    const controller = new AbortController();
    const subscriberId = crypto.randomUUID();
    const active = { pc, controller, streamId, subscriberId };
    viewer.current = active;
    setViewerID(streamId);
    try {
      const media = new MediaStream();
      pc.ontrack = ({ track }) => {
        if (viewer.current !== active) return;
        media.addTrack(track);
        setRemote(media);
      };
      preferVideo(pc.addTransceiver("video", { direction: "recvonly" }), capabilities, "receive");
      if (capabilities.audio?.length) pc.addTransceiver("audio", { direction: "recvonly" });
      // Sonoryx ICE-lite learns the viewer's address from the browser's checks.
      // Send the offer while the browser is still gathering its candidates.
      const offer = await localOffer(pc, controller.signal, "background");
      const answer = await signalMedia<MediaAnswer>("subscribe", { streamId, subscriberId, offer });
      if (controller.signal.aborted) {
        void end("unsubscribe", { streamId, subscriberId });
        return;
      }
      await pc.setRemoteDescription(answer.answer);
      if (controller.signal.aborted) return;
      const failView = (message: string) => {
        if (viewer.current !== active) return;
        setError(message);
        closeView();
      };
      const timer = window.setTimeout(() => failView("Не удалось установить медиасоединение с трансляцией"), 20000);
      controller.signal.addEventListener("abort", () => window.clearTimeout(timer), { once: true });
      const onConnectionChange = () => {
        if (pc.connectionState === "connected") window.clearTimeout(timer);
        else if (pc.connectionState === "failed") failView("Соединение с трансляцией потеряно");
      };
      pc.onconnectionstatechange = onConnectionChange;
      onConnectionChange();
    } catch (reason) {
      if (viewer.current === active) {
        setError(messageFor(reason));
        closeView();
      }
    }
  }

  const localScreen = local?.scope === scope ? local : undefined;
  return { enabled: !!capabilities, publishing: !!localScreen, local: localScreen, busy, viewerID, remote, error, publish, stop, watch, closeView };
}
