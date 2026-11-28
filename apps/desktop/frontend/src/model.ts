import type {ChannelDTO, ClientEventDTO, ParticipantDTO} from "./api";

export function canMoveParticipant(participant: ParticipantDTO | undefined, channelID: string, canDrag: boolean): boolean {
  return canDrag && !!participant && participant.channelId !== channelID;
}

function compareID(left: string, right: string): number {
  const a = BigInt(left);
  const b = BigInt(right);
  return a < b ? -1 : a > b ? 1 : 0;
}

export function buildChannelGroups(channels: ChannelDTO[]): Map<string, ChannelDTO[]> {
  const result = new Map<string, ChannelDTO[]>();
  const known = new Set(channels.map((channel) => channel.id));
  for (const channel of channels) {
    const parent = channel.parentId !== "0" && known.has(channel.parentId) ? channel.parentId : "0";
    const group = result.get(parent) ?? [];
    group.push(channel);
    result.set(parent, group);
  }
  for (const group of result.values()) {
    group.sort((left, right) => left.position - right.position || compareID(left.id, right.id));
  }
  return result;
}

export function mergeEventTail(current: ClientEventDTO[], next: ClientEventDTO[]): ClientEventDTO[] {
  const bySequence = new Map<string, ClientEventDTO>();
  for (const event of [...current, ...next]) bySequence.set(event.sequence, event);
  return [...bySequence.values()]
    .sort((left, right) => compareID(left.sequence, right.sequence))
    .slice(-200);
}
