export type WorkspaceSide = "browser" | "chat";
export type WorkspaceWidths = Record<WorkspaceSide, number>;

export const defaultWorkspaceWidths: WorkspaceWidths = { browser: 240, chat: 288 };

export function workspaceWidths(availableWidth: number, preferred: WorkspaceWidths) {
  const layoutWidth = Math.max(0, availableWidth - 16); // Two 8 px dividers.
  const browserMin = Math.min(192, layoutWidth / 3);
  const chatMin = Math.min(240, layoutWidth / 3);
  const centerMin = Math.min(280, Math.max(0, layoutWidth - browserMin - chatMin));
  const browserLimit = Math.max(browserMin, layoutWidth - chatMin - centerMin);
  const browser = Math.max(browserMin, Math.min(preferred.browser, browserLimit));
  const chatMax = Math.max(chatMin, layoutWidth - browser - centerMin);
  const chat = Math.max(chatMin, Math.min(preferred.chat, chatMax));

  return {
    widths: { browser, chat },
    minimums: { browser: browserMin, chat: chatMin },
    maximums: { browser: Math.max(browserMin, layoutWidth - chat - centerMin), chat: chatMax },
  };
}
