import Foundation
import Testing
import PoisqPlayback
import PoisqCore

@Suite struct PlayHistoryLogTests {
    private func song(_ id: String) -> Song { Song(id: id, title: id) }
    private let t0 = Date(timeIntervalSince1970: 1_000_000)
    private let day: TimeInterval = 86_400

    @Test func newestFirstAndOneEntryPerSong() {
        var log = PlayHistoryLog()
        log.record(song("a"), at: t0)
        log.record(song("b"), at: t0 + 1)
        log.record(song("a"), at: t0 + 2)
        #expect(log.recent(limit: 10).map(\.id) == ["a", "b"])
        #expect(log.entries.first?.count == 2)
        #expect(log.lastPlayed("a") == t0 + 2)
    }

    @Test func recentRespectsLimit() {
        var log = PlayHistoryLog()
        for i in 0..<5 { log.record(song("s\(i)"), at: t0 + Double(i)) }
        #expect(log.recent(limit: 2).map(\.id) == ["s4", "s3"])
        #expect(log.recent(limit: 0).isEmpty)
        #expect(log.recent(limit: -1).isEmpty)
    }

    @Test func capacityDropsOldest() {
        var log = PlayHistoryLog()
        for i in 0...PlayHistoryLog.capacity { log.record(song("s\(i)"), at: t0 + Double(i)) }
        #expect(log.entries.count == PlayHistoryLog.capacity)
        #expect(log.lastPlayed("s0") == nil)
        #expect(log.recent(limit: 1).first?.id == "s\(PlayHistoryLog.capacity)")
    }

    @Test func survivesCodableRoundTrip() throws {
        var log = PlayHistoryLog()
        log.record(song("a"), at: t0)
        let decoded = try JSONDecoder().decode(PlayHistoryLog.self, from: JSONEncoder().encode(log))
        #expect(decoded == log)
    }

    @Test func notRecentlyOrdersOldestThenNeverPlayed() {
        var log = PlayHistoryLog()
        log.record(song("old"), at: t0 - 90 * day)
        log.record(song("older"), at: t0 - 200 * day)
        log.record(song("fresh"), at: t0 - 2 * day)
        let favorites = ["fresh", "never1", "old", "older", "never2", "old"].map(song)
        let out = log.notRecently(from: favorites, olderThan: 30 * day, now: t0, limit: 10)
        #expect(out.map(\.id) == ["older", "old", "never1", "never2"])
    }

    @Test func notRecentlyBoundsAndEmptyInput() {
        let log = PlayHistoryLog()
        #expect(log.notRecently(from: [], olderThan: day, now: t0, limit: 5).isEmpty)
        #expect(log.notRecently(from: [song("a")], olderThan: day, now: t0, limit: 0).isEmpty)
        #expect(log.notRecently(from: [song("a"), song("b")], olderThan: day, now: t0, limit: 1).map(\.id) == ["a"])
    }
}
