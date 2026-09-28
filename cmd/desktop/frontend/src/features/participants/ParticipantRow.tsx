import {useEffect, useRef, useState} from "react";
import {createPortal} from "react-dom";
import type {ChannelDTO, ParticipantDTO} from "../../api";
import {desktopAPI} from "../../api";

type MenuState = {x: number; y: number; volume: number; channelID: string} | null;

function MicOffIcon() {
    return <svg viewBox="0 0 24 24" focusable="false" aria-hidden="true" fill="none"
                stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <path d="M12 3a3 3 0 0 0-3 3v6a3 3 0 0 0 6 0V6a3 3 0 0 0-3-3Z"/>
        <path d="M19 11a7 7 0 0 1-7 7 7 7 0 0 1-7-7"/>
        <path d="M12 18v3"/>
        <path d="m3 3 18 18"/>
    </svg>;
}

function SoundOffIcon() {
    return <svg viewBox="0 0 24 24" focusable="false" aria-hidden="true" fill="none"
                stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <path d="M4 13v-1a8 8 0 0 1 16 0v1"/>
        <path d="M4 13a2 2 0 0 0 2 2h1v-5H6a2 2 0 0 0-2 2Z"/>
        <path d="M20 13a2 2 0 0 1-2 2h-1v-5h1a2 2 0 0 1 2 2Z"/>
        <path d="m3 3 18 18"/>
    </svg>;
}

function SpeakerIcon({muted = false}: {muted?: boolean}) {
    return <svg viewBox="0 0 24 24" focusable="false" aria-hidden="true" fill="none"
                stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <path d="M11 5 6 9H3v6h3l5 4V5Z"/>
        {muted
            ? <><path d="m16 9 5 6"/><path d="m21 9-5 6"/></>
            : <><path d="M15.5 9.5a4 4 0 0 1 0 5"/><path d="M18.3 6.7a8 8 0 0 1 0 10.6"/></>}
    </svg>;
}

