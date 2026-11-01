import {useEffect, useLayoutEffect, useRef, useState, type ReactNode} from "react";
import {createPortal} from "react-dom";

const moveFocus = (root: HTMLElement, key: string) => {
    const buttons = [...root.querySelectorAll<HTMLButtonElement>("button:not(:disabled)")];
    const current = buttons.indexOf(document.activeElement as HTMLButtonElement);
    if (current < 0) {buttons[0]?.focus(); return;}
    const origin = buttons[current].getBoundingClientRect();
    if (key === "ArrowLeft" || key === "ArrowRight") {
        buttons[Math.max(0, Math.min(buttons.length - 1, current + (key === "ArrowRight" ? 1 : -1)))]?.focus();
        return;
    }
    // Rows differ in length between groups, so vertical moves go by geometry.
    const down = key === "ArrowDown";
    const candidates = buttons.filter((button) => {const top = button.getBoundingClientRect().top; return down ? top > origin.top + 1 : top < origin.top - 1;});
    const rowTop = candidates.reduce((best, button) => {const top = button.getBoundingClientRect().top; return down ? Math.min(best, top) : Math.max(best, top);}, down ? Infinity : -Infinity);
    const row = candidates.filter((button) => Math.abs(button.getBoundingClientRect().top - rowTop) <= 1);
    const distance = (button: HTMLButtonElement) => Math.abs(button.getBoundingClientRect().left - origin.left);
    row.sort((a, b) => distance(a) - distance(b))[0]?.focus();
};

// With a point the popover opens at the cursor, and any click outside it closes it.
export function EmojiPopover({anchor, point, label, className, closeOnScroll, onClose, children}: {
    anchor: HTMLElement; point?: {x: number; y: number}; label: string; className: string; closeOnScroll: boolean; onClose: () => void; children: ReactNode;
}) {
    const root = useRef<HTMLDivElement>(null);
    const [position, setPosition] = useState<{left: number; top: number}>();
    useLayoutEffect(() => {
        const node = root.current;
        if (!node) return;
        const bounds = anchor.getBoundingClientRect(), width = node.offsetWidth, height = node.offsetHeight;
        const above = (point?.y ?? bounds.top) - height - 6;
        setPosition({
            left: Math.max(8, Math.min(point ? point.x - width / 2 : bounds.right - width, window.innerWidth - width - 8)),
            top: above >= 8 ? above : Math.max(8, Math.min((point?.y ?? bounds.bottom) + 6, window.innerHeight - height - 8)),
        });
    }, [anchor, point]);
    useEffect(() => {
        root.current?.querySelector<HTMLButtonElement>("button:not(:disabled)")?.focus({preventScroll: true});
        const outside = (event: PointerEvent) => {if (!root.current?.contains(event.target as Node) && (point || !anchor.contains(event.target as Node))) onClose();};
        const scrolled = (event: Event) => {if (closeOnScroll && !root.current?.contains(event.target as Node)) onClose();};
        window.addEventListener("pointerdown", outside);
        window.addEventListener("resize", onClose);
        window.addEventListener("blur", onClose);
        document.addEventListener("scroll", scrolled, true);
        return () => {window.removeEventListener("pointerdown", outside); window.removeEventListener("resize", onClose); window.removeEventListener("blur", onClose); document.removeEventListener("scroll", scrolled, true);};
    }, [anchor, point, closeOnScroll, onClose]);
    return createPortal(<div ref={root} className={`chat-emoji-popover ${className}`} role="dialog" aria-label={label} style={position ?? {left: 0, top: 0, visibility: "hidden"}} onKeyDown={(event) => {
        if (event.key === "Escape") {event.preventDefault(); event.stopPropagation(); onClose(); anchor.focus();}
        if (event.key.startsWith("Arrow")) {event.preventDefault(); moveFocus(event.currentTarget, event.key);}
    }}>{children}</div>, document.body);
}
