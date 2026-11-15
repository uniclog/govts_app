import {wakeClock} from "./wakeLimit";
import {useLocale} from "../../locales/Locale";

export function WakeNotice({until, now, label}: {until: number; now: number; label: string}) {
    const {m} = useLocale();
    const left = wakeClock(Math.max(1, (until - now) / 1000));
    return <div className="wake-notice">
        <span className="wake-notice-icon" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round"><circle cx="12" cy="12" r="8"/><path d="M12 8v4.5l3 2"/></svg></span>
        <div>
            <strong>{m.people.wakeBlockedTitle}</strong>
            <p>{m.people.wakeBlockedBody}</p>
            <p className="wake-notice-time" aria-hidden="true">{m.people.wakeBlockedIn(left)}</p>
            <span className="wake-notice-live" role="status">{m.people.wakeBlockedTitle}. {m.people.wakeBlockedBody} {m.people.wakeBlockedIn(label || left)}</span>
        </div>
    </div>;
}
