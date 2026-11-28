export type Status = "connecting" | "connected" | "reconnecting" | "disconnected";

export interface Channel {
  id: string;
  canJoin: boolean;
  parentId: string;
  name: string;
  topic: string;
  position: number;
  maxUsers: number;
  minJoinLevel: number;
  bitrate: number;
  application: "voip" | "audio";
  description: string;
}

export interface Participant {
  id: string;
  userId: string;
  channelId: string;
  name: string;
  muted: boolean;
  deafened: boolean;
  speaking: boolean;
  away: boolean;
}

export interface Snapshot {
  type: "snapshot" | "state";
  status: Status;
  error?: string;
  serverName: string;
  revision: string;
  chatRevision: string;
  sessionId: string;
  userId: string;
  channelId: string;
  muted: boolean;
  deafened: boolean;
  canAdmin: boolean;
  isOwner: boolean;
  canKick: boolean;
  canBan: boolean;
  canDrag: boolean;
  channels: Channel[];
  participants: Participant[];
  screenStreams: ScreenStream[];
}

export interface ScreenStream {
  id: string;
  ownerId: string;
  channelId: string;
}

export interface ChatMessage {
  id: string;
  revision: string;
  senderId: string;
  senderName: string;
  text: string;
  sentAtMs: number;
  deleted: boolean;
  reactions?: { emoji: string; count: number; mine: boolean }[];
  attachmentIds?: string[];
}

export interface ChatPage {
  channelId?: string;
  userId?: string;
  cursor: string;
  hasMore: boolean;
  messages: ChatMessage[];
  dialogs?: Dialog[];
  pinnedIds?: string[];
}

export interface Dialog {
  userId: string;
  name: string;
  unread: number;
}

export type ChatTarget = { channelId: string; userId?: never } | { userId: string; channelId?: never };

type ChatCommand =
  | { type: "chat.watermark" }
  | { type: "chat.history" | "chat.pins"; cursor: string; forward?: boolean }
  | { type: "chat.changes" | "chat.read" | "chat.delete"; cursor: string }
  | { type: "chat.react"; cursor: string; text: string; forward: boolean }
  | { type: "chat.pin"; cursor: string; forward: boolean }
  | { type: "chat.send"; text: string; clientId: string; attachmentIds?: string[] };

export type Command =
  | { type: "join"; channelId: string }
  | { type: "mute" | "deafen" | "away"; value: boolean }
  | { type: "wake"; participantId: string }
  | { type: "chat.dialogs"; cursor: string }
  | (ChatTarget & ChatCommand);

export interface ClientEvent {
  time: string;
  sequence: string;
  kind: string;
  message: string;
}
export type ServerEvent = Snapshot | { type: "events"; events: ClientEvent[] } |
  { type: "result"; id: string; ok: boolean; error?: string; page?: ChatPage };
export type Send = (command: Command) => Promise<ChatPage | undefined>;

export interface WebSession {
  expiresAt: string;
  iceServers: RTCIceServer[];
}

export type LoginMode = "nickname" | "invite";
export type LoginInput = { nickname: string } | { account: string; accessCode: string };
