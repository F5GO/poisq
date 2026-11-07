import Testing
import Foundation
@testable import PoisqCore

@Suite struct ClientTests {
    @Test func authURLContainsTokenAndSalt() throws {
        let c = SubsonicClient(baseURL: URL(string: "https://m.example.com")!, username: "u", password: "p")
        let u = try c.url("ping")
        let q = URLComponents(url: u, resolvingAgainstBaseURL: false)!.queryItems!
        #expect(u.path == "/rest/ping")
        #expect(q.contains { $0.name == "t" && $0.value?.count == 32 })
        #expect(q.contains { $0.name == "s" })
        #expect(!u.absoluteString.contains("p=")) // пароль в открытом виде не уходит
    }

    @Test func streamURLIsStable() throws {
        let c = SubsonicClient(baseURL: URL(string: "https://m.example.com")!, username: "u", password: "p")
        let s = Song(id: "42", title: "x")
        #expect(try c.streamURL(for: s) == c.streamURL(for: s))
    }

    @Test func baselineHasNoPoisq() {
        #expect(!Capabilities.baseline.hasPoisq)
    }
}

@Suite struct QualityTests {
    private func query(_ url: URL) -> [String: String] {
        Dictionary(uniqueKeysWithValues: (URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems ?? []).map { ($0.name, $0.value ?? "") })
    }
    private let client = SubsonicClient(baseURL: URL(string: "https://m.example.com")!, username: "u", password: "p")

    @Test func originalHasNoTranscodingParameters() throws {
        let q = query(try client.streamURL(for: Song(id: "1", title: "x"), quality: .original))
        #expect(q["format"] == nil && q["maxBitRate"] == nil)
    }
    @Test func lowerQualityAsksForMP3WithCap() throws {
        let q = query(try client.streamURL(for: Song(id: "1", title: "x"), quality: .kbps192))
        #expect(q["format"] == "mp3" && q["maxBitRate"] == "192")
    }
    @Test func fileURLUsesDownloadOnlyForOriginal() throws {
        let s = Song(id: "1", title: "x")
        #expect(try client.fileURL(for: s, quality: .original).path.hasSuffix("/download"))
        #expect(try client.fileURL(for: s, quality: .kbps128).path.hasSuffix("/stream"))
    }

    @Test func songWithSeveralArtistsJoinsNames() throws {
        let json = #"{"id":"1","title":"T","artist":"A; B; C","artistId":"a","artists":[{"id":"a","name":"A"},{"id":"b","name":"B"},{"id":"c","name":"C"}]}"#
        let song = try JSONDecoder().decode(Song.self, from: Data(json.utf8))
        #expect(song.artists?.map(\.id) == ["a", "b", "c"])
        #expect(song.displayArtist == ListFormatter.localizedString(byJoining: ["A", "B", "C"]))
        let single = try JSONDecoder().decode(Song.self, from: Data(#"{"id":"2","title":"T","artist":"A"}"#.utf8))
        #expect(single.displayArtist == "A")
    }
}
