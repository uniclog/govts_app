// Same order as chatReactionEmoji in internal/domain/chat.go: the server accepts only these.
export const REACTION_EMOJI = ["👍", "❤️", "😂", "😮", "😢", "🔥", "👏", "🎉"];

const EMOJI_UNIT = /\p{Regional_Indicator}{2}|[0-9#*]️?⃣|\p{Extended_Pictographic}[️\p{Emoji_Modifier}]*(?:‍\p{Extended_Pictographic}[️\p{Emoji_Modifier}]*)*/gu;

export const isEmojiOnly = (text: string) => {
    const trimmed = text.trim();
    return trimmed !== "" && trimmed.replace(EMOJI_UNIT, "").trim() === "";
};

export type EmojiGroupId = "smileys" | "gestures" | "hearts" | "nature" | "food" | "activities";

export const EMOJI_GROUPS: {id: EmojiGroupId; emoji: string[]}[] = [
    {id: "smileys", emoji: ["😀", "😃", "😄", "😁", "😆", "😅", "😂", "🤣", "🙂", "🙃", "😉", "😊", "😇", "🥰", "😍", "🤩", "😘", "😋", "😛", "😜", "🤪", "🤗", "🤔", "🤨", "😐", "😑", "😶", "🙄", "😏", "😴", "😌", "😎", "🤓", "🥳", "😕", "😟", "😮", "😲", "😳", "🥺", "😢", "😭", "😱", "😤", "😡", "🤯", "🥶", "🥵", "🤢", "🤡", "💀", "👻"]},
    {id: "gestures", emoji: ["👍", "👎", "👌", "✌️", "🤞", "🤟", "🤘", "🤙", "👈", "👉", "👆", "👇", "👋", "🤚", "✋", "🖐️", "👏", "🙌", "🤝", "🙏", "💪", "✊", "👊", "🫡", "🤷", "🤦"]},
    {id: "hearts", emoji: ["❤️", "🧡", "💛", "💚", "💙", "💜", "🖤", "🤍", "🤎", "💔", "💕", "💖", "💯", "✨", "⭐", "🔥", "💥", "💤"]},
    {id: "nature", emoji: ["🐶", "🐱", "🦊", "🐻", "🐼", "🐸", "🐵", "🐔", "🦄", "🐝", "🦋", "🐢", "🌹", "🌻", "🌲", "🍀", "🌙", "☀️", "🌈", "⚡", "❄️", "🌊"]},
    {id: "food", emoji: ["🍎", "🍌", "🍓", "🍉", "🍕", "🍔", "🍟", "🌭", "🍿", "🍣", "🍜", "🍰", "🍪", "🍫", "☕", "🍵", "🍺", "🍷"]},
    {id: "activities", emoji: ["🎉", "🎊", "🎁", "🏆", "🥇", "⚽", "🏀", "🎮", "🎧", "🎵", "🎸", "🚀", "💻", "📱", "💡", "📌", "✅", "❌", "❓", "❗"]},
];
