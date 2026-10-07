import {useEffect, useRef, useState} from "react";
import {createPortal} from "react-dom";
import type {ParticipantDTO} from "../../api";
import {desktopAPI} from "../../api";
import {Icon} from "../../components/Icon";

type MenuState = {x: number; y: number; volume: number} | null;

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

export function ParticipantRow({participant, canKick, canBan, canDrag, depth, onError, onDragStart, onDragEnd, onMessage, variant = "tree", sharingScreen = false}: {
    participant: ParticipantDTO;
    canKick: boolean;
    canBan: boolean;
    canDrag: boolean;
    depth: number;
    onError: (message: string) => void;
    onDragStart: (event: React.DragEvent<HTMLDivElement>, participant: ParticipantDTO) => void;
    onDragEnd: () => void;
    onMessage: (participant: ParticipantDTO) => void;
    variant?: "tree" | "list" | "stage";
    sharingScreen?: boolean;
}) {
    const [menu, setMenu] = useState<MenuState>(null);
    const rowRef = useRef<HTMLDivElement>(null);
    const menuRef = useRef<HTMLDivElement>(null);
    const timerRef = useRef<number | undefined>(undefined);
    const errorText = (error: unknown) => (error instanceof Error ? error.message : String(error)).replace(/^Error:\s*/, "");

    const open = async (x: number, y: number) => {
        if (participant.local) return;
        setMenu({x, y, volume: 1});
        try {
            const volume = await desktopAPI.participantVolume(participant.sessionId);
            setMenu((current) => current && current.x === x && current.y === y ? {...current, volume} : current);
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
    const setVolumeLevel = (level: number, immediate = false) => setVolume((level / 100) ** 2, immediate);
    const moderate = async (action: "kick" | "ban") => {
        if (!menu) return;
        if (action === "ban" && !window.confirm(`Заблокировать ${participant.displayName}?`)) return;
        try {
            if (action === "kick") await desktopAPI.kick(participant.sessionId);
            if (action === "ban") await desktopAPI.ban(participant.sessionId);
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
        requestAnimationFrame(() => (menuRef.current?.querySelector<HTMLInputElement>("input") ?? menuRef.current?.querySelector<HTMLButtonElement>("button"))?.focus());
        return () => {
            window.removeEventListener("pointerdown", dismiss);
            window.removeEventListener("keydown", keydown);
        };
    }, [menu?.x, menu?.y]);

    const position = menu ? {
        left: Math.max(8, Math.min(menu.x, window.innerWidth - 284)),
        top: Math.max(8, Math.min(menu.y, window.innerHeight - 320)),
    } : undefined;
    const volumeLevel = menu ? Math.round(Math.sqrt(menu.volume) * 100) : 100;
    return <>
        <div ref={rowRef} className={`${variant === "stage" ? "stage-participant" : "participant-row"} participant-${variant} ${variant !== "list" && participant.speaking ? "speaking" : ""} ${variant !== "list" && participant.muted ? "muted" : ""} ${canDrag ? "draggable" : ""} ${participant.local ? "local" : ""}`}
             style={variant === "stage" ? undefined : {paddingLeft: variant === "list" ? 12 : 46 + depth * 18}} tabIndex={participant.local ? -1 : 0}
             draggable={canDrag}
             onDragStart={(event) => onDragStart(event, participant)} onDragEnd={onDragEnd}
             onDoubleClick={(event) => {
                 event.stopPropagation();
                 if (participant.local) return;
                 setMenu(null);
                 onMessage(participant);
             }}
             onContextMenu={(event) => {
                 event.preventDefault();
                 if (participant.local) return;
                 void open(event.clientX, event.clientY);
             }} onKeyDown={(event) => {
                if (event.key === "ContextMenu" || (event.shiftKey && event.key === "F10")) {
                    event.preventDefault();
                    if (participant.local) return;
                    const bounds = event.currentTarget.getBoundingClientRect();
                    void open(bounds.left + 40, bounds.bottom);
                }
             }}>{variant === "stage" ? <>
            <span className="stage-avatar">{participant.displayName.slice(0, 1).toUpperCase()}</span>
            <span className="stage-label"><span className="stage-name" title={`${participant.displayName}${participant.local ? " (вы)" : ""}`}>{participant.displayName}{participant.local ? " (вы)" : ""}</span>
            </span>
            <span className="stage-voice" aria-label={participant.muted ? "Микрофон выключен" : participant.deafened ? "Звук выключен" : participant.speaking ? "Говорит" : "Не говорит"}>
                {sharingScreen && <span className="participant-screen" title="Демонстрирует экран" aria-label="Демонстрирует экран"><Icon name="screen"/></span>}
                {participant.muted ? <MicOffIcon/> : participant.deafened ? <SoundOffIcon/> : participant.speaking ? <span className="voice-bars"><i/><i/><i/><i/><i/></span> : null}
            </span>
            </> : <><span className="avatar">{participant.displayName.slice(0, 1).toUpperCase()}</span>
            <span className="participant-label"><span className="participant-name" title={participant.displayName}>{participant.displayName}{participant.local ? " (вы)" : ""}</span>
            </span>
            {variant !== "list" && <span className="participant-status">
                {sharingScreen && <span className="participant-screen" title="Демонстрирует экран" aria-label="Демонстрирует экран"><Icon name="screen"/></span>}
                {participant.muted && <span className="participant-flag" aria-label="Микрофон выключен"><MicOffIcon/></span>}
                {participant.deafened && <span className="participant-flag" aria-label="Звук выключен"><SoundOffIcon/></span>}
                <span className="speaking-ring" aria-label={participant.speaking ? "Говорит" : "Не говорит"}/>
            </span>}</>}
        </div>
        {!participant.local && menu && createPortal(<div ref={menuRef} className="participant-menu" role="menu" style={position}
                                   aria-label={`Управление пользователем ${participant.displayName}`}>
            <div className="participant-menu-user">
                <span className="participant-menu-avatar" aria-hidden="true">{participant.displayName.slice(0, 1).toUpperCase()}</span>
                <strong>{participant.displayName}</strong>
            </div>
            <div className="participant-menu-separator"/>
            {!participant.local && <>
            <div className="participant-volume-heading">
                <label htmlFor={`participant-volume-${participant.sessionId}`}>Громкость пользователя</label>
                <output>{volumeLevel}pt.</output>
            </div>
            <div className="participant-volume-control">
                <span className="participant-volume-icon" aria-hidden="true">
                    <SpeakerIcon muted={menu.volume === 0}/>
                </span>
                <input id={`participant-volume-${participant.sessionId}`} type="range" min="0" max="300" step="1"
                       style={{"--participant-volume": `${volumeLevel / 300 * 100}%`} as React.CSSProperties}
                       value={volumeLevel} onChange={(event) => setVolumeLevel(Number(event.target.value))}
                       onPointerUp={(event) => setVolumeLevel(Number(event.currentTarget.value), true)}
                       onKeyUp={(event) => setVolumeLevel(Number(event.currentTarget.value), true)}/>
            </div>
            </>}
            <div className="participant-menu-actions">
                <button type="button" role="menuitem" onClick={() => { onMessage(participant); close(); }}><span><Icon name="chat"/></span>Сообщение</button>
                {!participant.local && <button type="button" role="menuitem" className={menu.volume === 0 ? "active" : ""}
                        onClick={() => setVolume(menu.volume === 0 ? 1 : 0, true)}>
                    <span aria-hidden="true"><SpeakerIcon muted={menu.volume !== 0}/></span>{menu.volume === 0 ? "Включить звук" : "Заглушить"}
                </button>}
                {!participant.local && canKick && <button type="button" role="menuitem" onClick={() => void moderate("kick")}>Отключить</button>}
                {!participant.local && canBan && <button type="button" role="menuitem" onClick={() => void moderate("ban")}>Заблокировать</button>}
            </div>
        </div>, document.body)}
    </>;
}
