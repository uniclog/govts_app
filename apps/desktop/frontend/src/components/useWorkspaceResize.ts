import {useLayoutEffect, useRef, useState} from "react";
import type {CSSProperties, KeyboardEvent, PointerEvent} from "react";
import {useLocale} from "../locales/Locale";

type Side = "browser" | "chat";
const defaults = {browser: 240, chat: 288};
const dividerWidth = 8;
const minimumCenterWidth = 280;

export function useWorkspaceResize(showInfo: boolean) {
    const {m} = useLocale();
    const layoutRef = useRef<HTMLDivElement>(null);
    const drag = useRef<{side: Side; startX: number; startWidth: number} | null>(null);
    const [resizing, setResizing] = useState(false);
    const [availableWidth, setAvailableWidth] = useState(window.innerWidth);
    const [preferredWidths, setPreferredWidths] = useState(defaults);

    useLayoutEffect(() => {
        const element = layoutRef.current;
        if (!element) return;
        setAvailableWidth(element.clientWidth);
        const observer = new ResizeObserver(() => setAvailableWidth(element.clientWidth));
        observer.observe(element);
        return () => observer.disconnect();
    }, []);

    const layoutWidth = Math.max(0, availableWidth - 2 * dividerWidth);
    const browserMin = Math.min(192, layoutWidth / 3);
    const chatMin = Math.min(240, layoutWidth / 3);
    const centerMin = Math.min(minimumCenterWidth, Math.max(0, layoutWidth - browserMin - chatMin));
    const browserLimit = Math.max(browserMin, layoutWidth - chatMin - centerMin);
    const browserWidth = Math.max(browserMin, Math.min(preferredWidths.browser, browserLimit));
    const chatMax = Math.max(chatMin, layoutWidth - browserWidth - centerMin);
    const chatWidth = Math.max(chatMin, Math.min(preferredWidths.chat, chatMax));
    const browserMax = Math.max(browserMin, layoutWidth - chatWidth - centerMin);

    const widths = {browser: browserWidth, chat: chatWidth};
    const minimums = {browser: browserMin, chat: chatMin};
    const maximums = {browser: browserMax, chat: chatMax};
    const resize = (side: Side, value: number) => {
        const next = Math.max(minimums[side], Math.min(value, maximums[side]));
        setPreferredWidths((current) => ({...current, [side]: next}));
    };
    const stop = () => { drag.current = null; setResizing(false); };
    const separatorProps = (side: Side) => ({
        role: "separator" as const,
        tabIndex: 0,
        "aria-orientation": "vertical" as const,
        "aria-label": side === "browser" ? m.channels.resizeChannels : showInfo ? m.channels.resizeInfo : m.channels.resizeChat,
        "aria-controls": side === "browser" ? "channel-browser-pane" : showInfo ? "channel-info-pane" : "server-content-pane",
        "aria-valuemin": Math.round(minimums[side]),
        "aria-valuemax": Math.round(maximums[side]),
        "aria-valuenow": Math.round(widths[side]),
        "aria-valuetext": m.channels.pixels(Math.round(widths[side])),
        onPointerDown: (event: PointerEvent<HTMLDivElement>) => {
            if (event.button !== 0) return;
            event.preventDefault();
            event.currentTarget.focus();
            event.currentTarget.setPointerCapture(event.pointerId);
            drag.current = {side, startX: event.clientX, startWidth: widths[side]};
            setResizing(true);
        },
        onPointerMove: (event: PointerEvent<HTMLDivElement>) => {
            const current = drag.current;
            if (!current || current.side !== side) return;
            resize(side, current.startWidth + (event.clientX - current.startX) * (side === "browser" ? 1 : -1));
        },
        onPointerUp: (event: PointerEvent<HTMLDivElement>) => {
            if (event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId);
            stop();
        },
        onPointerCancel: stop,
        onLostPointerCapture: stop,
        onDoubleClick: () => resize(side, defaults[side]),
        onKeyDown: (event: KeyboardEvent<HTMLDivElement>) => {
            if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
            event.preventDefault();
            const direction = (event.key === "ArrowRight" ? 1 : -1) * (side === "browser" ? 1 : -1);
            resize(side, widths[side] + direction * (event.shiftKey ? 30 : 10));
        },
    });

    return {layoutRef, resizing, separatorProps, style: {
        "--channel-browser-width": `${browserWidth}px`,
        "--channel-chat-width": `${chatWidth}px`,
    } as CSSProperties};
}
