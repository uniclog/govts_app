export const wakeLockLadder = [10, 30, 60, 120, 300, 600];

// Mirrors apps/server/internal/voice/hub.go: 2 free wakes per burst, and a burst with
// no lock clears after a quiet period (the first rung once both are spent).
const wakeFreeSends = 2;
const wakeBurstIdleMs = 2 * 60 * 1000;

export type WakeGate = {
    used: number;
    pending: number;
    step: number;
    until: number;
    // at is the last reserved wake or lock update, like attentionAt on the server.
    at: number;
};

export function freshWakeGate(): WakeGate {
    return {used: 0, pending: 0, step: 0, until: 0, at: 0};
}

function holdWake(gate: WakeGate, now: number, seconds: number): number {
    const rung = wakeLockLadder[Math.min(Math.max(gate.step, 0), wakeLockLadder.length - 1)];
    const hold = Math.max(rung, seconds);
    gate.until = now + hold * 1000;
    gate.step = Math.min(gate.step + 1, wakeLockLadder.length - 1);
    gate.used = wakeFreeSends;
    gate.pending = 0;
    gate.at = now;
    return hold;
}

function relaxWake(gate: WakeGate, now: number): void {
    if (gate.until !== 0) {
        if (gate.until > now) return;
        gate.used = 0;
        gate.pending = 0;
        gate.step = 0;
        gate.until = 0;
        return;
    }
    if (gate.used === 0 || gate.pending > 0) return;
    const idle = gate.used >= wakeFreeSends ? wakeLockLadder[0] * 1000 : wakeBurstIdleMs;
    if (now - gate.at >= idle) {
        gate.used = 0;
        gate.step = 0;
    }
}

// wakeDecision reserves a free send or starts/extends the pause.
// A number result is the pause in seconds; the caller must not send.
export function wakeDecision(gate: WakeGate, now: number): "send" | number {
    relaxWake(gate, now);
    if (gate.until > now) return holdWake(gate, now, 0);
    if (gate.used + gate.pending >= wakeFreeSends) return holdWake(gate, now, 0);
    gate.pending += 1;
    gate.at = now;
    return "send";
}

export function wakeSent(gate: WakeGate, now: number): void {
    gate.pending = Math.max(0, gate.pending - 1);
    if (gate.until > now) return;
    gate.used += 1;
}

export function wakeRejected(gate: WakeGate, now: number, seconds: number): number {
    gate.pending = Math.max(0, gate.pending - 1);
    return holdWake(gate, now, seconds);
}

export function wakeFailed(gate: WakeGate): void {
    gate.pending = Math.max(0, gate.pending - 1);
}

export function wakeClock(seconds: number): string {
    const safe = Math.max(0, Math.ceil(seconds));
    const minutes = Math.floor(safe / 60);
    const rest = safe % 60;
    return `${minutes}:${String(rest).padStart(2, "0")}`;
}
