import {useEffect, useLayoutEffect, useRef, useState} from "react";
import {PhotoViewer} from "./PhotoViewer";
import {desktopAPI, type FileAttachment, type FileTask} from "../../api";
import {useLocale} from "../../locales/Locale";
import type {ChatTarget} from "./chatStore";
import {Icon} from "../../components/Icon";
import {subscribePreview} from "./filePreviews";

export const fileSize = (bytes: number) => {
    if (!Number.isFinite(bytes) || bytes < 0) return "—";
    const units = ["B", "KiB", "MiB", "GiB", "TiB"];
    const unit = bytes < 1024 ? 0 : Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
    return `${unit === 0 ? bytes : (bytes / 1024 ** unit).toFixed(1)} ${units[unit]}`;
};

function AttachmentName({name}: {name: string}) {
    const root = useRef<HTMLElement>(null);
    const measure = useRef<HTMLSpanElement>(null);
    const [display, setDisplay] = useState(name);
    useLayoutEffect(() => {
        let live = true;
        const fit = () => {
            const node = measure.current;
            if (!node || !root.current?.clientWidth) return;
            const height = parseFloat(getComputedStyle(node).lineHeight) * 2 + .5;
            const fits = (text: string) => {node.textContent = text; return node.getBoundingClientRect().height <= height;};
            if (fits(name)) {setDisplay(name); return;}
            const chars = Array.from(name);
            const shortened = (keep: number) => {
                const tail = Math.max(1, Math.ceil(keep * .45));
                return chars.slice(0, keep - tail).join("") + "…" + chars.slice(-tail).join("");
            };
            let low = 1, high = chars.length - 1, best = shortened(1);
            while (low <= high) {
                const middle = Math.floor((low + high) / 2), text = shortened(middle);
                if (fits(text)) {best = text; low = middle + 1;} else high = middle - 1;
            }
            setDisplay(best);
        };
        fit();
        const observer = new ResizeObserver(fit);
        if (root.current) observer.observe(root.current);
        void document.fonts.ready.then(() => {if (live) fit();});
        return () => {live = false; observer.disconnect();};
    }, [name]);
    return <strong ref={root} className="file-card-name" title={name} aria-label={name}>
        <span className="file-name-intrinsic" aria-hidden="true">{name}</span>
        <span className="file-name-text" aria-hidden="true">{display}</span>
        <span ref={measure} className="file-name-measure" aria-hidden="true"/>
    </strong>;
}
export const fileBusy = (error: unknown) => /HTTP 429|\b(?:busy|preview_busy|verification_busy)\b/.test(String(error));
const metadataCache = new Map<string, Promise<FileAttachment>>();
let metadataContext = "";
const metadataBatches = new Map<string, Map<string, {resolve: (file: FileAttachment) => void; reject: (error: unknown) => void}>>();
async function flushMetadata(context: string) {
    const batch = metadataBatches.get(context); if (!batch) return;
    // New callers are collected into the next bounded batch.
    metadataBatches.delete(context);
    const entries = [...batch];
    for (let offset = 0; offset < entries.length; offset += 64) {
        const part = entries.slice(offset, offset + 64);
        try {
            const files = await desktopAPI.fileMetadata(context, part.map(([id]) => id));
            const byID = new Map(files.map((file) => [file.id, file]));
            for (const [id, pending] of part) {const file = byID.get(id); if (file) pending.resolve(file); else {if(metadataContext === context) metadataCache.delete(id); pending.reject(new Error("Attachment unavailable"));}}
        } catch(e) {for (const [id, pending] of part) {if (metadataContext === context) metadataCache.delete(id); pending.reject(e);}}
    }
}
export function attachmentMetadata(context: string, id: string, retry: boolean): Promise<FileAttachment> {
    if (metadataContext !== context) {metadataCache.clear(); metadataContext = context;}
    if (retry && !metadataBatches.get(context)?.has(id)) metadataCache.delete(id);
    const cached = metadataCache.get(id); if (cached) return cached;
    const promise = new Promise<FileAttachment>((resolve, reject) => {
        let batch = metadataBatches.get(context);
        if (!batch) {batch = new Map(); metadataBatches.set(context, batch); queueMicrotask(() => void flushMetadata(context));}
        batch.set(id, {resolve, reject});
    });
    if (metadataCache.size >= 512) metadataCache.delete(metadataCache.keys().next().value!);
    metadataCache.set(id, promise);
    return promise;
}
export function useTransfers(context: string, connected: boolean, active: boolean) {
    const [tasks, setTasks] = useState<FileTask[]>([]);
    useEffect(() => {
        if (!active) return;
        let disposed = false, busy = false;
        const poll = async () => { if (busy) return; busy = true; try { const list = await desktopAPI.fileQueue(context); if (!disposed) setTasks(list ?? []); } catch { /* Existing queue remains visible while disconnected. */ } finally {busy = false;} };
        void poll(); const timer = window.setInterval(() => void poll(), 1000);
        return () => {disposed = true; window.clearInterval(timer);};
    }, [context, connected, active]);
    return tasks;
}

