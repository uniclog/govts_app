import {useEffect, useRef, useState} from "react";
import {createPortal} from "react-dom";
import {useLocale} from "../../locales/Locale";
import {desktopAPI} from "../../api";

export function PhotoViewer({context, id, thumbnail, name, onClose}: {context: string; id: string; thumbnail: string; name: string; onClose: () => void}) {
    const {m} = useLocale();
    const photo = useRef<HTMLButtonElement>(null);
    const image = useRef<HTMLImageElement>(null);
    const copyButton = useRef<HTMLButtonElement>(null);
    const retryButton = useRef<HTMLButtonElement>(null);
    const [original, setOriginal] = useState("");
    const [ready, setReady] = useState(false);
    const [loadFailed, setLoadFailed] = useState(false);
    const [attempt, setAttempt] = useState(0);
    const [menu, setMenu] = useState<{x: number; y: number}>();
    const [status, setStatus] = useState("");
    const [failed, setFailed] = useState(false);
    useEffect(() => {
        let disposed = false;
        setOriginal(""); setReady(false); setLoadFailed(false); setMenu(undefined); setStatus("");
        const request = desktopAPI.fileOriginalImage(context, id);
        void request.then((data) => {
            if (!/^data:image\/(png|jpeg|webp);base64,/.test(data)) throw new Error("Invalid original image");
            if (!disposed) setOriginal(data);
        }).catch(() => {if (!disposed) setLoadFailed(true);});
        return () => {disposed = true; void request.cancel();};
    }, [context, id, attempt]);
    useEffect(() => {
        const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
        photo.current?.focus();
        return () => {if (previous?.isConnected) previous.focus();};
    }, []);
    useEffect(() => {if (menu) copyButton.current?.focus();}, [menu]);
    const dismissMenu = () => {setMenu(undefined); photo.current?.focus();};
    const copy = () => {
        dismissMenu(); setStatus(""); setFailed(false);
        try {
            if (!navigator.clipboard?.write || typeof ClipboardItem === "undefined") throw new Error("Clipboard unavailable");
            const img = image.current;
            if (!ready || !img?.naturalWidth) throw new Error("Image unavailable");
            const canvas = document.createElement("canvas");
            canvas.width = img.naturalWidth; canvas.height = img.naturalHeight;
            const ctx = canvas.getContext("2d");
            if (!ctx) throw new Error("Canvas unavailable");
            ctx.drawImage(img, 0, 0);
            const png = new Promise<Blob>((resolve, reject) => canvas.toBlob((blob) => blob ? resolve(blob) : reject(new Error("Image encoding failed")), "image/png"));
            void navigator.clipboard.write([new ClipboardItem({"image/png": png})])
                .then(() => setStatus(m.files.copied))
                .catch(() => {setFailed(true); setStatus(m.files.copyFailed);});
        } catch {setFailed(true); setStatus(m.files.copyFailed);}
    };
    return createPortal(<div role="dialog" aria-modal="true" aria-label={name} className="file-preview"
        onClick={(event) => {if (event.target === event.currentTarget) {if (menu) dismissMenu(); else onClose();}}}
        onKeyDown={(event) => {
            if (event.key === "Escape") {event.preventDefault(); event.stopPropagation(); if (menu) dismissMenu(); else onClose();}
            if (event.key === "Tab") {
                event.preventDefault();
                if (menu) copyButton.current?.focus();
                else if (loadFailed && document.activeElement === photo.current) retryButton.current?.focus();
                else photo.current?.focus();
            }
        }}>
        <button ref={photo} type="button" className="file-preview-photo" aria-label={m.files.close} onClick={onClose}
            onContextMenu={(event) => {
                event.preventDefault(); event.stopPropagation(); setStatus("");
                if (!ready) return;
                const bounds = event.currentTarget.getBoundingClientRect();
                const x = event.clientX || bounds.left + 16, y = event.clientY || bounds.top + 16;
                setMenu({x: Math.max(8, Math.min(x, window.innerWidth - 184)), y: Math.max(8, Math.min(y, window.innerHeight - 52))});
            }}>
            <img key={original || thumbnail} ref={image} src={original || thumbnail} alt={name} draggable={false}
                onLoad={() => {if (original) setReady(true);}}
                onError={() => {if (original) {setReady(false); setLoadFailed(true);}}}/>
        </button>
        {!ready && <div className="file-copy-status" role={loadFailed ? "alert" : "status"}>
            {loadFailed ? m.files.originalFailed : m.files.originalLoading}
            {loadFailed && <button ref={retryButton} className="file-action-button" type="button" onClick={() => setAttempt((value) => value + 1)}>{m.chat.retry}</button>}
        </div>}
        {menu && <div className="file-photo-menu" role="menu" aria-label={m.files.preview} style={{left: menu.x, top: menu.y}}>
            <button ref={copyButton} role="menuitem" type="button" onClick={copy}>{m.files.copyImage}</button>
        </div>}
        {status && <div className={`file-copy-status${failed ? " is-error" : ""}`} role={failed ? "alert" : "status"}>{status}</div>}
    </div>, document.body);
}
