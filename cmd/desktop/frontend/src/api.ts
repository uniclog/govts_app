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

export const desktopAPI = {
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
    setMuted: (value: boolean) => Service.SetMuted(value),
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
    closeToTray: (): Promise<boolean> => Call.ByName("uniclog.io/sonoryx/internal/ui/wails.Service.CloseToTray"),
    setCloseToTray: (value: boolean): Promise<void> => Call.ByName("uniclog.io/sonoryx/internal/ui/wails.Service.SetCloseToTray", value),
    mediaServerIdentity: () => Service.MediaServerIdentity(),
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
