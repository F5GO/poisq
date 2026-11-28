import CryptoKit
import Foundation

/// Emoji fingerprint of the installation root key, like Telegram: 4 emoji from the first 32 bits of
/// SHA-256(label ‖ key). Studio shows the same emoji next to the QR code. Matches Go `KeyEmoji` and testdata/emoji-v1.json.
public enum SecureKeyEmoji {
    static let table: [String] = "🐀🐄🐅🐇🐈🐉🐊🐋🐌🐍🐎🐐🐑🐒🐔🐕🐖🐗🐘🐙🐚🐛🐜🐝🐞🐟🐠🐡🐢🐦🐧🐨🐪🐬🐼🐻🐺🐸🐹🦊🦁🦄🦅🦆🦇🦉🦈🦋🦌🦍🦏🦒🦓🦔🦕🦖🦗🦘🦚🦛🦜🦝🦞🦢🦥🦦🦨🦩🦀🦂🦃🦐🦑🦠🌲🌳🌴🌵🌷🌸🌹🌺🌻🌼🍀🍁🍂🍃🍄🌾🌿🌱🍇🍉🍊🍋🍌🍍🍎🍐🍑🍒🍓🥝🥥🥑🍆🥔🥕🥒🥦🌽🧄🧅🍞🥐🥖🧀🍗🥩🥓🍔🍟🍕🌭🥪🌮🌯🥗🍿🍚🍜🍣🍡🦪🍦🍧🍨🍩🍪🎂🍰🧁🥧🍫🍬🍭🍯🍼🥛☕🍾🍷🍸🍹🍺🥂🥤🧉🧊⚽⚾🏀🏈🏉🎾🏐🎳🏏🏓🏸🥊🥋⛳🎣🎽🎿🛷🥌🎯🎱🔮🎮🎰🎲🧩🧸🎭🎨🧵🧶🎼🎤🎧🎷🎸🎹🎺🎻🥁📱📷🔋💡🔦📚📖💎🔑🔒🔔🎁🎈🎉🎀🏆🥇🎃🎄🧨✨🎏🚀🚁🚂🚌🚑🚒🚓🚕🚗🚚🚜🚲🛴🛵⛵🚢🛸🚠🌍🌙⭐🌈⚡🔥💧🌊⛄☔🌋🌕🌀⌛⏰🧭".map(String.init)
    static let label = Data("poisq-secure/v1 emoji".utf8)

    public static func fingerprint(rootPublicKey: Data) -> [String] {
        let digest = Array(SHA256.hash(data: label + rootPublicKey))
        return digest.prefix(4).map { table[Int($0)] }
    }
}

extension SecureServerPairing {
    /// Four emoji to compare with poisq Studio after scanning.
    public var emojiFingerprint: [String] { SecureKeyEmoji.fingerprint(rootPublicKey: rootPublicKey) }
}
