import {useEffect, useLayoutEffect, useRef, useState, type CSSProperties, type ReactNode} from "react";

// List size limits and its gap below the control, px.
const listMaxHeight = 240;
const listMinHeight = 120;
const listGap = 6;

export type SelectOption = {
    value: string;
    label: string;
    detail?: string;
    selectedLabel?: string;
    fontFamily?: string;
    disabled?: boolean;
};

export function Select({id, label, value, options, disabled = false, onChange, onOpen, hint, labelExtra, className}: {
    id: string;
    label: string;
    value: string;
    options: SelectOption[];
    disabled?: boolean;
    onChange: (value: string) => void;
    // Called each time the list opens, e.g. to refresh its options.
    onOpen?: () => void;
    hint?: string;
    labelExtra?: ReactNode;
    className?: string;
}) {
    const [open, setOpen] = useState(false);
    const [placement, setPlacement] = useState<CSSProperties>();
    const rootRef = useRef<HTMLDivElement>(null);
    const controlRef = useRef<HTMLDivElement>(null);
    const labelId = `${id}-label`;
    const selected = options.find((option) => option.value === value);
    const selectedName = selected?.selectedLabel ?? selected?.label ?? value;

    useEffect(() => {
        if (disabled) setOpen(false);
    }, [disabled]);

    // The list always opens downward and is fixed to the window, so a
    // scrolling parent (a dialog, a settings page) never clips it. It stays
    // inside its parent's top layer, e.g. above a modal dialog. Scrolling or
    // resizing moves it with the control.
    useLayoutEffect(() => {
        if (!open) { setPlacement(undefined); return; }
        const place = () => {
            const control = controlRef.current?.getBoundingClientRect();
            if (!control) return;
            const top = control.bottom + listGap;
            setPlacement({position: "fixed", top, left: control.left, right: "auto", width: control.width,
                maxHeight: Math.max(listMinHeight, Math.min(listMaxHeight, window.innerHeight - top - listGap))});
        };
        place();
        window.addEventListener("resize", place);
        window.addEventListener("scroll", place, true);
        return () => {
            window.removeEventListener("resize", place);
            window.removeEventListener("scroll", place, true);
        };
    }, [open]);

    useEffect(() => {
        if (!open) return;
        const closeOnEscape = (event: KeyboardEvent) => {
            if (event.key !== "Escape") return;
            event.preventDefault();
            event.stopPropagation();
            setOpen(false);
        };
        const closeOnPointer = (event: PointerEvent) => {
            if (!rootRef.current?.contains(event.target as Node)) setOpen(false);
        };
        window.addEventListener("keydown", closeOnEscape, true);
        window.addEventListener("pointerdown", closeOnPointer);
        return () => {
            window.removeEventListener("keydown", closeOnEscape, true);
            window.removeEventListener("pointerdown", closeOnPointer);
        };
    }, [open]);

    const choose = (next: string) => {
        setOpen(false);
        if (next !== value) onChange(next);
    };

    return <div className={className ? `app-select ${className}` : "app-select"} ref={rootRef}>
        {labelExtra
            ? <div className="app-select-heading"><span className="setting-label" id={labelId}>{label}</span>{labelExtra}</div>
            : <span className="setting-label" id={labelId}>{label}</span>}
        <div className="app-select-control" ref={controlRef}>
            <button id={id} className="app-select-trigger" type="button" disabled={disabled}
                    aria-labelledby={`${labelId} ${id}`} aria-haspopup="listbox" aria-expanded={open}
                    onClick={() => { if (!open) onOpen?.(); setOpen(!open); }}><span style={selected?.fontFamily ? {fontFamily: `"${selected.fontFamily}", "Noto Sans", sans-serif`} : undefined}>{selectedName}</span></button>
            {open && <div className="app-select-options" style={placement} role="listbox" aria-labelledby={labelId}>
                {options.map((option) => <button type="button" role="option" aria-selected={option.value === value} disabled={option.disabled}
                                                  className={option.value === value ? "selected" : ""}
                                                  style={option.fontFamily ? {fontFamily: `"${option.fontFamily}", "Noto Sans", sans-serif`} : undefined}
                                                  onClick={() => choose(option.value)}
                                                  key={option.value}>{option.label}{option.detail ?
                    <small>{option.detail}</small> : null}</button>)}
            </div>}
        </div>
        {hint && <small>{hint}</small>}
    </div>;
}
