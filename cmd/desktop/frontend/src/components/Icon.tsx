export type IconName = "channels" | "settings" | "mic" | "sound" | "soundOff" | "screen" | "info" | "disconnect" | "hangup" | "chat" | "server";

export function Icon({name}: {name: IconName}) {
    const paths: Record<IconName, React.ReactNode> = {
        server: <><rect x="4" y="3" width="16" height="8" rx="2"/><rect x="4" y="13" width="16" height="8" rx="2"/><path d="M8 7h.01M8 17h.01M12 7h4M12 17h4"/></>,
        channels: <><rect x="4" y="4" width="16" height="16" rx="3"/><path d="M9 4v16M13 9h3M13 13h3"/></>,
        settings: <><path d="m9 3-1 3-3 1 1 3-2 2 2 2-1 3 3 1 1 3h6l1-3 3-1-1-3 2-2-2-2 1-3-3-1-1-3Z"/><circle cx="12" cy="12" r="3"/></>,
        mic: <><rect x="9" y="3" width="6" height="12" rx="3"/><path d="M6 11v1a6 6 0 0 0 12 0v-1M12 18v3M9 21h6"/></>,
        sound: <><path d="m11 5-5 4H3v6h3l5 4ZM16 8a6 6 0 0 1 0 8M19 5a10 10 0 0 1 0 14"/></>,
        soundOff: <><path d="m11 5-5 4H3v6h3l5 4Z"/><path d="m16 9 5 5M21 9l-5 5"/></>,
        screen: <><rect x="3" y="4" width="18" height="13" rx="2"/><path d="M12 17v4M8 21h8"/></>,
        hangup: <path d="M3 15v-3c5-5 13-5 18 0v3h-5v-4a13 13 0 0 0-8 0v4Z"/>,
        chat: <path d="M5 4h14a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H9l-6 3V6a2 2 0 0 1 2-2Z"/>,
        info: <><circle cx="12" cy="12" r="9"/><path d="M12 11v6M12 7h.01"/></>,
        disconnect: <><path d="M9 4H4v16h5M9 12h12m-4-4 4 4-4 4"/></>,
    };
    return <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">{paths[name]}</svg>;
}
