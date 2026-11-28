import {desktopAPI} from "../../api";

type PreviewJob = {
    key: string; context: string; id: string; subscribers: number; running: boolean;
    promise: Promise<string>; resolve: (data: string) => void; reject: (error: unknown) => void;
};
const jobs = new Map<string, PreviewJob>();
const queue: PreviewJob[] = [];
const cache = new Map<string, {data: string; expires: number}>();
let cacheBytes = 0;
let cacheContext = "";
let working = false;

function remember(key: string, data: string) {
    while (cache.size >= 64 || cacheBytes + data.length > 8 * 1024 * 1024) {
        const oldest = cache.keys().next().value;
        if (oldest === undefined) break;
        cacheBytes -= cache.get(oldest)!.data.length; cache.delete(oldest);
    }
    cache.set(key, {data, expires: Date.now() + 60_000}); cacheBytes += data.length;
}

async function drain() {
    if (working) return;
    working = true;
    try {
        while (queue.length) {
            const job = queue.shift()!;
            job.running = true;
            try {
                let data = "";
                for (let attempt = 0; ; attempt++) {
                    if (job.subscribers === 0) throw new Error("preview-cancelled");
                    try { data = await desktopAPI.filePreview(job.context, job.id); break; }
                    catch (error) {
                        if (attempt >= 2 || !/HTTP 429|preview_busy/.test(String(error))) throw error;
                        await new Promise<void>((resolve) => window.setTimeout(resolve, (attempt + 1) * 800));
                    }
                }
                if (!/^data:image\/jpeg;base64,[A-Za-z0-9+/]+={0,2}$/.test(data) || data.length > 700_000) throw new Error("invalid-preview");
                if (job.subscribers > 0 && cacheContext === job.context) remember(job.key, data);
                job.resolve(data);
            } catch (error) { job.reject(error); }
            finally { jobs.delete(job.key); }
        }
    } finally { working = false; }
}

/** Visible images share one decoder request at a time; hidden queued images are cancelled. */
export function subscribePreview(context: string, id: string): {promise: Promise<string>; release: () => void} {
    if (cacheContext !== context) {cache.clear(); cacheBytes = 0; cacheContext = context;}
    const key = JSON.stringify([context, id]);
    const cached = cache.get(key);
    if (cached && cached.expires > Date.now()) {
        cache.delete(key); cache.set(key, cached);
        return {promise: Promise.resolve(cached.data), release: () => undefined};
    }
    if (cached) {cacheBytes -= cached.data.length; cache.delete(key);}
    let job = jobs.get(key);
    if (!job) {
        if (queue.length >= 32) return {promise: Promise.reject(new Error("preview-queue-full")), release: () => undefined};
        let resolve!: (data: string) => void, reject!: (error: unknown) => void;
        const promise = new Promise<string>((yes, no) => {resolve = yes; reject = no;});
        job = {key, context, id, subscribers: 0, running: false, promise, resolve, reject};
        jobs.set(key, job); queue.push(job);
        queueMicrotask(() => void drain());
    }
    const subscribed = job;
    subscribed.subscribers++;
    let released = false;
    return {promise: subscribed.promise, release: () => {
        if (released) return;
        released = true; subscribed.subscribers--;
        if (subscribed.subscribers === 0 && !subscribed.running) {
            const index = queue.indexOf(subscribed);
            if (index >= 0) queue.splice(index, 1);
            jobs.delete(key); subscribed.reject(new Error("preview-cancelled"));
        }
    }};
}