export function TransferQueue({context, tasks, report}: {context: string; tasks: FileTask[]; report: (error: string) => void}) {
    const {m} = useLocale();
    const control = async (id: string, action: string) => {try {await desktopAPI.fileControl(context, id, action);} catch(e) {report(fileBusy(e) ? m.files.busy : m.files.requestFailed);}};
    const visibleTasks = tasks.filter((task) => task.state !== "done" && task.state !== "cancelled");
    if (!visibleTasks.length) return null;
    return <div className="file-queue" aria-label={m.files.queue}>
        {visibleTasks.map((task) => <TransferCard key={task.id} context={context} task={task} control={control}/>)}</div>;
}

function TransferCard({context, task, control}: {context: string; task: FileTask; control: (id: string, action: string) => Promise<void>}) {
    const {m} = useLocale();
    const [thumbnail, setThumbnail] = useState("");
    const ready = task.state === "ready";
    const running = task.state === "running" || task.state === "queued" || task.state === "verifying";
    const photo = /\.(png|jpe?g|webp)$/i.test(task.name);
    const state = m.files.states[task.state as keyof typeof m.files.states] ?? task.state;
    useEffect(() => {
        setThumbnail("");
        if (!photo || !ready || !task.attachmentId || task.attachmentId === "0") return;
        let disposed = false;
        const subscription = subscribePreview(context, task.attachmentId);
        void subscription.promise.then((data) => {if (!disposed) setThumbnail(data);}).catch(() => undefined);
        return () => {disposed = true; subscription.release();};
    }, [context, task.attachmentId, photo, ready]);
    const cancel = ready && task.direction === "upload" ? m.files.removeAttachment : m.files.cancel;
    return <div className={`file-transfer${ready ? " is-ready" : ""}${task.error ? " has-error" : ""}`}>
        <span className="file-transfer-icon" aria-hidden="true">{thumbnail ? <img src={thumbnail} alt=""/> : <Icon name={task.direction !== "upload" ? "download" : "file"}/>}</span>
        <div className="file-transfer-info">
            <strong title={`${task.name}${photo ? `\n${m.files.originalMetadata}` : ""}`}>{task.name}</strong>
            <small title={task.error || state}>
                {ready ? <><span className="file-transfer-ready" role="img" aria-label={state}><Icon name="check"/></span>{fileSize(task.size)}</>
                    : <>{fileSize(task.offset)} / {fileSize(task.size)} · <span role={task.error ? "alert" : undefined}>{state}</span></>}
            </small>
        </div>
        <div className="file-transfer-actions">
            {!ready && <button type="button" title={running ? m.files.pause : m.files.resume} aria-label={`${running ? m.files.pause : m.files.resume}: ${task.name}`} onClick={() => void control(task.id, running ? "pause" : "resume")}><Icon name={running ? "pause" : "play"}/></button>}
            <button type="button" title={cancel} aria-label={`${cancel}: ${task.name}`} onClick={() => void control(task.id, "cancel")}><Icon name="close"/></button>
        </div>
        {!ready && <progress value={task.state === "verifying" ? undefined : task.offset} max={Math.max(1, task.size)} aria-label={task.name}/>}
    </div>;
}

export async function stageFiles(context: string, target: ChatTarget, files: File[], maxBytes: number) {
    if (files.length > 4) throw new Error("Maximum 4 attachments");
    for (const file of files) {
        if (file.size > maxBytes) throw new Error(`File too large: ${file.name}`);
        const id = await desktopAPI.fileStageCreate(context);
        try {
            for (let offset = 0; offset < file.size; offset += 256 * 1024) {
                const bytes = new Uint8Array(await file.slice(offset, offset + 256 * 1024).arrayBuffer());
                let binary = ""; for (let i = 0; i < bytes.length; i += 8192) binary += String.fromCharCode(...bytes.subarray(i, i + 8192));
                await desktopAPI.fileStageWrite(id, offset, btoa(binary));
            }
            await desktopAPI.fileStageFinish(context, target.kind, target.id, id, file.name);
        } catch(e) {await desktopAPI.fileStageCancel(id).catch(() => undefined); throw e;}
    }
}

