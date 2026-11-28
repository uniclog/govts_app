import { useLayoutEffect, useRef, useState } from "react";
import type { CSSProperties, KeyboardEvent, PointerEvent } from "react";
import { defaultWorkspaceWidths, workspaceWidths, type WorkspaceSide, type WorkspaceWidths } from "../../../../shared-ui/workspace-widths";

const storageKey = "sonoryx.web.workspace-widths";
const panels = {
  browser: { id: "channel-browser-pane", label: "Ширина списка каналов", direction: 1 },
  chat: { id: "chat-pane", label: "Ширина чата", direction: -1 },
} as const;

function readWidths(): WorkspaceWidths {
  try {
    const saved = JSON.parse(localStorage.getItem(storageKey) ?? "null");
    if (Number.isFinite(saved?.browser) && saved.browser > 0 && Number.isFinite(saved?.chat) && saved.chat > 0) {
      return { browser: saved.browser, chat: saved.chat };
    }
  } catch { /* Use default widths when storage is unavailable. */ }
  return defaultWorkspaceWidths;
}

function saveWidths(widths: WorkspaceWidths) {
  try { localStorage.setItem(storageKey, JSON.stringify(widths)); } catch { /* Keep this tab's widths. */ }
}

export function useWorkspaceResize() {
  const layoutRef = useRef<HTMLDivElement>(null);
  const drag = useRef<{ side: WorkspaceSide; pointerId: number; startX: number; startWidth: number }>();
  const [resizing, setResizing] = useState(false);
  const [availableWidth, setAvailableWidth] = useState(() => window.innerWidth);
  const [preferred, setPreferred] = useState(readWidths);
  const latest = useRef(preferred);

  useLayoutEffect(() => {
    const element = layoutRef.current;
    if (!element) return;
    const measure = () => setAvailableWidth(element.clientWidth);
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, []);

  const { widths, minimums, maximums } = workspaceWidths(availableWidth, preferred);
  const setWidth = (side: WorkspaceSide, width: number, persist: boolean) => {
    const next = { ...latest.current, [side]: width };
    latest.current = next;
    setPreferred(next);
    if (persist) saveWidths(next);
  };
  const resize = (side: WorkspaceSide, width: number, persist = true) => {
    setWidth(side, Math.max(minimums[side], Math.min(width, maximums[side])), persist);
  };
  const finishDrag = (event: PointerEvent<HTMLDivElement>) => {
    if (drag.current?.pointerId !== event.pointerId) return;
    drag.current = undefined;
    setResizing(false);
    saveWidths(latest.current);
  };

  const separatorProps = (side: WorkspaceSide) => ({
    role: "separator" as const,
    tabIndex: 0,
    "aria-orientation": "vertical" as const,
    "aria-label": panels[side].label,
    "aria-controls": panels[side].id,
    "aria-valuemin": Math.round(minimums[side]),
    "aria-valuemax": Math.round(maximums[side]),
    "aria-valuenow": Math.round(widths[side]),
    "aria-valuetext": `${Math.round(widths[side])} пикселей`,
    onPointerDown: (event: PointerEvent<HTMLDivElement>) => {
      if (event.button !== 0 || drag.current) return;
      event.preventDefault();
      event.currentTarget.focus();
      event.currentTarget.setPointerCapture(event.pointerId);
      drag.current = { side, pointerId: event.pointerId, startX: event.clientX, startWidth: widths[side] };
      setResizing(true);
    },
    onPointerMove: (event: PointerEvent<HTMLDivElement>) => {
      const current = drag.current;
      if (current?.pointerId !== event.pointerId || current.side !== side) return;
      resize(side, current.startWidth + (event.clientX - current.startX) * panels[side].direction, false);
    },
    onPointerUp: finishDrag,
    onPointerCancel: finishDrag,
    onLostPointerCapture: finishDrag,
    onDoubleClick: () => setWidth(side, defaultWorkspaceWidths[side], true),
    onKeyDown: (event: KeyboardEvent<HTMLDivElement>) => {
      if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
      event.preventDefault();
      const direction = (event.key === "ArrowRight" ? 1 : -1) * panels[side].direction;
      resize(side, widths[side] + direction * (event.shiftKey ? 30 : 10));
    },
  });

  return { layoutRef, resizing, separatorProps, style: {
    "--channel-browser-width": `${widths.browser}px`,
    "--channel-chat-width": `${widths.chat}px`,
  } as CSSProperties };
}
