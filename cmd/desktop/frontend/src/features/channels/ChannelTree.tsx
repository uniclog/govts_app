import {useMemo, useState} from "react";
import type {ChannelDTO, ParticipantDTO} from "../../api";
import {buildChannelGroups, canMoveParticipant} from "../../model";
import {ParticipantRow} from "../participants/ParticipantRow";
import {Icon} from "../../components/Icon";
import {useLocale} from "../../locales/Locale";

const participantDragType = "application/x-sonoryx-participant";

export function ChannelTree({channels, participants, screenOwners, canKick, canBan, canDrag, wakeEnabled, currentID, onJoin, onMoveParticipant, onError, onMessage, onWake, query = ""}: {
    query?: string;
    channels: ChannelDTO[];
    participants: ParticipantDTO[];
    screenOwners: Set<string>;
    canKick: boolean;
    canBan: boolean;
    canDrag: boolean;
    wakeEnabled: boolean;
    currentID: string;
    onJoin: (id: string) => void
    onMoveParticipant: (sessionID: string, channelID: string) => void;
    onError: (message: string) => void;
    onMessage: (participant: ParticipantDTO) => void;
    onWake: (participant: ParticipantDTO) => void;
}) {
    const {m} = useLocale();
    const [draggingSessionID, setDraggingSessionID] = useState<string | null>(null);
    const [dropTargetID, setDropTargetID] = useState<string | null>(null);
    const normalizedQuery = query.trim().toLocaleLowerCase();
    const visibleChannels = useMemo(() => {
        if (!normalizedQuery) return channels;
        const visible = new Set(channels.filter((channel) => channel.name.toLocaleLowerCase().includes(normalizedQuery)).map((channel) => channel.id));
        for (const person of participants) {
            if (person.displayName.toLocaleLowerCase().includes(normalizedQuery)) visible.add(person.channelId);
        }
        const byID = new Map(channels.map((channel) => [channel.id, channel]));
        for (const id of [...visible]) {
            const visited = new Set<string>();
            let parent = byID.get(id)?.parentId;
            while (parent && parent !== "0" && !visited.has(parent)) {
                visited.add(parent);
                visible.add(parent);
                parent = byID.get(parent)?.parentId;
            }
        }
        return channels.filter((channel) => visible.has(channel.id));
    }, [channels, participants, normalizedQuery]);
    const children = useMemo(() => {
        return buildChannelGroups(visibleChannels);
    }, [visibleChannels]);
    const draggingParticipant = participants.find((participant) => participant.sessionId === draggingSessionID);
    const renderLevel = (parentID: string, depth: number): React.ReactNode => (children.get(parentID) ?? []).map((channel) => {
        const members = participants.filter((participant) => participant.channelId === channel.id);
        const canDrop = canMoveParticipant(draggingParticipant, channel.id, canDrag);
        return <div key={channel.id}>
            <button
                className={`channel-row ${currentID === channel.id ? "selected current" : ""} ${canDrop && dropTargetID === channel.id ? "drop-target" : ""}`}
                style={{paddingLeft: 14 + depth * 18}}
                title={canDrop ? m.channels.dropHere : !channel.canJoin ? m.channels.noAccess : undefined}
                onDragOver={(event) => {
                    if (!canDrop) return;
                    event.preventDefault();
                    event.dataTransfer.dropEffect = "move";
                    if (dropTargetID !== channel.id) setDropTargetID(channel.id);
                }}
                onDragLeave={(event) => {
                    if (!(event.relatedTarget instanceof Node) || !event.currentTarget.contains(event.relatedTarget)) {
                        setDropTargetID((current) => current === channel.id ? null : current);
                    }
                }}
                onDrop={(event) => {
                    if (!canDrop || !draggingSessionID || event.dataTransfer.getData(participantDragType) !== draggingSessionID) return;
                    event.preventDefault();
                    setDropTargetID(null);
                    setDraggingSessionID(null);
                    onMoveParticipant(draggingSessionID, channel.id);
                }}
                onDoubleClick={() => { if (channel.canJoin) onJoin(channel.id); }}><span className="channel-icon" aria-hidden="true"><Icon name="sound"/></span><span
                className="channel-name">{channel.name}</span><span className="channel-count">{members.length}{channel.maxUsers ? `/${channel.maxUsers}` : ""}</span>
            </button>
            {members.filter((person) => !normalizedQuery || channel.name.toLocaleLowerCase().includes(normalizedQuery) || person.displayName.toLocaleLowerCase().includes(normalizedQuery)).map((participant) => <ParticipantRow key={participant.sessionId} participant={participant}
                                                          sharingScreen={screenOwners.has(participant.sessionId)}
                                                          canKick={canKick} canBan={canBan} canDrag={canDrag}
                                                          canWake={wakeEnabled && participant.channelId === currentID}
                                                          onWake={onWake}
                                                          depth={depth} onError={onError} onMessage={onMessage}
                                                          onDragStart={(event, source) => {
                                                              if (!canDrag) {
                                                                  event.preventDefault();
                                                                  return;
                                                              }
                                                              event.dataTransfer.setData(participantDragType, source.sessionId);
                                                              event.dataTransfer.effectAllowed = "move";
                                                              setDraggingSessionID(source.sessionId);
                                                          }}
                                                          onDragEnd={() => { setDraggingSessionID(null); setDropTargetID(null); }}/>)}{renderLevel(channel.id, depth + 1)}</div>;
    });
    return <div className="channel-tree">{visibleChannels.length ? renderLevel("0", 0) :
        <div className="empty-state">{m.channels.noChannels}</div>}</div>;
}
