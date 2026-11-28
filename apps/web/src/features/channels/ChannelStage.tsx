import { useState, type ReactElement } from "react";
import type { Channel, Participant } from "../../api/types";
import { messageFor, post, request } from "../../api/http";
import { Icon } from "../../components/Icon";
import { AudioRange } from "../settings/AudioSettings";

export function Avatar({ name, speaking = false }: { name: string; speaking?: boolean }) {
  const initial = Array.from(name.trim())[0]?.toLocaleUpperCase() || "•";
  const colour = (initial.codePointAt(0) ?? 0) % 6;
  return <span className={speaking ? "avatar speaking" : "avatar"} data-colour={colour} aria-hidden="true">{initial}</span>;
}

type Moderation = { canKick: boolean; canBan: boolean; canDrag: boolean };

export function ChannelStage({ channel, members, screens, sessionId, userId, onMessage, onWake, moderation, channels, onModerate }: {
  channel: Channel; members: Participant[]; sessionId: string; userId: string; onMessage: (person: Participant) => void;
  screens: ReactElement[];
  onWake: (participantId: string) => void;
  moderation: Moderation; channels: Channel[];
  onModerate: (action: "kick" | "ban" | "move", participantId: string, channelId?: string) => void;
}) {
  const speaking = members.filter((person) => person.speaking && !person.muted && !person.deafened && !person.away);
  const tileCount = members.length + screens.length;
  return <>
    <header className="stage-heading">
      <span className="channel-heading-icon"><Icon name="voice" /></span>
      <div><h2>{channel.name}</h2><p>{channel.topic || "Голосовой канал"}</p></div>
      <span className="member-stack" aria-label="Участники">
        {members.slice(0, 4).map((person) => <Avatar key={person.id} name={person.name} />)}
        {members.length > 4 && <small>+{members.length - 4}</small>}
      </span>
    </header>
    <div className="participants-stage" aria-label="Участники канала">
      <div className="stage-badges">
        <span className={speaking.length ? "stage-speaking" : ""}><Icon name="voice" />
          {speaking.length ? speaking.map((person) => person.name).join(", ") : "Сейчас тихо"}</span>
        <span><Icon name="users" />{members.length}</span>
      </div>
      {tileCount > 0 ? <ul className="member-grid" data-single={tileCount === 1}>
        {members.map((person) => <li key={person.id} data-speaking={speaking.includes(person)}>
          <Avatar name={person.name} speaking={speaking.includes(person)} />
          <strong title={person.name}>{person.name}</strong>
          {person.id === sessionId && <span className="self-label">Вы</span>}
          {person.away ? <span title="Отошёл"><Icon name="away" /></span>
            : person.deafened ? <span title="Звук выключен"><Icon name="headphones" /></span>
            : person.muted && <span title="Микрофон выключен"><Icon name="mic" /></span>}
          {person.userId !== userId && person.userId !== "0" &&
            <div className="stage-actions">
              <button type="button" title={"Написать " + person.name} aria-label={"Написать " + person.name}
                onClick={() => onMessage(person)}><Icon name="chat" /></button>
              <button type="button" title={"Позвать " + person.name} aria-label={"Позвать " + person.name}
                onClick={() => onWake(person.id)}><Icon name="bell" /></button>
              <ParticipantVolume participant={person} />
              {(moderation.canKick || moderation.canBan || moderation.canDrag) && <details className="participant-moderation">
                <summary aria-label={"Управление · " + person.name} title="Управление участником"><Icon name="shield" /></summary>
                <div className="participant-volume-menu">
                  {moderation.canKick && <button type="button" onClick={() => {
                    if (window.confirm("Отключить " + person.name + "?")) onModerate("kick", person.id);
                  }}>Отключить</button>}
                  {moderation.canBan && <button type="button" onClick={() => {
                    if (window.confirm("Заблокировать " + person.name + "?")) onModerate("ban", person.id);
                  }}>Заблокировать</button>}
                  {moderation.canDrag && <label>Переместить<select value="" onChange={(event) => {
                    if (event.target.value) onModerate("move", person.id, event.target.value);
                  }}><option value="">Выбери канал</option>
                    {channels.filter((item) => item.id !== person.channelId).map((item) => <option value={item.id} key={item.id}>{item.name}</option>)}
                  </select></label>}
                </div>
              </details>}
            </div>}
        </li>)}
        {screens}
      </ul> : <p className="empty stage-empty">В канале пока никого нет.</p>}
    </div>
  </>;
}

function ParticipantVolume({ participant }: { participant: Participant }) {
  const [open, setOpen] = useState(false);
  const [volume, setVolume] = useState<number>();
  const [error, setError] = useState("");
  const path = "/api/audio/participants/" + participant.id;
  async function toggle() {
    setOpen(!open);
    if (!open) {
      setError("");
      try { setVolume((await request<{ volume: number }>(path)).volume); }
      catch (reason) { setError(messageFor(reason)); }
    }
  }
  async function apply(value: number) {
    setError("");
    try { setVolume((await post<{ volume: number }>(path, { volume: value })).volume); }
    catch (reason) { setError(messageFor(reason)); }
  }
  return <div className="participant-volume">
    <button type="button" aria-label={"Громкость · " + participant.name} title="Громкость участника"
      aria-expanded={open} onClick={() => void toggle()}><Icon name="headphones" /></button>
    {open && <div className="participant-volume-menu">
      {error && <p className="notice">{error}</p>}
      {volume !== undefined && <label>Громкость<AudioRange value={volume} max={9} apply={(value) => void apply(value)} /></label>}
    </div>}
  </div>;
}
