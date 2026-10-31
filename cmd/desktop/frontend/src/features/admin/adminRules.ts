import type {AdminAccount, AdminPermission} from "../../api";

// The part of ClientViewDTO the administration rules read.
export type AdminViewer = {userId: string; isOwner: boolean; joinLevel?: number; canKick: boolean; canBan: boolean; canDrag: boolean};

export const maxLevel = 65535;

export function parseLevel(value: string): number | null {
    if (!/^\d{1,5}$/.test(value.trim())) return null;
    const level = Number(value);
    return level <= maxLevel ? level : null;
}

// These rules mirror the server hierarchy (internal/voice/admin.go) so that
// controls the server would refuse are disabled; the server still decides.
export function canEditAccount(viewer: AdminViewer, account: AdminAccount): boolean {
    if (account.owner || account.id === viewer.userId) return false;
    return viewer.isOwner || (!account.admin && account.joinLevel <= (viewer.joinLevel ?? 0));
}

export function canTogglePermission(viewer: AdminViewer, account: AdminAccount, permission: AdminPermission): boolean {
    if (!canEditAccountPermissions(viewer, account)) return false;
    if (viewer.isOwner) return true;
    const held: Record<AdminPermission, boolean> = {kick: viewer.canKick, ban: viewer.canBan, drag: viewer.canDrag, admin: false};
    return held[permission];
}

// The owner may edit their own permissions, while level, ban and deletion
// continue to use the stricter account hierarchy.
export function canEditAccountPermissions(viewer: AdminViewer, account: AdminAccount): boolean {
    return (viewer.isOwner && account.owner && account.id === viewer.userId) || canEditAccount(viewer, account);
}

export function canBan(viewer: AdminViewer, account: AdminAccount): boolean {
    return canEditAccount(viewer, account) && (viewer.isOwner || viewer.canBan);
}

export function levelAllowed(viewer: AdminViewer, level: number | null): boolean {
    return level !== null && (viewer.isOwner || level <= (viewer.joinLevel ?? 0));
}

export function canEditChannelLevel(viewer: AdminViewer, current: number): boolean {
    return viewer.isOwner || current <= (viewer.joinLevel ?? 0);
}

// The part of ChannelDTO the channel editor reads.
export type AdminChannelNode = {id: string; parentId: string; name: string; position: number};

// Channel editing (create, settings, move) needs server 0.3.7 or newer.
export const channelEditingServerVersion = "0.3.7";

export function versionAtLeast(version: string | undefined, minimum: string): boolean {
    const parse = (value: string) => value.trim().replace(/^v/, "").split(/[.+-]/).slice(0, 3).map((part) => Number.parseInt(part, 10));
    if (!version) return false;
    const have = parse(version);
    const want = parse(minimum);
    if (have.length < 3 || have.some((part) => !Number.isFinite(part))) return false;
    for (let i = 0; i < 3; i++) {
        if (have[i] !== want[i]) return have[i] > want[i];
    }
    return true;
}

function siblingOrder(a: AdminChannelNode, b: AdminChannelNode): number {
    return a.position - b.position || compareIDs(a.id, b.id);
}

// channelTree lists channels depth first in display order with their depth.
export function channelTree<T extends AdminChannelNode>(channels: T[]): {channel: T; depth: number}[] {
    const children = new Map<string, T[]>();
    for (const channel of channels) {
        const list = children.get(channel.parentId) ?? [];
        list.push(channel);
        children.set(channel.parentId, list);
    }
    const result: {channel: T; depth: number}[] = [];
    const visit = (parentId: string, depth: number) => {
        for (const channel of [...(children.get(parentId) ?? [])].sort(siblingOrder)) {
            if (result.some((item) => item.channel.id === channel.id)) continue;
            result.push({channel, depth});
            visit(channel.id, depth + 1);
        }
    };
    visit("0", 0);
    return result;
}

// descendantIDs returns id and every channel below it: none of them can
// become the new parent of id.
export function descendantIDs(channels: AdminChannelNode[], id: string): Set<string> {
    const result = new Set([id]);
    let grew = true;
    while (grew) {
        grew = false;
        for (const channel of channels) {
            if (result.has(channel.parentId) && !result.has(channel.id)) {
                result.add(channel.id);
                grew = true;
            }
        }
    }
    return result;
}

// reorderPositions moves id one place up (-1) or down (+1) among its
// siblings and returns the positions that change, numbering the siblings
// 0, 1, 2… so equal positions become distinct.
export function reorderPositions(channels: AdminChannelNode[], id: string, direction: -1 | 1): {id: string; position: number}[] {
    const channel = channels.find((item) => item.id === id);
    if (!channel) return [];
    const siblings = channels.filter((item) => item.parentId === channel.parentId).sort(siblingOrder);
    const index = siblings.findIndex((item) => item.id === id);
    const target = index + direction;
    if (target < 0 || target >= siblings.length) return [];
    [siblings[index], siblings[target]] = [siblings[target], siblings[index]];
    return siblings.flatMap((item, position) => item.position === position ? [] : [{id: item.id, position}]);
}

// compareIDs orders decimal uint64 strings numerically.
function compareIDs(a: string, b: string): number {
    return a.length - b.length || (a < b ? -1 : a > b ? 1 : 0);
}
