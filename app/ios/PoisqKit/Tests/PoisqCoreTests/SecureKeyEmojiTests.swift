import Foundation
import Testing
@testable import PoisqCore

private struct EmojiVector: Decodable {
    struct Example: Decodable { let rootPublicKey: String; let emoji: String }
    let label: String
    let table: String
    let examples: [Example]
}

@Suite struct SecureKeyEmojiTests {
    @Test func matchesGoVector() throws {
        let v: EmojiVector = try loadSharedVector("emoji-v1.json")
        #expect(SecureKeyEmoji.table.joined() == v.table)
        #expect(SecureKeyEmoji.table.count == 256 && Set(SecureKeyEmoji.table).count == 256)
        #expect(String(decoding: SecureKeyEmoji.label, as: UTF8.self) == v.label)
        for example in v.examples {
            #expect(SecureKeyEmoji.fingerprint(rootPublicKey: Data(hex: example.rootPublicKey)).joined() == example.emoji)
        }
        let pairing = try #require(SecureServerPairing(code: try SecurePairingVector.load().pairing.code))
        #expect(pairing.emojiFingerprint.joined() == v.examples[0].emoji)
    }
}
