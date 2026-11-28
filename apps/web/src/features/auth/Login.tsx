import { useState, type FormEvent } from "react";
import { login, messageFor } from "../../api/http";
import type { LoginMode, WebSession } from "../../api/types";
import { Icon } from "../../components/Icon";

const nicknameKey = "sonoryx.web.nickname";

function savedNickname() {
  try { return localStorage.getItem(nicknameKey) ?? ""; }
  catch { return ""; }
}

export function Login({ mode, connected, notice }: { mode: LoginMode; connected: (session: WebSession) => void; notice: string }) {
  const [nickname, setNickname] = useState(savedNickname);
  const [account, setAccount] = useState("");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const nicknameMode = mode === "nickname";
  const tooLong = new TextEncoder().encode(nickname.trim().normalize("NFKC")).length > 64;
  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      const session = await login(nicknameMode ? { nickname: nickname.trim() } : { account: account.trim(), accessCode: code.trim() });
      if (nicknameMode) {
        try { localStorage.setItem(nicknameKey, nickname.trim()); } catch { /* Keep the input for this page. */ }
      }
      connected(session);
    } catch (reason) {
      setError(messageFor(reason));
      setBusy(false);
    }
  }
  return (
    <main className="lobby">
      <header className="titlebar"><img src="/sonoryx.svg" alt="" /><strong>SRX — Подключение</strong></header>
      <div className="lobby-layout">
        <section className="connection-hero">
          <div className="connection-rings" aria-hidden="true" />
          <div className="connection-content">
            <div className="connection-brand"><img src="/sonoryx.svg" alt="" /><strong>SRX</strong></div>
            <h1>Голос, который<br />просто работает</h1>
            <p className="lead">{nicknameMode ? "Введите никнейм, чтобы подключиться к голосовым каналам и чату." : "Войдите с аккаунтом и кодом доступа, которые выдал оператор."}</p>
            <form className="connection-form" onSubmit={(event) => void submit(event)}>
              {nicknameMode
                ? <label>Никнейм<span className="connection-input"><Icon name="users" /><input required autoFocus autoComplete="nickname" placeholder="Ваш никнейм"
                    disabled={busy} maxLength={64} value={nickname} onChange={(event) => setNickname(event.target.value)} /></span></label>
                : <>
                    <label>Аккаунт<span className="connection-input"><Icon name="users" /><input required autoFocus autoComplete="username" maxLength={40} disabled={busy} value={account} onChange={(event) => setAccount(event.target.value)} /></span></label>
                    <label>Код доступа<span className="connection-input"><Icon name="lock" /><input required autoComplete="current-password" maxLength={128} disabled={busy} type="password" value={code} onChange={(event) => setCode(event.target.value)} /></span></label>
                  </>}
              {(error || notice) && <p className="notice" role="status">{error || notice}</p>}
              {nicknameMode && tooLong && <p className="notice">Никнейм должен быть не длиннее 64 байт</p>}
              <button className="primary" type="submit" disabled={busy || (nicknameMode ? !nickname.trim() || tooLong : !account.trim() || !code.trim())}>
                <Icon name="arrowRight" />{busy ? "Подключение…" : "Подключиться"}
              </button>
            </form>
          </div>
        </section>
      </div>
    </main>
  );
}
