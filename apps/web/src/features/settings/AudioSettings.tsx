import { useEffect, useRef, useState } from "react";
import { messageFor, post, request } from "../../api/http";
import { Icon } from "../../components/Icon";
import { applyAppearance, fontSizes, readAppearance, themes } from "./appearance";
import { notificationsEnabled, setNotifications } from "../chat/notifications";

interface AudioConfig {
  gain: number;
  noise: boolean;
  noiseSensitivity: number;
  vad: boolean;
  vadMode: string;
  vadSensitivity: number;
  soundVolume: number;
  muteOnAway: boolean;
  deafenOnAway: boolean;
}

type Section = "microphone" | "playback" | "interface" | "behavior";
type Change = { name: keyof AudioConfig } & ({ number: number } | { enabled: boolean } | { choice: string });

export function AudioSettings({ close, capture, playback, onCapture, onPlayback }: {
  close: () => void;
  capture: string;
  playback: string;
  onCapture: (id: string) => void;
  onPlayback: (id: string) => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [settings, setSettings] = useState<AudioConfig>();
  const [devices, setDevices] = useState<MediaDeviceInfo[]>([]);
  const [section, setSection] = useState<Section>("microphone");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const [appearance, setAppearance] = useState(readAppearance);
  const [notify, setNotify] = useState(notificationsEnabled);

  useEffect(() => {
    dialog.current?.showModal();
    const controller = new AbortController();
    void request<AudioConfig>("/api/audio", { signal: controller.signal })
      .then(setSettings).catch((reason) => { if (!controller.signal.aborted) setError(messageFor(reason)); });
    return () => controller.abort();
  }, []);

  useEffect(() => {
    const media = navigator.mediaDevices;
    if (!media) return;
    let active = true;
    const refresh = () => void media.enumerateDevices()
      .then((list) => { if (active) setDevices(list); }).catch(() => undefined);
    refresh();
    media.addEventListener("devicechange", refresh);
    return () => { active = false; media.removeEventListener("devicechange", refresh); };
  }, []);

  async function apply(change: Change) {
    setPending(true);
    setError("");
    try {
      await post<void>("/api/audio", change);
      const value = "enabled" in change ? change.enabled : "choice" in change ? change.choice : change.number;
      setSettings((current) => current ? { ...current, [change.name]: value } : current);
    } catch (reason) {
      setError(messageFor(reason));
    } finally {
      setPending(false);
    }
  }

  const toggle = (name: "noise" | "vad" | "muteOnAway" | "deafenOnAway", label: string) =>
    <label className="settings-toggle"><input type="checkbox" checked={!!settings?.[name]} disabled={pending}
      onChange={(event) => void apply({ name, enabled:event.target.checked })} />{label}</label>;
  const slider = (name: "gain" | "noiseSensitivity" | "vadSensitivity" | "soundVolume", label: string, max = 1, disabled = false) =>
    <label>{label}<AudioRange value={settings?.[name] ?? 0} max={max} disabled={pending || disabled}
      apply={(number) => void apply({ name, number })} /></label>;
  const deviceOptions = (kind: MediaDeviceKind) => devices.filter((device) => device.kind === kind && device.deviceId !== "default" && device.deviceId)
    .map((device, index) => <option key={device.deviceId} value={device.deviceId}>{device.label || ("Устройство " + (index + 1))}</option>);

  return <dialog ref={dialog} className="settings-dialog" aria-label="Настройки"
    onCancel={(event) => { event.preventDefault(); close(); }}>
    <header><h2>Настройки</h2><button type="button" aria-label="Закрыть настройки" onClick={close}>×</button></header>
    <div className="settings-layout">
      <nav aria-label="Разделы настроек">
        <button type="button" aria-pressed={section === "microphone"} onClick={() => setSection("microphone")}><Icon name="mic" />Микрофон</button>
        <button type="button" aria-pressed={section === "playback"} onClick={() => setSection("playback")}><Icon name="headphones" />Звук</button>
        <button type="button" aria-pressed={section === "interface"} onClick={() => setSection("interface")}><Icon name="screen" />Интерфейс</button>
        <button type="button" aria-pressed={section === "behavior"} onClick={() => setSection("behavior")}><Icon name="away" />Поведение</button>
      </nav>
      <div className="settings-content">
        {error && <p className="notice" role="status">{error}</p>}
        {!settings && !error && <p className="empty">Читаем настройки…</p>}
        {section === "microphone" && <>
          <label>Устройство ввода<select value={capture} onChange={(event) => onCapture(event.target.value)}><option value="">По умолчанию</option>{deviceOptions("audioinput")}</select></label>
          {settings && <>
            {slider("gain", "Усиление микрофона", 2)}
            {toggle("noise", "Шумоподавление")}
            {slider("noiseSensitivity", "Чувствительность шумоподавления", 1, !settings.noise)}
            {toggle("vad", "Активация голосом")}
            <label>Режим активации<select value={settings.vadMode} disabled={!settings.vad || pending}
              onChange={(event) => void apply({ name:"vadMode", choice:event.target.value })}>
              <option value="hybrid">Уровень и голос</option><option value="level">Уровень сигнала</option><option value="vad">Голос</option>
            </select></label>
            {slider("vadSensitivity", "Чувствительность активации", 1, !settings.vad)}
          </>}
        </>}
        {section === "playback" && <>
          <label>Устройство вывода<select value={playback} onChange={(event) => onPlayback(event.target.value)}><option value="">По умолчанию</option>{deviceOptions("audiooutput")}</select></label>
          {settings && slider("soundVolume", "Громкость звуков Sonoryx")}
        </>}
        {section === "interface" && <>
          <label>Цветовая тема<select value={appearance.theme} onChange={(event) => {
            const value = { ...appearance, theme:event.target.value };
            applyAppearance(value);
            setAppearance(value);
          }}>{themes.map(([id, label]) => <option key={id} value={id}>{label}</option>)}</select></label>
          <label>Размер текста<select value={appearance.fontSize} onChange={(event) => {
            const value = { ...appearance, fontSize:Number(event.target.value) };
            applyAppearance(value);
            setAppearance(value);
          }}>{fontSizes.map((size) => <option key={size} value={size}>{size}</option>)}</select></label>
        </>}
        {section === "behavior" && settings && <>
          {toggle("muteOnAway", "Отключать микрофон, когда я отошёл")}
          {toggle("deafenOnAway", "Отключать звук, когда я отошёл")}
          <label className="settings-toggle"><input type="checkbox" checked={notify} disabled={typeof Notification === "undefined"}
            onChange={(event) => {
              const checked = event.target.checked;
              void setNotifications(checked).then((value) => {
                setNotify(value);
                if (checked && !value) setError("Браузер не разрешил уведомления");
              }).catch((reason) => setError(messageFor(reason)));
            }} />Уведомления о сообщениях, пока вкладка открыта в фоне</label>
          <p className="empty">В активной вкладке Ctrl+Alt+M управляет микрофоном, Ctrl+Alt+D — звуком, Ctrl+Alt+E — статусом.</p>
        </>}
      </div>
    </div>
  </dialog>;
}

export function AudioRange({ value, max, disabled, apply }: {
  value: number; max: number; disabled?: boolean; apply: (value: number) => void;
}) {
  const [draft, setDraft] = useState(value);
  useEffect(() => setDraft(value), [value]);
  const commit = () => { if (draft !== value) apply(draft); };
  return <span className="settings-range"><input type="range" min="0" max={max} step="0.01" value={draft} disabled={disabled}
    onChange={(event) => setDraft(event.target.valueAsNumber)} onPointerUp={commit}
    onKeyUp={(event) => { if (["ArrowLeft","ArrowRight","ArrowUp","ArrowDown","Home","End"].includes(event.key)) commit(); }} />
    <output>{Math.round(draft * 100)}%</output>
  </span>;
}
