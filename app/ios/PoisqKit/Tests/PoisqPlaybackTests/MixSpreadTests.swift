import Testing
@testable import PoisqPlayback
import PoisqCore

@Suite struct MixSpreadTests {
    private func song(_ id: String, album: String, artist: String = "A") -> Song {
        Song(id: id, title: id, artist: artist, albumId: album)
    }

    @Test func neverPutsSameAlbumNextToEachOther() {
        let pool = (1...6).map { song("a\($0)", album: "X") } + (1...4).map { song("b\($0)", album: "Y") } + (1...4).map { song("c\($0)", album: "Z") }
        let out = Player.spread(pool.shuffled(), after: nil, maxPerAlbum: 2, limit: 30)
        for (l, r) in zip(out, out.dropFirst()) { #expect(l.albumId != r.albumId) }
    }

    @Test func capsTracksPerAlbum() {
        let pool = (1...8).map { song("a\($0)", album: "X") } + [song("b", album: "Y")]
        let out = Player.spread(pool, after: nil, maxPerAlbum: 2, limit: 30)
        #expect(out.filter { $0.albumId == "X" }.count <= 2)
    }

    @Test func doesNotStartWithAlbumOfLastQueuedTrack() {
        let last = song("last", album: "X")
        let out = Player.spread([song("a", album: "X"), song("b", album: "Y")], after: last, maxPerAlbum: 2, limit: 30)
        #expect(out.first?.albumId == "Y")
    }
}
