import type { ReactNode } from "react";

const paths = {
  voice: <><circle cx="5" cy="12" r="1" fill="currentColor"/><path d="M10 8a6 6 0 0 1 0 8M15 4a11 11 0 0 1 0 16"/></>,
  screen: <><rect x="3" y="4" width="18" height="13" rx="2"/><path d="M12 17v4M8 21h8"/></>,
  search: <><circle cx="10.5" cy="10.5" r="6.5"/><path d="m16 16 5 5"/></>,
  headphones: <><path d="M4 14v-3a8 8 0 0 1 16 0v3"/><rect x="3" y="12" width="5" height="9" rx="2"/><rect x="16" y="12" width="5" height="9" rx="2"/></>,
  users: <><circle cx="9" cy="8" r="3"/><path d="M3 21v-3a6 6 0 0 1 12 0v3M16 5a3 3 0 0 1 0 6M21 21v-3a6 6 0 0 0-3-5"/></>,
  away: <path d="M20.5 13.2A8.5 8.5 0 0 1 10.8 3.5a8.5 8.5 0 1 0 9.7 9.7Z"/>,
  bell: <><path d="M18 8a6 6 0 0 0-12 0c0 7-3 7-3 9h18c0-2-3-2-3-9M10 21h4"/></>,
  lock: <><rect x="5" y="10" width="14" height="11" rx="2"/><path d="M8 10V7a4 4 0 0 1 8 0v3"/></>,
  arrowRight: <path d="M4 12h16m-6-6 6 6-6 6"/>,
  server: <><rect x="4" y="3" width="16" height="8" rx="2"/><rect x="4" y="13" width="16" height="8" rx="2"/><path d="M8 7h.01M8 17h.01M12 7h4M12 17h4"/></>,
  settings: <><path d="M12 2v3m0 14v3m10-10h-3M5 12H2m17-7-2 2M7 17l-2 2m14 0-2-2M7 7 5 5"/><circle cx="12" cy="12" r="7"/><circle cx="12" cy="12" r="2.5"/></>,
  shield: <><path d="m12 3 8 4v5c0 5-3 8-8 10-5-2-8-5-8-10V7Z"/><path d="m9 12 2 2 4-4"/></>,
  mic: <><rect x="9" y="3" width="6" height="12" rx="3"/><path d="M6 11v1a6 6 0 0 0 12 0v-1M12 18v3M9 21h6"/></>,
  hangup: <path d="M3 15v-3c5-5 13-5 18 0v3h-5v-4a13 13 0 0 0-8 0v4Z"/>,
  chat: <path d="M5 4h14a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H9l-6 3V6a2 2 0 0 1 2-2Z"/>,
  send: <><path d="m21 3-7 18-4-7-7-4Z"/><path d="m10 14 5-5"/></>,
  attachment: <path d="m8 13 6.5-6.5a3 3 0 0 1 4.2 4.2L10 19.4a5 5 0 0 1-7-7L12 3.5a2 2 0 0 1 2.8 2.8L6 15.1a1 1 0 0 0 1.4 1.4L16 8"/>,
  play: <path d="m8 4 12 8-12 8Z"/>,
  refresh: <><path d="M20 7v5h-5M4 17v-5h5"/><path d="M6.1 7a7 7 0 0 1 11.5-1L20 9M4 15l2.4 3A7 7 0 0 0 17.9 17"/></>,
} satisfies Record<string, ReactNode>;

export function Icon({ name }: { name: keyof typeof paths }) {
  return <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7"
    strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">{paths[name]}</svg>;
}
