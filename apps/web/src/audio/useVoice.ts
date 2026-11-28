import { useEffect, useRef, useState } from "react";
import { answerOffer, messageFor } from "../api/http";
import { waitForICE } from "../api/webrtc";
import type { Send } from "../api/types";

type VoiceStatus = "waiting" | "connecting" | "connected" | "offline";
type MicStatus = "off" | "requesting" | "ready" | "unavailable";

export function useVoice(online: boolean, connection: number, iceServers: RTCIceServer[], muted: boolean, deafened: boolean, send: Send,
  captureDevice: string, playbackDevice: string) {
  const audio = useRef<HTMLAudioElement>(null);
  const negotiatedTrack = useRef<MediaStreamTrack>();
  const microphone = useRef<MediaStreamTrack>();
  const lifetime = useRef(true);
  const busy = useRef(false);
  const selectedCapture = useRef(captureDevice);
  const [status, setStatus] = useState<VoiceStatus>("waiting");
  const [mic, setMic] = useState<MicStatus>("off");
  const [micBusy, setMicBusy] = useState(false);
  const [error, setError] = useState("");
  const [deviceNote, setDeviceNote] = useState("");
  const [playBlocked, setPlayBlocked] = useState(false);
  const [retry, setRetry] = useState(0);

  useEffect(() => {
    lifetime.current = true;
    return () => {
      lifetime.current = false;
      microphone.current?.stop();
      microphone.current = undefined;
    };
  }, []);

  useEffect(() => {
    if (microphone.current) microphone.current.enabled = !muted;
  }, [muted]);

  useEffect(() => {
    if (selectedCapture.current === captureDevice) return;
    selectedCapture.current = captureDevice;
    if (!microphone.current) return;
    microphone.current.stop();
    microphone.current = undefined;
    setMic("off");
    void send({ type: "mute", value: true }).catch(() => undefined);
  }, [captureDevice, send]);

  useEffect(() => {
    const element = audio.current;
    if (!element?.setSinkId) return;
    let active = true;
    void element.setSinkId(playbackDevice).catch(() => {
      if (active) setDeviceNote("Браузер не разрешил выбрать это устройство вывода");
    });
    return () => { active = false; };
  }, [playbackDevice]);

  useEffect(() => {
    if (!online) {
      setStatus("waiting");
      setError("");
      return;
    }
    setStatus("connecting");
    setError("");
    setPlayBlocked(false);
    const controller = new AbortController();
    let timeout = 0;
    let peer: RTCPeerConnection | undefined;
    let retryTimer = 0;
    let timeoutMessage = "Браузер не успел подготовить голосовое соединение";

    const negotiate = async () => {
      peer = new RTCPeerConnection({ iceServers });
      const current = peer;
      const transceiver = current.addTransceiver("audio", { direction: "sendrecv" });
      const opus = RTCRtpSender.getCapabilities("audio")?.codecs.filter((codec) => codec.mimeType.toLowerCase() === "audio/opus");
      if (opus?.length) transceiver.setCodecPreferences(opus);
      const track = microphone.current;
      if (track) await transceiver.sender.replaceTrack(track);
      if (controller.signal.aborted) return;
      negotiatedTrack.current = track;
      current.ontrack = ({ track, streams }) => {
        const element = audio.current;
        if (!element || controller.signal.aborted) return;
        element.srcObject = streams[0] ?? new MediaStream([track]);
        void element.play().catch(() => {
          if (!controller.signal.aborted) setPlayBlocked(true);
        });
      };
      current.onconnectionstatechange = () => {
        if (controller.signal.aborted) return;
        window.clearTimeout(retryTimer);
        if (current.connectionState === "connected") {
          window.clearTimeout(timeout);
          setStatus("connected");
        } else if (current.connectionState === "failed" || current.connectionState === "disconnected") {
          setStatus("offline");
          retryTimer = window.setTimeout(() => setRetry((value) => value + 1), 2500);
        }
      };
      const offer = await current.createOffer();
      await current.setLocalDescription(offer);
      await waitForICE(current, AbortSignal.any([controller.signal, AbortSignal.timeout(8000)]));
      timeoutMessage = "Шлюз не ответил на запрос подключения голоса";
      const answer = await answerOffer(current.localDescription!.toJSON(),
        AbortSignal.any([controller.signal, AbortSignal.timeout(11000)]));
      if (controller.signal.aborted) return;
      await current.setRemoteDescription(answer);
      timeout = window.setTimeout(() => {
        if (current.connectionState !== "connected") {
          setStatus("offline");
          setError("Голосовое соединение не установилось");
          setRetry((value) => value + 1);
        }
      }, 12000);
    };
    void negotiate().catch((reason) => {
      if (controller.signal.aborted) return;
      peer?.close();
      negotiatedTrack.current = undefined;
      setStatus("offline");
      setError(reason instanceof DOMException && reason.name === "TimeoutError" ? timeoutMessage : messageFor(reason));
      retryTimer = window.setTimeout(() => setRetry((value) => value + 1), 5000);
    });

    return () => {
      controller.abort();
      window.clearTimeout(timeout);
      window.clearTimeout(retryTimer);
      negotiatedTrack.current = undefined;
      peer?.close();
      if (audio.current) audio.current.srcObject = null;
    };
  }, [online, connection, iceServers, retry]);

  async function toggleMicrophone() {
    if (busy.current) return;
    busy.current = true;
    setMicBusy(true);
    setDeviceNote("");
    try {
      const alreadyAcquired = Boolean(microphone.current);
      if (!microphone.current) {
        setMic("requesting");
        try {
          const stream = await navigator.mediaDevices.getUserMedia({
            audio: { echoCancellation: true, noiseSuppression: true, channelCount: 1, ...(captureDevice ? { deviceId: { exact: captureDevice } } : {}) },
          });
          const track = stream.getAudioTracks()[0];
          if (!lifetime.current || !track || selectedCapture.current !== captureDevice) {
            stream.getTracks().forEach((item) => item.stop());
            if (lifetime.current) setMic(track ? "off" : "unavailable");
            return;
          }
          track.enabled = false;
          microphone.current = track;
          track.onended = () => {
            microphone.current = undefined;
            setMic("unavailable");
            setDeviceNote("Микрофон отключён. Прослушивание и чат доступны");
            void send({ type: "mute", value: true }).catch(() => undefined);
          };
          setMic("ready");
        } catch {
          if (lifetime.current) {
            setMic("unavailable");
            setDeviceNote("Нет доступа к микрофону. Прослушивание и чат доступны");
          }
          return;
        }
      }
      const track = microphone.current;
      if (!track) return;
      const value = alreadyAcquired && track.enabled;
      if (value) track.enabled = false;
      await send({ type: "mute", value });
      if (!lifetime.current || microphone.current !== track) return;
      track.enabled = !value;
      // Signal the new sender's SSRC explicitly after a listen-only offer.
      if (!value && negotiatedTrack.current !== track) setRetry((version) => version + 1);
    } catch (reason) {
      if (lifetime.current) setDeviceNote(messageFor(reason));
    } finally {
      busy.current = false;
      if (lifetime.current) setMicBusy(false);
    }
  }

  async function play() {
    try {
      await audio.current?.play();
      setPlayBlocked(false);
    } catch {
      setPlayBlocked(true);
    }
  }

  return { audio, status, mic, micBusy, error, deviceNote, playBlocked, deafened, toggleMicrophone, play, retry: () => setRetry((value) => value + 1) };
}
