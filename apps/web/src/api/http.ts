import type { LoginInput, LoginMode, WebSession } from "./types";

export class APIError extends Error {
  constructor(readonly status: number, message: string) {
    super(message);
  }
}

export async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, { ...init, credentials: "same-origin", cache: "no-store" });
  if (!response.ok) {
    const problem = await response.json().catch(() => undefined);
    throw new APIError(response.status, problem?.error ?? "Шлюз не ответил");
  }
  return response.status === 204 ? undefined as T : await response.json() as T;
}

export function post<T>(path: string, data: unknown, signal?: AbortSignal) {
  return request<T>(path, {
    method: "POST", signal, headers: { "Content-Type": "application/json" }, body: JSON.stringify(data),
  });
}

export const currentSession = (signal?: AbortSignal) => request<WebSession>("/api/session", { signal });
export const publicConfig = (signal?: AbortSignal) => request<{ loginMode: LoginMode }>("/api/config", { signal });
export const login = (input: LoginInput) =>
  post<WebSession>("/api/session", input, AbortSignal.timeout(8000));
export const logout = () => request<void>("/api/session", { method: "DELETE", signal: AbortSignal.timeout(8000) });
export const answerOffer = (offer: RTCSessionDescriptionInit, signal: AbortSignal) =>
  post<RTCSessionDescriptionInit>("/api/webrtc/offer", offer, signal);

export const messageFor = (error: unknown) => error instanceof Error ? error.message : "Ошибка подключения";
