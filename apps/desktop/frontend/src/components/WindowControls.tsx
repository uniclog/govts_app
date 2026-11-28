import {useCallback, useEffect, useState} from "react";
import {Events, Window as WailsWindow} from "@wailsio/runtime";
import {Icon} from "./Icon";
import {useLocale} from "../locales/Locale";
import {rawError} from "../locales";
import "./window-controls.css";

// Keep these outside the main area's inert subtree so settings cannot block
// native window controls. Close still goes through the Go WindowClosing hook.
export function WindowControls({overlayOpen, onError}: {
    overlayOpen: boolean;
    onError: (message: string) => void;
}) {
    const {m} = useLocale();
    const [maximised, setMaximised] = useState(false);
    useEffect(() => {
        let active = true;
        let generation = 0;
        const refresh = () => {
            const current = ++generation;
            void WailsWindow.IsMaximised().then((value) => {
                if (active && current === generation) setMaximised(value);
            }).catch(() => { /* Native state is unavailable in browser previews. */ });
        };
        const off = [Events.Types.Common.WindowMaximise, Events.Types.Common.WindowUnMaximise,
            Events.Types.Common.WindowRestore].map((name) => Events.On(name, refresh));
        window.addEventListener("resize", refresh);
        refresh();
        return () => {
            active = false;
            off.forEach((unsubscribe) => unsubscribe());
            window.removeEventListener("resize", refresh);
        };
    }, []);
    const run = useCallback(async (operation: () => Promise<void>) => {
        try { await operation(); }
        catch (error) { onError(rawError(error)); }
    }, [onError]);
    useEffect(() => {
        const onDoubleClick = (event: MouseEvent) => {
            const target = event.target;
            if (event.button !== 0 || !(target instanceof Element)) return;
            if (!target.closest(".status-bar, .window-overlay-drag")) return;
            // Respect the same no-drag regions as native window dragging.
            if (getComputedStyle(target).getPropertyValue("--wails-draggable").trim() !== "drag") return;
            event.preventDefault();
            void run(() => WailsWindow.ToggleMaximise());
        };
        document.addEventListener("dblclick", onDoubleClick);
        return () => document.removeEventListener("dblclick", onDoubleClick);
    }, [run]);
    return <div className="window-chrome">
        {overlayOpen && <div className="window-overlay-drag" aria-hidden="true"/>}
        <div className="window-controls" role="group" aria-label={m.window.controls}>
            <button type="button" aria-label={m.window.minimise} title={m.window.minimise}
                onClick={() => void run(() => WailsWindow.Minimise())}><Icon name="minimise"/></button>
            <button type="button" aria-label={maximised ? m.window.restore : m.window.maximise}
                title={maximised ? m.window.restore : m.window.maximise}
                onClick={() => void run(() => WailsWindow.ToggleMaximise())}>
                <Icon name={maximised ? "restoreWindow" : "maximise"}/>
            </button>
            <button type="button" className="window-close" aria-label={m.window.close} title={m.window.close}
                onClick={() => void run(() => WailsWindow.Close())}><Icon name="close"/></button>
        </div>
    </div>;
}
