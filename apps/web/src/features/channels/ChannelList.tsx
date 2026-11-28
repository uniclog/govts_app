import type { Channel, Participant } from "../../api/types";
import { useState } from "react";
import { Icon } from "../../components/Icon";
import { Avatar } from "./ChannelStage";

function ChannelRow({ channel, current, people, enter, busy }: {
  channel: Channel;
  current: boolean;
  people: Participant[];
  enter: (id: string) => void;
  busy: boolean;
}) {
  return (
    <div className="channel-row">
      <button className="channel" type="button" aria-current={current ? "true" : undefined} title={channel.canJoin ? channel.topic || channel.name : "Нет доступа к каналу"}
        disabled={busy || current || !channel.canJoin} onClick={() => enter(channel.id)}>
        <Icon name={channel.canJoin ? "voice" : "lock"} />
        <span>{channel.name}</span><small>{people.length}</small>
      </button>
      {people.length > 0 && <ul className="participants">
        {people.map((person) => <li key={person.id}>
          <Avatar name={person.name} speaking={person.speaking && !person.muted && !person.deafened} />
          <span>{person.name}</span>
          {(person.deafened || person.muted || person.speaking) && <small>{person.deafened ? "Не слушает" : person.muted ? "Без микр." : "Говорит"}</small>}
        </li>)}
      </ul>}
    </div>
  );
}

export function ChannelList({ channels, participants, current, enter, busy }: {
  channels: Channel[];
  participants: Participant[];
  current: string;
  enter: (id: string) => void;
  busy: boolean;
}) {
  const [query, setQuery] = useState("");
  const search = query.trim().toLocaleLowerCase();
  const children = new Map<string, Channel[]>();
  const people = new Map<string, Participant[]>();
  const channelsByID = new Map(channels.map((channel) => [channel.id, channel]));
  for (const channel of channels) {
    const parent = channelsByID.has(channel.parentId) ? channel.parentId : "0";
    const group = children.get(parent) ?? [];
    group.push(channel);
    children.set(parent, group);
  }
  for (const person of participants) {
    const group = people.get(person.channelId) ?? [];
    group.push(person);
    people.set(person.channelId, group);
  }
  const visible = new Set<string>();
  if (search) {
    for (const channel of channels) {
      if (!channel.name.toLocaleLowerCase().includes(search) &&
          !people.get(channel.id)?.some(({ name }) => name.toLocaleLowerCase().includes(search))) continue;
      let ancestor: Channel | undefined = channel;
      while (ancestor && !visible.has(ancestor.id)) {
        visible.add(ancestor.id);
        ancestor = channelsByID.get(ancestor.parentId);
      }
    }
  }
  const seen = new Set<string>();
  const renderGroup = (parent: string): React.ReactNode => children.get(parent)
    ?.sort((a, b) => a.position - b.position || a.name.localeCompare(b.name))
    .filter(({ id }) => !seen.has(id) && (!search || visible.has(id))).map((channel) => {
      seen.add(channel.id);
      return <li key={channel.id}>
        <ChannelRow channel={channel} current={channel.id === current} people={people.get(channel.id) ?? []} enter={enter} busy={busy} />
        {children.has(channel.id) && <ul>{renderGroup(channel.id)}</ul>}
      </li>;
    });

  return (
    <nav className="channels" aria-label="Каналы сервера">
      <label className="channel-search"><Icon name="search" />
        <input aria-label="Поиск по каналам и участникам" placeholder="Поиск" value={query} onChange={(event) => setQuery(event.target.value)} />
      </label>
      <h2>Голосовые каналы</h2>
      <div className="channel-scroll">
        {channels.length === 0 ? <p className="empty">Подключение к серверу…</p>
          : search && !visible.size ? <p className="empty">Ничего не найдено.</p> : <ul>{renderGroup("0")}</ul>}
      </div>
    </nav>
  );
}
