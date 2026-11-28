import { useEffect, useRef } from "react";
import { Icon } from "../../components/Icon";

interface ScreenTileProps {
  owner: string;
  own: boolean;
  advertised: boolean;
  canWatch: boolean;
  watching: boolean;
  preview?: MediaStream;
  onWatch?: () => void;
  onClose?: () => void;
}

export function ScreenTile({ owner, own, advertised, canWatch, watching, preview, onWatch, onClose }: ScreenTileProps) {
  const title = own ? "Твоя демонстрация" : "Демонстрация · " + owner;
  return <li className="stage-screen-tile" data-watching={watching} aria-label={title}>
    {preview ? <ScreenVideo stream={preview} own={own} title={title} /> : <span className="stage-screen-icon"><Icon name="screen" /></span>}
    <strong title={title}>{title}</strong>
    {own ? <span className="stage-screen-state">{advertised ? "Ты показываешь экран" : "Подключение трансляции…"}</span>
      : watching ? <>
          {!preview && <span className="stage-screen-state" role="status">Подключение к демонстрации…</span>}
          <button type="button" onClick={onClose}>Закрыть просмотр</button>
        </> : canWatch ? <button type="button" onClick={onWatch}>Смотреть</button>
          : <span className="stage-screen-state">Просмотр недоступен</span>}
  </li>;
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