export function ParticipantRow({participant, channels, canKick, canBan, canDrag, depth, onError, onDragStart, onDragEnd}: {
    participant: ParticipantDTO;
    channels: ChannelDTO[];
    canKick: boolean;
    canBan: boolean;
    canDrag: boolean;
    depth: number;
    onError: (message: string) => void;
    onDragStart: (event: React.DragEvent<HTMLDivElement>, participant: ParticipantDTO) => void;
    onDragEnd: () => void;
}) {
    const [menu, setMenu] = useState<MenuState>(null);
    const rowRef = useRef<HTMLDivElement>(null);
    const menuRef = useRef<HTMLDivElement>(null);
    const timerRef = useRef<number | undefined>(undefined);
    const errorText = (error: unknown) => (error instanceof Error ? error.message : String(error)).replace(/^Error:\s*/, "");

    const open = async (x: number, y: number) => {
        if (participant.local) return;
        try {
            const volume = await desktopAPI.participantVolume(participant.sessionId);
            setMenu({x, y, volume, channelID: channels.find((channel) => channel.id !== participant.channelId)?.id ?? ""});
        } catch (error) {
            onError(errorText(error));
        }
    };
    const close = () => {
        setMenu(null);
        rowRef.current?.focus();
    };
    const setVolume = (volume: number, immediate = false) => {
        setMenu((current) => current ? {...current, volume} : current);
        window.clearTimeout(timerRef.current);
        const commit = () => void desktopAPI.setParticipantVolume(participant.sessionId, volume).catch((error) => onError(errorText(error)));
        if (immediate) commit();
        else timerRef.current = window.setTimeout(commit, 40);
    };
    const moderate = async (action: "kick" | "ban" | "drag") => {
        if (!menu) return;
        if (action === "ban" && !window.confirm(`Заблокировать ${participant.displayName}?`)) return;
        try {
            if (action === "kick") await desktopAPI.kick(participant.sessionId);
            if (action === "ban") await desktopAPI.ban(participant.sessionId);
            if (action === "drag") await desktopAPI.drag(participant.sessionId, menu.channelID);
            close();
        } catch (error) {
            onError(errorText(error));
        }
    };

    useEffect(() => () => window.clearTimeout(timerRef.current), []);
    useEffect(() => {
        if (!menu) return;
        const dismiss = (event: PointerEvent) => {
            if (!menuRef.current?.contains(event.target as Node)) close();
        };
        const keydown = (event: KeyboardEvent) => {
            if (event.key === "Escape") close();
        };
        window.addEventListener("pointerdown", dismiss);
        window.addEventListener("keydown", keydown);
        requestAnimationFrame(() => menuRef.current?.querySelector<HTMLInputElement>("input")?.focus());
        return () => {
            window.removeEventListener("pointerdown", dismiss);
            window.removeEventListener("keydown", keydown);
        };
    }, [menu?.x, menu?.y]);

    const position = menu ? {
        left: Math.max(8, Math.min(menu.x, window.innerWidth - 284)),
        top: Math.max(8, Math.min(menu.y, window.innerHeight - 320)),
    } : undefined;
    return <>
        <div ref={rowRef} className={`participant-row ${participant.speaking ? "speaking" : ""} ${canDrag ? "draggable" : ""}`}
             style={{paddingLeft: 46 + depth * 18}} tabIndex={participant.local ? -1 : 0}
             draggable={canDrag}
             onDragStart={(event) => onDragStart(event, participant)} onDragEnd={onDragEnd}
             onContextMenu={(event) => {
                 if (participant.local) return;
                 event.preventDefault();
                 void open(event.clientX, event.clientY);
             }} onKeyDown={(event) => {
                if (!participant.local && (event.key === "ContextMenu" || (event.shiftKey && event.key === "F10"))) {
                    event.preventDefault();
                    const bounds = event.currentTarget.getBoundingClientRect();
                    void open(bounds.left + 40, bounds.bottom);
                }
             }}><span className="avatar">{participant.displayName.slice(0, 1).toUpperCase()}</span>
            <span>{participant.displayName}{participant.local ? " (вы)" : ""}</span>
            <span className="participant-status">
                {participant.muted && <span className="participant-flag" aria-label="Микрофон выключен"><MicOffIcon/></span>}
                {participant.deafened && <span className="participant-flag" aria-label="Звук выключен"><SoundOffIcon/></span>}
                <span className="speaking-ring" aria-label={participant.speaking ? "Говорит" : "Не говорит"}/>
            </span>
        </div>
        {menu && createPortal(<div ref={menuRef} className="participant-menu" role="menu" style={position}
                                   aria-label={`Управление пользователем ${participant.displayName}`}>
            <div className="participant-menu-user">
                <span className="participant-menu-avatar" aria-hidden="true">{participant.displayName.slice(0, 1).toUpperCase()}</span>
                <strong>{participant.displayName}</strong>
            </div>
            <div className="participant-menu-separator"/>
            <div className="participant-volume-heading">
                <label htmlFor={`participant-volume-${participant.sessionId}`}>Громкость пользователя</label>
                <output>{Math.round(menu.volume * 100)}%</output>
            </div>
            <div className="participant-volume-control">
                <span className="participant-volume-icon" aria-hidden="true">
                    <SpeakerIcon muted={menu.volume === 0}/>
                </span>
                <input id={`participant-volume-${participant.sessionId}`} type="range" min="0" max="2" step="0.01"
                       style={{"--participant-volume": `${menu.volume / 2 * 100}%`} as React.CSSProperties}
                       value={menu.volume} onChange={(event) => setVolume(Number(event.target.value))}
                       onPointerUp={(event) => setVolume(Number(event.currentTarget.value), true)}
                       onKeyUp={(event) => setVolume(Number(event.currentTarget.value), true)}/>
            </div>
            <div className="participant-menu-actions">
                <button type="button" role="menuitem" className={menu.volume === 0 ? "active" : ""}
                        onClick={() => setVolume(menu.volume === 0 ? 1 : 0, true)}>
                    <span aria-hidden="true"><SpeakerIcon muted={menu.volume !== 0}/></span>{menu.volume === 0 ? "Включить звук" : "Заглушить"}
                </button>
                {canKick && <button type="button" role="menuitem" onClick={() => void moderate("kick")}>Отключить</button>}
                {canBan && <button type="button" role="menuitem" onClick={() => void moderate("ban")}>Заблокировать</button>}
                {canDrag && menu.channelID && <div className="participant-drag-control">
                    <select aria-label="Канал для перемещения" value={menu.channelID}
                            onChange={(event) => setMenu((current) => current ? {...current, channelID: event.target.value} : null)}>
                        {channels.filter((channel) => channel.id !== participant.channelId).map((channel) =>
                            <option key={channel.id} value={channel.id}>{channel.name}</option>)}
                    </select>
                    <button type="button" role="menuitem" onClick={() => void moderate("drag")}>Переместить</button>
                </div>}
            </div>
        </div>, document.body)}
    </>;
}
