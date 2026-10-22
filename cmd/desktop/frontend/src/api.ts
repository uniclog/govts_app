import {Call, Events} from "@wailsio/runtime";
import {Service} from "../bindings/uniclog.io/sonoryx/internal/ui/wails";

export type {
    AudioDeviceDTO,
    AudioDevicesDTO,
    AudioMeterDTO,
    ChannelDTO,
    ClientEventDTO,
    ClientViewDTO,
    ConnectionStatsDTO,
    ConnectRequest,
    ParticipantDTO,
    ScreenStreamDTO,
    MediaTrustDTO,
    ChatRequestDTO,
    ChatPageDTO,
    ChatMessageDTO,
    ChatDialogDTO,
} from "../bindings/uniclog.io/sonoryx/internal/ui/wails";

import type {AudioMeterDTO, ConnectRequest, ChatRequestDTO} from "../bindings/uniclog.io/sonoryx/internal/ui/wails";

export type AdminPermission = "kick" | "ban" | "drag" | "admin";
export type AdminAccount = {id: string; name: string; fingerprint: string; joinLevel: number; kick: boolean; ban: boolean; drag: boolean;
    admin: boolean; owner: boolean; banned: boolean; online: boolean; banReason: string};
export type AdminAccountsPage = {accounts: AdminAccount[] | null; next: string};
export type AdminAuditEntry = {id: string; happenedAtMs: number; actor: string; action: string; targetUserId: string; detail: string};
export type AdminAuditPage = {entries: AdminAuditEntry[] | null; next: string};
export type AdminChannelInput = {parentId: string; name: string; topic: string; description: string; position: number; maxUsers: number;
    minJoinLevel: number; bitrate: number; application: "voip" | "audio"};

const adminCall = <T>(method: string, ...args: unknown[]): Promise<T> =>
    Call.ByName(`uniclog.io/sonoryx/internal/ui/wails.Service.${method}`, ...args) as Promise<T>;

let diagnosticsInFlight = 0;
export type RecentServer = {address: string; alias?: string; favorite: boolean; lastVisited: number; current: boolean;
    onlineCount: number | null; status: "unknown" | "available" | "unavailable"; updatedAt: number; lastAttemptAt: number};
export function logDiagnostic(operation: string, message: unknown): void {
    if (diagnosticsInFlight >= 16) return;
    diagnosticsInFlight++;
    const text = message instanceof Error ? `${message.name}: ${message.message}` : String(message);
    void Call.ByName("uniclog.io/sonoryx/internal/ui/wails.Service.LogDiagnostic", operation.slice(0, 128), text.slice(0, 2048))
        .catch(() => undefined)
        .finally(() => { diagnosticsInFlight--; });
}

export type HotkeyAction = "mute" | "deafen" | "away";
// The shortcut is Ctrl+Alt+key; key is an upper-case Latin letter or digit.
export type Hotkey = {action: HotkeyAction; key: string; status: "active" | "off" | "failed"};

