import { useCallback, useEffect, useRef, useState } from "react";
import { sha256 } from "@noble/hashes/sha2.js";
import { bytesToHex } from "@noble/hashes/utils.js";
import { messageFor, request } from "../../api/http";
import type { ChatTarget } from "../../api/types";

type UploadState = "hashing" | "uploading" | "verifying" | "paused" | "error" | "ready";
type Status = { id: string; state: string; offset: number; attachmentId: string };
interface Upload {
  requestId: string;
  file: File;
  state: UploadState;
  offset: number;
  id?: string;
  digest?: string;
  attachmentId?: string;
  error?: string;
}
interface Capabilities {
  maxFileBytes: number;
  chunkBytes: number;
  maxAttachments: number;
  uploadsEnabled: boolean;
}

const uploadURL = (id: string) => "/api/files/uploads/" + id;

async function digestFile(file: File, signal: AbortSignal) {
  const hash = sha256.create();
  const reader = file.stream().getReader();
  try {
    for (;;) {
      signal.throwIfAborted();
      const { value, done } = await reader.read();
      if (done) break;
      hash.update(value);
    }
    signal.throwIfAborted();
    return bytesToHex(hash.digest());
  } finally {
    if (signal.aborted) await reader.cancel();
    reader.releaseLock();
  }
}

function delay(signal: AbortSignal) {
  return new Promise<void>((resolve, reject) => {
    const abort = () => { window.clearTimeout(timer); reject(signal.reason); };
    const timer = window.setTimeout(() => { signal.removeEventListener("abort", abort); resolve(); }, 1000);
    if (signal.aborted) abort();
    else signal.addEventListener("abort", abort, { once: true });
  });
}

export function useUploads(target: ChatTarget, active: boolean) {
  const [capabilities, setCapabilities] = useState<Capabilities>();
  const [uploads, setUploads] = useState<Upload[]>([]);
  const [error, setError] = useState("");
  const controllers = useRef(new Map<string, AbortController>());

  useEffect(() => {
    if (!active) return;
    const controller = new AbortController();
    void request<Capabilities>("/api/files/capabilities", { signal: controller.signal })
      .then(setCapabilities).catch(() => undefined);
    return () => controller.abort();
  }, [active, target]);

  useEffect(() => {
    const activeControllers = controllers.current;
    return () => { for (const controller of activeControllers.values()) controller.abort(); };
  }, []);

  const update = useCallback((requestId: string, change: Partial<Upload>) => {
    setUploads((current) => current.map((upload) => upload.requestId === requestId ? { ...upload, ...change } : upload));
  }, []);

  async function start(upload: Upload) {
    if (!capabilities || controllers.current.has(upload.requestId)) return;
    const controller = new AbortController();
    const signal = controller.signal;
    controllers.current.set(upload.requestId, controller);
    update(upload.requestId, { state: upload.digest ? "uploading" : "hashing", error: undefined });
    try {
      const digest = upload.digest ?? await digestFile(upload.file, signal);
      update(upload.requestId, { digest, state: "uploading" });
      let status = upload.id
        ? await request<Status>(uploadURL(upload.id), { signal })
        : await request<Status>("/api/files/uploads", {
            method: "POST", signal, headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ ...target, requestId: upload.requestId, name: upload.file.name, size: upload.file.size, sha256: digest }),
          });
      const id = status.id;
      update(upload.requestId, { id, offset: status.offset });
      while (status.state === "uploading" && status.offset < upload.file.size) {
        const from = status.offset;
        const chunk = upload.file.slice(from, Math.min(from + capabilities.chunkBytes, upload.file.size));
        status = await request<Status>(uploadURL(id), {
          method: "PATCH", signal, headers: { "Upload-Offset": String(from) }, body: chunk,
        });
        if (status.offset !== from + chunk.size) throw new Error("Sonoryx вернул неверное смещение загрузки");
        update(upload.requestId, { offset: status.offset });
      }
      if (status.state === "uploading") {
        status = await request<Status>(uploadURL(id) + "/complete", { method: "POST", signal });
      }
      if (status.state === "verifying") update(upload.requestId, { state: "verifying" });
      while (status.state === "verifying") {
        await delay(signal);
        status = await request<Status>(uploadURL(id), { signal });
      }
      if ((status.state !== "ready" && status.state !== "attached") || !/^[1-9][0-9]*$/.test(status.attachmentId)) {
        throw new Error("Sonoryx не подтвердил вложение");
      }
      update(upload.requestId, { attachmentId: status.attachmentId, state: "ready", offset: upload.file.size });
    } catch (reason) {
      update(upload.requestId, signal.aborted ? { state: "paused" } : { state: "error", error: messageFor(reason) });
    } finally {
      controllers.current.delete(upload.requestId);
    }
  }

  function add(files: FileList | File[]) {
    if (!capabilities?.uploadsEnabled) return;
    setError("");
    if (files.length > capabilities.maxAttachments - uploads.length) {
      setError("К одному сообщению можно прикрепить не больше " + capabilities.maxAttachments + " файлов");
      return;
    }
    const next: Upload[] = [];
    for (const file of files) {
      if (file.size > capabilities.maxFileBytes) {
        setError("Файл превышает лимит сервера: " + file.name);
        continue;
      }
      next.push({ file, requestId: crypto.randomUUID().replaceAll("-", ""), state: "hashing", offset: 0 });
    }
    setUploads((current) => [...current, ...next]);
    for (const upload of next) void start(upload);
  }

  function pause(upload: Upload) {
    controllers.current.get(upload.requestId)?.abort();
  }

  async function remove(upload: Upload) {
    pause(upload);
    setUploads((current) => current.filter((item) => item.requestId !== upload.requestId));
    if (upload.id) {
      await request<void>(uploadURL(upload.id), { method: "DELETE" }).catch(() => undefined);
    }
  }

  const ready = uploads.flatMap((upload) => upload.state === "ready" && upload.attachmentId ? [upload.attachmentId] : []);
  const clearSent = (ids: string[]) => setUploads((current) => current.filter(({ attachmentId }) => !attachmentId || !ids.includes(attachmentId)));
  return { uploads, enabled: capabilities?.uploadsEnabled === true, ready, error, add, pause, start, remove, clearSent };
}
