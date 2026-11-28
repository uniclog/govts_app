import {useEffect} from "react";
import type {ReactNode} from "react";
import {Icon} from "../../components/Icon";
import {useLocale} from "../../locales/Locale";

export function SettingsOverlay({onClose, children, label, closeLabel}: {onClose: () => void; children: ReactNode; label?: string; closeLabel?: string}) {
    const {m} = useLocale();
    label ??= m.settings.title;
    closeLabel ??= m.settings.close;
    useEffect(() => {
        const onKey = (event: KeyboardEvent) => {
            if (event.key !== "Escape" || event.defaultPrevented) return;
            const target = event.target;
            if (target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement || target instanceof HTMLSelectElement) return;
            onClose();
        };
        window.addEventListener("keydown", onKey);
        return () => window.removeEventListener("keydown", onKey);
    }, [onClose]);
    return <div className="settings-overlay" onMouseDown={(event) => {
        if (event.button === 0 && event.target === event.currentTarget) onClose();
    }}>
        <div className="settings-window" role="dialog" aria-modal="true" aria-label={label}>
            <button type="button" className="settings-close" title={closeLabel} aria-label={closeLabel} autoFocus onClick={onClose}><Icon name="close"/></button>
            {children}
        </div>
    </div>;
}