export function AttachmentCard({context, id}: {context: string; id: string}) {
    const {m} = useLocale();
    const [file, setFile] = useState<FileAttachment>();
    const [error, setError] = useState("");
    const [preview, setPreview] = useState("");
    const [previewState, setPreviewState] = useState<"idle" | "loading" | "ready" | "error">("idle");
    const [previewAttempt, setPreviewAttempt] = useState(0);
    const [visible, setVisible] = useState(false);
    const card = useRef<HTMLDivElement>(null);
    const [expanded, setExpanded] = useState(false);
    const [attempt, setAttempt] = useState(0);
    const save = () => {setError(""); void desktopAPI.fileSave(context, id).catch((e) => setError(fileBusy(e) ? m.files.busy : m.files.requestFailed));};
    useEffect(() => {
        let disposed = false;
        setFile(undefined); setPreview(""); setError("");
        void attachmentMetadata(context, id, attempt > 0).then((next) => {
            if (!disposed) {
                setFile(next);
                if (next.deleted) {setExpanded(false); setPreview("");}
            }
        }).catch((e) => {if (!disposed) setError(fileBusy(e) ? m.files.busy : m.files.requestFailed);});
        return () => {disposed = true;};
    }, [context, id, attempt, m.files.busy, m.files.requestFailed]);
    const image = !!file && !file.deleted && /\.(png|jpe?g|webp)$/i.test(file.name);
    useEffect(() => {
        setVisible(false);
        if (!image || !card.current) return;
        if (typeof IntersectionObserver === "undefined") {setVisible(true); return;}
        const observer = new IntersectionObserver(([entry]) => setVisible(entry.isIntersecting), {root: card.current.closest(".chat-history"), rootMargin: "120px", threshold: 0.01});
        observer.observe(card.current);
        return () => observer.disconnect();
    }, [context, id, image]);
    const previewWanted = image && (visible || expanded);
    useEffect(() => {
        setPreview(""); setPreviewState("idle");
        if (!previewWanted) return;
        let disposed = false;
        setPreviewState("loading");
        const subscription = subscribePreview(context, id);
        void subscription.promise.then((data) => {if (!disposed) {setPreview(data); setPreviewState("ready");}})
            .catch(() => {if (!disposed) setPreviewState("error");});
        return () => {disposed = true; subscription.release();};
    }, [context, id, previewWanted, previewAttempt]);
    return <div ref={card} className={`file-card${image ? " file-card-image" : ""}${file?.deleted ? " is-deleted" : ""}`}>
        {image && <div className="file-inline-preview">
            {preview ? <button className="file-thumbnail" type="button" aria-label={`${m.files.preview}: ${file.name}`} onClick={() => setExpanded(true)}>
                <img src={preview} alt={file.name} decoding="async"/>
            </button> : <div className="file-thumbnail-placeholder" aria-busy={previewState === "loading"}>
                <Icon name="file"/><span>{previewState === "error" ? m.files.previewUnavailable : m.files.previewLoading}</span>
                {previewState === "error" && <button className="file-action-button" type="button" onClick={() => setPreviewAttempt((value) => value + 1)}>{m.chat.retry}</button>}
            </div>}
        </div>}
        {image && <div className="file-image-toolbar">
            <span className="file-image-name" title={`${file.name} · ${fileSize(file.size)}`}>{file.name}</span>
            <button className="file-image-download" type="button" title={m.files.save} aria-label={`${m.files.save}: ${file.name}`} onClick={save}><Icon name="download"/></button>
        </div>}
        {!image && <div className="file-card-summary"><Icon name="file"/>
            <div className="file-card-info"><AttachmentName name={file?.name || `#${id}`}/>
                {file && <small>{fileSize(file.size)}</small>}
            </div>
        </div>}
        {file?.deleted && <span className="file-status-badge">{m.files.deleted}</span>}
        {file && !file.deleted && !image && <button className="file-download-button" type="button" title={m.files.save} aria-label={`${m.files.save}: ${file.name}`} onClick={save}><Icon name="download"/></button>}
        {error && <div className="file-card-error"><small role="alert">{error}</small><button className="file-action-button" type="button" onClick={() => setAttempt((v) => v + 1)}>{m.chat.retry}</button></div>}
        {expanded && preview && <PhotoViewer context={context} id={id} thumbnail={preview} name={file?.name ?? m.files.preview} onClose={() => setExpanded(false)}/>}
    </div>;
}
