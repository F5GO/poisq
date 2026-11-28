package securetransport

import (
	"crypto/ed25519"
	"crypto/sha256"
)

// emojiTable — 256 заметно разных эмодзи, каждый — один символ Unicode без вариантов и составных последовательностей,
// чтобы на всех устройствах выглядеть одинаково. Совпадает со Swift и с testdata/emoji-v1.json.
const emojiTable = "🐀🐄🐅🐇🐈🐉🐊🐋🐌🐍🐎🐐🐑🐒🐔🐕🐖🐗🐘🐙🐚🐛🐜🐝🐞🐟🐠🐡🐢🐦🐧🐨🐪🐬🐼🐻🐺🐸🐹🦊🦁🦄🦅🦆🦇🦉🦈🦋🦌🦍🦏🦒🦓🦔🦕🦖🦗🦘🦚🦛🦜🦝🦞🦢🦥🦦🦨🦩🦀🦂🦃🦐🦑🦠🌲🌳🌴🌵🌷🌸🌹🌺🌻🌼🍀🍁🍂🍃🍄🌾🌿🌱🍇🍉🍊🍋🍌🍍🍎🍐🍑🍒🍓🥝🥥🥑🍆🥔🥕🥒🥦🌽🧄🧅🍞🥐🥖🧀🍗🥩🥓🍔🍟🍕🌭🥪🌮🌯🥗🍿🍚🍜🍣🍡🦪🍦🍧🍨🍩🍪🎂🍰🧁🥧🍫🍬🍭🍯🍼🥛☕🍾🍷🍸🍹🍺🥂🥤🧉🧊⚽⚾🏀🏈🏉🎾🏐🎳🏏🏓🏸🥊🥋⛳🎣🎽🎿🛷🥌🎯🎱🔮🎮🎰🎲🧩🧸🎭🎨🧵🧶🎼🎤🎧🎷🎸🎹🎺🎻🥁📱📷🔋💡🔦📚📖💎🔑🔒🔔🎁🎈🎉🎀🏆🥇🎃🎄🧨✨🎏🚀🚁🚂🚌🚑🚒🚓🚕🚗🚚🚜🚲🛴🛵⛵🚢🛸🚠🌍🌙⭐🌈⚡🔥💧🌊⛄☔🌋🌕🌀⌛⏰🧭"

const emojiLabel = "poisq-secure/v1 emoji"

var emojiList = func() []string {
	out := make([]string, 0, 256)
	for _, r := range emojiTable {
		out = append(out, string(r))
	}
	if len(out) != 256 {
		panic("secure transport: emoji table must have 256 entries")
	}
	return out
}()

// KeyEmoji — отпечаток корневого ключа для сверки человеком, как в Telegram: 4 эмодзи из первых 32 бит
// SHA-256(метка ‖ ключ). Его показывают и панель Studio, и приложение после сканирования QR.
func KeyEmoji(root ed25519.PublicKey) []string {
	sum := sha256.Sum256(append([]byte(emojiLabel), root...))
	return []string{emojiList[sum[0]], emojiList[sum[1]], emojiList[sum[2]], emojiList[sum[3]]}
}
