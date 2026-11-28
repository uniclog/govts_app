import {useEffect, useRef} from "react";
import type {ClientEventDTO} from "../../api";
import {useLocale} from "../../locales/Locale";

export function EventPanel({events, active}: {events: ClientEventDTO[]; active: boolean}) {
    const {m, htmlLang} = useLocale();
    const listRef = useRef<HTMLDivElement>(null);
    const stickToBottom = useRef(true);
    useEffect(() => {
        if (active && stickToBottom.current && listRef.current) listRef.current.scrollTop = listRef.current.scrollHeight;
    }, [events, active]);
    return <section className="panel event-panel" aria-label={m.channels.eventLog}>
        <div className="event-list" ref={listRef} onScroll={(event) => {
            const element = event.currentTarget;
            stickToBottom.current = element.scrollHeight - element.scrollTop - element.clientHeight < 12;
        }}>{events.length === 0 ? <div className="empty-event">{m.channels.eventsEmpty}</div> :
            events.map((item) => <div className={`event-row kind-${item.kind}`} key={item.sequence}>
                <time>{new Date(item.time).toLocaleTimeString(htmlLang, {
                    hour: "2-digit", minute: "2-digit", second: "2-digit"
                })}</time>
                <span className="event-marker"/><span>{item.message}</span>
            </div>)}</div>
    </section>;
}