export const desktopAPI = {
    hotkeys: (): Promise<Hotkey[]> => Call.ByName("uniclog.io/sonoryx/internal/ui/wails.Service.Hotkeys"),
    setHotkey: (action: HotkeyAction, key: string): Promise<void> => Call.ByName("uniclog.io/sonoryx/internal/ui/wails.Service.SetHotkey", action, key),
    hotkeysEnabled: (): Promise<boolean> => Call.ByName("uniclog.io/sonoryx/internal/ui/wails.Service.HotkeysEnabled"),
    setHotkeysEnabled: (value: boolean): Promise<void> => Call.ByName("uniclog.io/sonoryx/internal/ui/wails.Service.SetHotkeysEnabled", value),
    hotkeyNotifications: (): Promise<boolean> => Call.ByName("uniclog.io/sonoryx/internal/ui/wails.Service.HotkeyNotifications"),
    setHotkeyNotifications: (value: boolean): Promise<void> => Call.ByName("uniclog.io/sonoryx/internal/ui/wails.Service.SetHotkeyNotifications", value),
    setHotkeyNotices: (texts: Record<string, string>): Promise<void> => Call.ByName("uniclog.io/sonoryx/internal/ui/wails.Service.SetHotkeyNotices", texts),
    recentServers: (): Promise<RecentServer[]> => Call.ByName("uniclog.io/sonoryx/internal/ui/wails.Service.RecentServers"),
    refreshServerStatuses: (): Promise<void> => Service.RefreshServerStatuses(),
    onServerStatusChanged: (listener: () => void) => Events.On("server-status-changed", listener),
    setServerAlias: (address: string, alias: string): Promise<void> => Call.ByName("uniclog.io/sonoryx/internal/ui/wails.Service.SetServerAlias", address, alias),
    deleteRecentServer: (address: string): Promise<void> => Call.ByName("uniclog.io/sonoryx/internal/ui/wails.Service.DeleteRecentServer", address),
    setServerFavorite: (address: string, favorite: boolean): Promise<void> => Call.ByName("uniclog.io/sonoryx/internal/ui/wails.Service.SetServerFavorite", address, favorite),
    reconnectServer: (address: string): Promise<void> => Call.ByName("uniclog.io/sonoryx/internal/ui/wails.Service.ReconnectServer", address),
    chat: (request: ChatRequestDTO) => Service.Chat(request),
    snapshot: () => Service.Snapshot(),
    connectionStats: () => Service.ConnectionStats(),
    eventsAfter: (sequence: string) => Service.EventsAfter(sequence),
    savedDisplayName: () => Service.SavedDisplayName(),
    connect: (request: ConnectRequest) => Service.Connect(request),
    disconnect: () => Service.Disconnect(),
    joinChannel: (channelID: string) => Service.JoinChannel(channelID),
    kick: (sessionID: string) => Service.Kick(sessionID),
    ban: (sessionID: string) => Service.Ban(sessionID),
    drag: (sessionID: string, channelID: string) => Service.Drag(sessionID, channelID),
    adminAccounts: (cursor: string) => adminCall<AdminAccountsPage>("AdminAccounts", cursor),
    adminSetPermission: (accountID: string, permission: AdminPermission, enabled: boolean) => adminCall<AdminAccount>("AdminSetPermission", accountID, permission, enabled),
    adminSetJoinLevel: (accountID: string, level: number) => adminCall<AdminAccount>("AdminSetJoinLevel", accountID, level),
    adminSetBan: (accountID: string, banned: boolean, reason: string) => adminCall<AdminAccount>("AdminSetBan", accountID, banned, reason),
    adminSetChannelJoinLevel: (channelID: string, level: number) => adminCall<void>("AdminSetChannelJoinLevel", channelID, level),
    adminAudit: (cursor: string) => adminCall<AdminAuditPage>("AdminAudit", cursor),
    adminCreateChannel: (input: AdminChannelInput) => adminCall<string>("AdminCreateChannel", input),
    adminUpdateChannel: (channelID: string, input: AdminChannelInput) => adminCall<void>("AdminUpdateChannel", channelID, input),
    adminDeleteChannel: (channelID: string) => adminCall<void>("AdminDeleteChannel", channelID),
    adminDeleteAccount: (accountID: string) => adminCall<void>("AdminDeleteAccount", accountID),
    setMuted: (value: boolean) => Service.SetMuted(value),
    setAway: (away: boolean) => Service.SetAway(away),
    wake: (sessionID: string) => Service.Wake(sessionID),
    messageReceived: () => Service.MessageReceived(),
    previewSoundPack: () => Service.PreviewSoundPack(),
    stopSoundPreview: () => Service.StopSoundPreview(),
    setSoundPack: (id: string) => Service.SetSoundPack(id),
    setSoundVolume: (value: number) => Service.SetSoundVolume(value),
    setMuteOnAway: (value: boolean) => Service.SetMuteOnAway(value),
    setDeafenOnAway: (value: boolean) => Call.ByName("uniclog.io/sonoryx/internal/ui/wails.Service.SetDeafenOnAway", value),
    setSoundCue: (id: string, enabled: boolean) => Call.ByName("uniclog.io/sonoryx/internal/ui/wails.Service.SetSoundCue", id, enabled),
    setDeafened: (value: boolean) => Service.SetDeafened(value),
    setRNNoiseEnabled: (value: boolean) => Service.SetRNNoiseEnabled(value),
    setRNNoiseSensitivity: (value: number) => Service.SetRNNoiseSensitivity(value),
    setMicrophoneGain: (value: number) => Service.SetMicrophoneGain(value),
    setVADEnabled: (value: boolean) => Service.SetVADEnabled(value),
    setVADMode: (value: string) => Service.SetVADMode(value),
    setVADSensitivity: (value: number) => Service.SetVADSensitivity(value),
    audioDevices: () => Service.AudioDevices(),
    setCaptureDevice: (id: string) => Service.SetCaptureDevice(id),
    setPlaybackDevice: (id: string) => Service.SetPlaybackDevice(id),
    setMicrophonePreview: (enabled: boolean) => Service.SetMicrophonePreview(enabled),
    setMicrophoneMonitor: (enabled: boolean) => Service.SetMicrophoneMonitor(enabled),
    participantVolume: (sessionID: string) => Service.ParticipantVolume(sessionID),
    setParticipantVolume: (sessionID: string, value: number) => Service.SetParticipantVolume(sessionID, value),
    theme: () => Service.Theme(),
    setTheme: (value: string) => Service.SetTheme(value),
    locale: (): Promise<string> => Call.ByName("uniclog.io/sonoryx/internal/ui/wails.Service.Locale"),
    setLocale: (value: string): Promise<void> => Call.ByName("uniclog.io/sonoryx/internal/ui/wails.Service.SetLocale", value),
    closeToTray: (): Promise<boolean> => Call.ByName("uniclog.io/sonoryx/internal/ui/wails.Service.CloseToTray"),
    setCloseToTray: (value: boolean): Promise<void> => Call.ByName("uniclog.io/sonoryx/internal/ui/wails.Service.SetCloseToTray", value),
    mediaServerIdentity: () => Service.MediaServerIdentity(),
    screenCapabilities: () => Call.ByName("uniclog.io/sonoryx/internal/ui/wails.Service.ScreenCapabilities") as Promise<{legacy?: boolean; codecs?: {mimeType: string; sdpFmtpLine: string; payloadType: number}[]; audio?: {mimeType: string; sdpFmtpLine: string; payloadType: number}[]}>,
    screenAudioSupported: (): Promise<boolean> => Call.ByName("uniclog.io/sonoryx/internal/ui/wails.Service.ScreenAudioSupported"),
    screenAudioApps: (): Promise<{pid: number; name: string; title: string}[] | null> => Call.ByName("uniclog.io/sonoryx/internal/ui/wails.Service.ScreenAudioApps"),
    startScreenAudio: (pid: number): Promise<string> => Call.ByName("uniclog.io/sonoryx/internal/ui/wails.Service.StartScreenAudio", pid),
    // 48 kHz stereo s16le PCM as base64; empty while the source is silent.
    readScreenAudio: (id: string): Promise<string | null> => Call.ByName("uniclog.io/sonoryx/internal/ui/wails.Service.ReadScreenAudio", id),
    stopScreenAudio: (id: string): Promise<void> => Call.ByName("uniclog.io/sonoryx/internal/ui/wails.Service.StopScreenAudio", id),
    trustMediaServer: (fingerprint: string) => Service.TrustMediaServer(fingerprint),
    publishScreen: (offer: {type: string; sdp: string}) => Service.PublishScreen(offer),
    subscribeScreen: (streamID: string, subscriberID: string, offer: {type: string; sdp: string}) =>
        Service.SubscribeScreen(streamID, subscriberID, offer),
    stopScreen: (streamID: string) => Service.StopScreen(streamID),
    unsubscribeScreen: (streamID: string, subscriberID: string) => Service.UnsubscribeScreen(streamID, subscriberID),
    openScreenWindow: (streamID: string, ownerName: string) => Service.OpenScreenWindow(streamID, ownerName),
    onStateChanged: (listener: () => void) => Events.On("client-state-changed", listener),
    onTrayScreenShare: (listener: () => void) => Events.On("tray-screen-share", () => listener()),
    onEventLogChanged: (listener: () => void) => Events.On("client-event-log-changed", listener),
    onAudioMeter: (listener: (sample: AudioMeterDTO) => void) =>
        Events.On("audio-meter", (event) => listener(event.data as AudioMeterDTO)),
};
