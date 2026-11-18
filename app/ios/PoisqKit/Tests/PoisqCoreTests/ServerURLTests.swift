import Testing
import Foundation
@testable import PoisqCore

@Suite struct ServerURLTests {
    @Test func bareHostTriesOnlyHTTPS() {
        #expect(ServerURL.candidates(from: " music.example.com/ ").map(\.absoluteString) == ["https://music.example.com"])
    }
    @Test func explicitSchemeIsKept() {
        #expect(ServerURL.candidates(from: "http://192.168.1.5:4533").map(\.absoluteString) == ["http://192.168.1.5:4533"])
    }
    @Test func subpathIsKept() {
        #expect(ServerURL.candidates(from: "https://x.com/navidrome/").map(\.absoluteString) == ["https://x.com/navidrome"])
    }
    @Test func garbageIsRejected() {
        #expect(ServerURL.candidates(from: "").isEmpty)
        #expect(ServerURL.candidates(from: "ftp://x.com").isEmpty)
    }
    @Test func uppercaseSchemeIsAccepted() {
        #expect(ServerURL.candidates(from: "HTTPS://music.example.com").map(\.absoluteString) == ["HTTPS://music.example.com"])
    }
}
