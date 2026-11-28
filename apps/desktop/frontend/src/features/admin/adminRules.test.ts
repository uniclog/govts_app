import {describe, expect, it} from "vitest";
import type {AdminAccount} from "../../api";
import {canBan, canEditAccount, canEditChannelLevel, canTogglePermission, channelTree, descendantIDs, levelAllowed, parseLevel, reorderPositions, versionAtLeast, type AdminChannelNode, type AdminViewer} from "./adminRules";

const account = (patch: Partial<AdminAccount> = {}): AdminAccount => ({
    id: "5", name: "user", fingerprint: "00", joinLevel: 5, kick: false, ban: false, drag: false,
    admin: false, owner: false, banned: false, online: false, banReason: "", ...patch,
});
const administrator: AdminViewer = {userId: "2", isOwner: false, joinLevel: 10, canKick: true, canBan: false, canDrag: false};
const owner: AdminViewer = {userId: "1", isOwner: true, canKick: false, canBan: false, canDrag: false};

describe("parseLevel", () => {
    it("accepts 0..65535 as plain digits", () => {
        expect(parseLevel("0")).toBe(0);
        expect(parseLevel(" 25 ")).toBe(25);
        expect(parseLevel("65535")).toBe(65535);
    });
    it("rejects everything else", () => {
        for (const value of ["", "-1", "1.5", "1e3", "65536", "123456", "abc"]) expect(parseLevel(value)).toBeNull();
    });
});

describe("account rules", () => {
    it("protects the owner and the viewer's own account from everyone", () => {
        expect(canEditAccount(owner, account({owner: true, id: "9"}))).toBe(false);
        expect(canEditAccount(owner, account({id: "1"}))).toBe(false);
        expect(canEditAccount(administrator, account({id: "2"}))).toBe(false);
    });
    it("lets an administrator change only lower plain accounts", () => {
        expect(canEditAccount(administrator, account())).toBe(true);
        expect(canEditAccount(administrator, account({joinLevel: 10}))).toBe(true);
        expect(canEditAccount(administrator, account({joinLevel: 11}))).toBe(false);
        expect(canEditAccount(administrator, account({admin: true}))).toBe(false);
        expect(canEditAccount(owner, account({admin: true, joinLevel: 60000}))).toBe(true);
    });
    it("grants only held permissions and never admin unless owner", () => {
        expect(canTogglePermission(administrator, account(), "kick")).toBe(true);
        expect(canTogglePermission(administrator, account(), "ban")).toBe(false);
        expect(canTogglePermission(administrator, account(), "admin")).toBe(false);
        expect(canTogglePermission(owner, account(), "admin")).toBe(true);
        expect(canTogglePermission(administrator, account({joinLevel: 11}), "kick")).toBe(false);
    });
    it("needs the ban permission to ban unless owner", () => {
        expect(canBan(administrator, account())).toBe(false);
        expect(canBan({...administrator, canBan: true}, account())).toBe(true);
        expect(canBan(owner, account())).toBe(true);
    });
});

describe("level rules", () => {
    it("caps levels at the viewer's own unless owner", () => {
        expect(levelAllowed(administrator, 10)).toBe(true);
        expect(levelAllowed(administrator, 11)).toBe(false);
        expect(levelAllowed(administrator, null)).toBe(false);
        expect(levelAllowed(owner, 65535)).toBe(true);
        expect(levelAllowed({...administrator, joinLevel: undefined}, 1)).toBe(false);
    });
    it("locks channels above the viewer's level", () => {
        expect(canEditChannelLevel(administrator, 10)).toBe(true);
        expect(canEditChannelLevel(administrator, 30)).toBe(false);
        expect(canEditChannelLevel(owner, 30)).toBe(true);
    });
});

const node = (id: string, parentId: string, position: number): AdminChannelNode => ({id, parentId, name: `c${id}`, position});
// Tree: 1 > (3, 2 > 4), 10 at the root after 1.
const channels = [node("10", "0", 1), node("2", "1", 1), node("1", "0", 0), node("4", "2", 0), node("3", "1", 0)];

describe("channel tree", () => {
    it("lists channels depth first by position, then numeric ID", () => {
        expect(channelTree(channels).map(({channel, depth}) => `${channel.id}@${depth}`)).toEqual(["1@0", "3@1", "2@1", "4@2", "10@0"]);
        expect(channelTree([node("10", "0", 0), node("9", "0", 0)]).map(({channel}) => channel.id)).toEqual(["9", "10"]);
    });
    it("finds a channel and everything below it", () => {
        expect([...descendantIDs(channels, "1")].sort()).toEqual(["1", "2", "3", "4"]);
        expect([...descendantIDs(channels, "10")]).toEqual(["10"]);
    });
    it("swaps neighbours and renumbers only changed siblings", () => {
        expect(reorderPositions(channels, "2", -1)).toEqual([{id: "2", position: 0}, {id: "3", position: 1}]);
        expect(reorderPositions(channels, "3", -1)).toEqual([]);
        expect(reorderPositions(channels, "10", 1)).toEqual([]);
        expect(reorderPositions(channels, "1", 1)).toEqual([{id: "10", position: 0}, {id: "1", position: 1}]);
        // Equal positions become distinct.
        expect(reorderPositions([node("5", "0", 0), node("6", "0", 0), node("7", "0", 0)], "7", -1))
            .toEqual([{id: "7", position: 1}, {id: "6", position: 2}]);
    });
});

describe("versionAtLeast", () => {
    it("compares release versions", () => {
        expect(versionAtLeast("0.3.7", "0.3.7")).toBe(true);
        expect(versionAtLeast("0.3.10", "0.3.7")).toBe(true);
        expect(versionAtLeast("v0.4.0", "0.3.7")).toBe(true);
        expect(versionAtLeast("0.3.6", "0.3.7")).toBe(false);
        expect(versionAtLeast("", "0.3.7")).toBe(false);
        expect(versionAtLeast("dev", "0.3.7")).toBe(false);
    });
});
