// Keep unacknowledged recording IDs across component unmounts. Releasing an
// old ID is safe: Go only resumes the recording whose ID still matches.
export class CaptureReleases {
    private readonly pending = new Set<number>();
    private readonly inFlight = new Map<number, Promise<void>>();
    private timer: ReturnType<typeof setTimeout> | null = null;
    private readonly recovered = new Set<() => void>();

    constructor(private readonly end: (token: number) => Promise<void>) {}

    onRecovered(listener: () => void): () => void {
        this.recovered.add(listener);
        return () => { this.recovered.delete(listener); };
    }

    release(token: number): Promise<void> {
        this.pending.add(token);
        const existing = this.inFlight.get(token);
        if (existing) return existing;
        const attempt = Promise.resolve().then(() => this.end(token)).then(() => {
            this.pending.delete(token);
            if (!this.pending.size && this.timer !== null) {
                clearTimeout(this.timer);
                this.timer = null;
            }
            if (!this.pending.size) for (const listener of this.recovered) listener();
        }, (error) => {
            if (this.timer === null) {
                this.timer = setTimeout(() => {
                    this.timer = null;
                    for (const id of this.pending) void this.release(id).catch(() => undefined);
                }, 500);
            }
            throw error;
        }).finally(() => this.inFlight.delete(token));
        this.inFlight.set(token, attempt);
        return attempt;
    }
}
