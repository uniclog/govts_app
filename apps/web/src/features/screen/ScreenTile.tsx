import { useEffect, useRef, useState } from "react";
import { Icon } from "../../components/Icon";

interface ScreenTileProps {
  owner: string;
  own: boolean;
  advertised: boolean;
  canWatch: boolean;
  preview?: MediaStream;
  onWatch?: () => void;
}

export function ScreenTile({ owner, own, advertised, canWatch, preview, onWatch }: ScreenTileProps) {
  const title = own ? "Твоя демонстрация" : "Демонстрация · " + owner;
  return <li className="stage-screen-tile" aria-label={title}>
    {preview ? <ScreenVideo stream={preview} own={own} title={title} /> : <span className="stage-screen-icon"><Icon name="screen" /></span>}
    <strong title={title}>{title}</strong>
    {own ? <span className="stage-screen-state">{advertised ? "Ты показываешь экран" : "Подключение трансляции…"}</span>
      : canWatch ? <button type="button" onClick={onWatch}>Смотреть</button>
          : <span className="stage-screen-state">Просмотр недоступен</span>}
  </li>;
}

export function ScreenViewer({ current, streams, preview, onWatch, onClose }: {
  current: { id: string; owner: string };
  streams: { id: string; owner: string }[];
  preview?: MediaStream;
  onWatch: (id: string) => void;
  onClose: () => void;
}) {
  const panel = useRef<HTMLElement>(null);
  const [fullscreen, setFullscreen] = useState(false);
  const [fullscreenError, setFullscreenError] = useState("");
  const title = "Демонстрация · " + current.owner;

  useEffect(() => {
    const changed = () => setFullscreen(!!panel.current && panel.current === document.fullscreenElement);
    document.addEventListener("fullscreenchange", changed);
    return () => document.removeEventListener("fullscreenchange", changed);
  }, []);

  async function toggleFullscreen() {
    const element = panel.current;
    if (!element) return;
    setFullscreenError("");
    try {
      if (document.fullscreenElement === element) await document.exitFullscreen();
      else await element.requestFullscreen();
    } catch {
      setFullscreenError("Не удалось переключить полноэкранный режим");
    }
  }

  const fullscreenLabel = fullscreen ? "Выйти из полноэкранного режима" : "На весь экран";
  return <section className="screen-viewer" ref={panel} aria-label={title}>
    <header className="screen-viewer-header">
      <strong title={title}>{title}</strong>
      {streams.length > 1 && <select value={current.id} title="Выбрать трансляцию" aria-label="Выбрать трансляцию"
        onChange={(event) => onWatch(event.target.value)}>
        {streams.map(({ id, owner }) => <option key={id} value={id}>{owner}</option>)}
      </select>}
      {document.fullscreenEnabled && <button type="button" aria-label={fullscreenLabel} title={fullscreenLabel}
        onClick={() => void toggleFullscreen()}><Icon name={fullscreen ? "minimize" : "maximize"} /></button>}
      <button type="button" aria-label="Закрыть просмотр" title="Закрыть просмотр" onClick={onClose}><Icon name="close" /></button>
    </header>
    <div className="screen-viewer-media">
      {preview ? <ScreenVideo stream={preview} own={false} title={title} />
        : <div className="screen-viewer-loading" role="status"><Icon name="screen" />Подключение к демонстрации…</div>}
      {fullscreenError && <p className="notice screen-viewer-notice" role="status">{fullscreenError}</p>}
    </div>
  </section>;
}

function ScreenVideo({ stream, own, title }: { stream: MediaStream; own: boolean; title: string }) {
  const player = useRef<HTMLVideoElement>(null);
  useEffect(() => {
    const video = player.current;
    if (!video) return;
    video.srcObject = stream;
    void video.play().catch(() => undefined);
    return () => { video.srcObject = null; };
  }, [stream]);
  return <video ref={player} aria-label={title} controls={!own} autoPlay muted={own} playsInline />;
}
