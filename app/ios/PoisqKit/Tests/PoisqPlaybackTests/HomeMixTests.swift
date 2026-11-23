import Testing
import PoisqPlayback
import PoisqCore

@Suite struct HomeMixTests {
    private func song(_ id: String, album: String? = nil, artist: String? = nil, genre: String? = nil) -> Song {
        Song(id: id, title: id, artist: artist ?? "artist-\(id)", albumId: album ?? "album-\(id)", genre: genre)
    }

    @Test func interleavesSourcesInTurn() {
        let out = HomeMix.quickPicks(similar: [song("s1"), song("s2")], favorites: [song("f1"), song("f2")], random: [song("r1")])
        #expect(out.map(\.id) == ["s1", "f1", "r1", "s2", "f2"])
    }

    @Test func dropsDuplicatesAcrossSources() {
        let out = HomeMix.quickPicks(similar: [song("a")], favorites: [song("a"), song("b")], random: [song("b"), song("c")])
        #expect(out.map(\.id).sorted() == ["a", "b", "c"])
    }

    @Test func respectsLimit() {
        let many = (1...50).map { song("r\($0)") }
        #expect(HomeMix.quickPicks(similar: [], favorites: [], random: many, limit: 20).count == 20)
        #expect(HomeMix.quickPicks(similar: [], favorites: [], random: many, limit: 0).isEmpty)
        #expect(HomeMix.quickPicks(similar: [], favorites: [], random: many, limit: -3).isEmpty)
    }

    @Test func emptySourcesGiveEmptyPicks() {
        #expect(HomeMix.quickPicks(similar: [], favorites: [], random: []).isEmpty)
    }

    @Test func neverPutsSameAlbumBackToBack() {
        let one = (1...5).map { song("x\($0)", album: "X", artist: "A") }
        let other = (1...5).map { song("y\($0)", album: "Y", artist: "B") }
        let out = HomeMix.quickPicks(similar: one, favorites: other, random: [])
        for (l, r) in zip(out, out.dropFirst()) { #expect(l.albumId != r.albumId) }
        #expect(out.filter { $0.albumId == "X" }.count <= 2)
    }

    @Test func doesNotRepeatAlbumAcrossBatchBoundary() {
        let last = song("prev", album: "X", artist: "A")
        let out = HomeMix.quickPicks(similar: [song("x1", album: "X", artist: "A")], favorites: [song("y1", album: "Y", artist: "B")],
                                     random: [], after: last)
        #expect(out.first?.albumId == "Y")
    }

    @Test func stationSpreadsAlbumsAndCapsThem() {
        let albumX = (1...5).map { song("x\($0)", album: "X", artist: "A") }
        let mixed = albumX + [song("y1", album: "Y", artist: "B"), song("z1", album: "Z", artist: "C"), song("w1", album: "W", artist: "D")]
        let out = HomeMix.station(mixed)
        for (l, r) in zip(out, out.dropFirst()) { #expect(l.albumId != r.albumId) }
        #expect(out.filter { $0.albumId == "X" }.count <= 2)
        #expect(HomeMix.station([]).isEmpty)
    }

    @Test func topGenresByCountThenName() {
        let songs = [song("1", genre: "Rock"), song("2", genre: "Rock"), song("3", genre: "Jazz"),
                     song("4", genre: "Ambient"), song("5", genre: " Jazz "), song("6", genre: "Pop")]
        #expect(HomeMix.topGenres(songs, limit: 3) == ["Jazz", "Rock", "Ambient"])
    }

    @Test func topGenresSkipsEmptyAndBoundsLimit() {
        let songs = [song("1", genre: nil), song("2", genre: "  "), song("3", genre: "Rock")]
        #expect(HomeMix.topGenres(songs) == ["Rock"])
        #expect(HomeMix.topGenres(songs, limit: 0).isEmpty)
        #expect(HomeMix.topGenres([]).isEmpty)
    }
}
