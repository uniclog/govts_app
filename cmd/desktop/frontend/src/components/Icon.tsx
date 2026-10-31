export type IconName = "minimise" | "maximise" | "restoreWindow" | "globe" | "users" | "lock" | "arrowRight" | "channels" | "settings" | "mic" | "sound" | "screen" | "info" | "disconnect" | "hangup" | "chat" | "server" | "close" | "refresh" | "away" | "here" | "shield" | "attachment" | "file" | "download" | "check" | "pause" | "play";

export function Icon({name}: {name: IconName}) {
    const paths: Record<IconName, React.ReactNode> = {
        minimise: <path d="M5 12h14"/>,
        maximise: <rect x="5" y="5" width="14" height="14" rx="1"/>,
        restoreWindow: <><path d="M9 5V3h12v12h-2"/><rect x="3" y="9" width="12" height="12" rx="1"/></>,
        globe: <><circle cx="12" cy="12" r="9"/><ellipse cx="12" cy="12" rx="4" ry="9"/><path d="M3 12h18"/></>,
        users: <><circle cx="9" cy="8" r="3"/><path d="M3 21v-3a6 6 0 0 1 12 0v3M16 5a3 3 0 0 1 0 6M21 21v-3a6 6 0 0 0-3-5"/></>,
        lock: <><rect x="5" y="10" width="14" height="11" rx="2"/><path d="M8 10V7a4 4 0 0 1 8 0v3"/></>,
        arrowRight: <path d="M4 12h16m-6-6 6 6-6 6"/>,
        check: <path d="m5 12 4 4L19 6"/>,
        pause: <path d="M8 5v14M16 5v14"/>,
        play: <path d="m8 4 12 8-12 8Z"/>,
        attachment: <path d="m8 13 6.5-6.5a3 3 0 0 1 4.2 4.2L10 19.4a5 5 0 0 1-7-7L12 3.5a2 2 0 0 1 2.8 2.8L6 15.1a1 1 0 0 0 1.4 1.4L16 8"/>,
        file: <><path d="M14 3H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9Z"/><path d="M14 3v6h6M8 13h8M8 17h5"/></>,
        download: <path d="M12 3v12m-5-5 5 5 5-5M4 16v4h16v-4"/>,
        refresh: <><path d="M20 7v5h-5M4 17v-5h5"/><path d="M6.1 7a7 7 0 0 1 11.5-1L20 9M4 15l2.4 3A7 7 0 0 0 17.9 17"/></>,
        server: <><rect x="4" y="3" width="16" height="8" rx="2"/><rect x="4" y="13" width="16" height="8" rx="2"/><path d="M8 7h.01M8 17h.01M12 7h4M12 17h4"/></>,
        channels: <><rect x="4" y="4" width="16" height="16" rx="3"/><path d="M9 4v16M13 9h3M13 13h3"/></>,
        settings: <><path d="M18.86 9.50L21.19 10.05L21.19 13.95L18.86 14.50L18.62 15.09L19.88 17.12L17.12 19.88L15.09 18.62L14.50 18.86L13.95 21.19L10.05 21.19L9.50 18.86L8.91 18.62L6.88 19.88L4.12 17.12L5.38 15.09L5.14 14.50L2.81 13.95L2.81 10.05L5.14 9.50L5.38 8.91L4.12 6.88L6.88 4.12L8.91 5.38L9.50 5.14L10.05 2.81L13.95 2.81L14.50 5.14L15.09 5.38L17.12 4.12L19.88 6.88L18.62 8.91Z"/><circle cx="12" cy="12" r="3"/></>,
        mic: <><rect x="9" y="3" width="6" height="12" rx="3"/><path d="M6 11v1a6 6 0 0 0 12 0v-1M12 18v3M9 21h6"/></>,
        sound: <><path d="m11 5-5 4H3v6h3l5 4ZM16 8a6 6 0 0 1 0 8M19 5a10 10 0 0 1 0 14"/></>,
        screen: <><rect x="3" y="4" width="18" height="13" rx="2"/><path d="M12 17v4M8 21h8"/></>,
        hangup: <path d="M3 15v-3c5-5 13-5 18 0v3h-5v-4a13 13 0 0 0-8 0v4Z"/>,
        chat: <path d="M5 4h14a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H9l-6 3V6a2 2 0 0 1 2-2Z"/>,
        info: <><circle cx="12" cy="12" r="9"/><path d="M12 11v6M12 7h.01"/></>,
        disconnect: <><path d="M9 4H4v16h5M9 12h12m-4-4 4 4-4 4"/></>,
        close: <path d="M6 6l12 12M18 6 6 18"/>,
        away: <><circle cx="9" cy="8" r="3"/><path d="M3 19v-1a4 4 0 0 1 4-4h2.5"/><path d="M15.5 15.5h5.5M15.5 19.5h5.5"/></>,
        here: <><circle cx="9" cy="8" r="3"/><path d="M3 19v-1a4 4 0 0 1 4-4h2.5"/><path d="m15 17.5 2.2 2.2 4.3-4.7"/></>,
        shield: <><path d="M12 3 5 6v5c0 4.5 3 8.3 7 10 4-1.7 7-5.5 7-10V6Z"/><path d="m9 12 2 2 4-4"/></>,
    };
    return <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">{paths[name]}</svg>;
}
