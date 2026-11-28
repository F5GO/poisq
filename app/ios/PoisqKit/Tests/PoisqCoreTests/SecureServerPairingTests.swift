import Foundation
import Testing
@testable import PoisqCore

@Suite struct SecureServerPairingTests {
    // TestPairingCodeSwiftVector in Go emits this same token.
    private static let vector = "poisq-secure:v1:00112233445566778899aabbccddeeff:AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA:aHR0cHM6Ly9tdXNpYy5leGFtcGxlLmNvbTo4NDQz"

    @Test func readsStudioPairingCode() throws {
        let pairing = try #require(SecureServerPairing(code: Self.vector))
        #expect(pairing.origin.absoluteString == "https://music.example.com:8443")
        #expect(pairing.installationID == "00112233445566778899aabbccddeeff")
        #expect(pairing.publicKey == Data((1...32).map(UInt8.init)))
    }

    @Test func rejectsInvalidPairingFields() {
        let fields = Self.vector.split(separator: ":").map(String.init)
        for (position, invalid) in [
            (0, "other"),
            (1, "v2"),
            (2, String(repeating: "0", count: 32)),
            (2, fields[2].uppercased()),
            (3, base64URL(Data(repeating: 0, count: 32))),
            (3, base64URL(Data(repeating: 1, count: 31))),
            (3, fields[3] + "="),
            (4, base64URL(Data([0xff]))),
        ] {
            var corrupted = fields
            corrupted[position] = invalid
            #expect(SecureServerPairing(code: corrupted.joined(separator: ":")) == nil)
        }
        #expect(SecureServerPairing(code: Self.vector + ":extra") == nil)
        #expect(SecureServerPairing(code: Self.vector + String(repeating: " ", count: 512)) == nil)
    }

    @Test func rejectsNoncanonicalOrigins() {
        let prefix = Self.vector.split(separator: ":").dropLast().joined(separator: ":")
        for address in [
            "http://music.example.com",
            "https://user@music.example.com",
            "https://music.example.com/path",
            "https://music.example.com?",
            "https://music.example.com#",
            "https://music.example.com/",
            "https://music.example.com:443",
            "https://music.example.com:0",
            "https://music.example.com:65536",
            "https://music.example.com:08443",
            "https://bad_host.example.com",
            "https://Music.example.com",
            "https://K.example.com",
            "https://music.example.com.",
        ] {
            #expect(SecureServerPairing(code: prefix + ":" + base64URL(Data(address.utf8))) == nil, "Accepted \(address)")
        }
    }

    private func base64URL(_ data: Data) -> String {
        data.base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }
}
