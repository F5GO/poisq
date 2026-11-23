import Foundation
import PoisqCore

/// История прослушиваний на устройстве: в стандартном Subsonic нет списка недавних треков.
/// Новые записи — в начале; одна запись на трек; размер ограничен.
public struct PlayHistoryLog: Codable, Sendable, Equatable {
    public struct Entry: Codable, Sendable, Equatable {
        public var song: Song
        public var lastPlayed: Date
        public var count: Int
    }

    public static let capacity = 500
    public private(set) var entries: [Entry]

    public init(entries: [Entry] = []) {
        self.entries = Array(entries.prefix(Self.capacity))
    }

    /// Трек дослушан: поднимаем наверх и увеличиваем счётчик. Самые старые записи сверх лимита выбрасываются.
    public mutating func record(_ song: Song, at date: Date) {
        var count = 0
        if let i = entries.firstIndex(where: { $0.song.id == song.id }) {
            count = entries[i].count
            entries.remove(at: i)
        }
        entries.insert(Entry(song: song, lastPlayed: date, count: count + 1), at: 0)
        if entries.count > Self.capacity { entries.removeLast(entries.count - Self.capacity) }
    }

    public func recent(limit: Int) -> [Song] {
        guard limit > 0 else { return [] }
        return entries.prefix(limit).map(\.song)
    }

    public func lastPlayed(_ songID: String) -> Date? {
        entries.first { $0.song.id == songID }?.lastPlayed
    }

    /// «Давно не слушал»: любимые, которые не играли дольше `interval`. Сначала те, что слушали давнее всего,
    /// затем не игравшие на этом устройстве ни разу — в исходном порядке.
    public func notRecently(from favorites: [Song], olderThan interval: TimeInterval, now: Date, limit: Int) -> [Song] {
        guard limit > 0 else { return [] }
        let cutoff = now.addingTimeInterval(-interval)
        let played = Dictionary(entries.map { ($0.song.id, $0.lastPlayed) }, uniquingKeysWith: { first, _ in first })
        var seen = Set<String>()
        let unique = favorites.filter { seen.insert($0.id).inserted }
        let old = unique.compactMap { song in played[song.id].flatMap { $0 < cutoff ? (song, $0) : nil } }
            .sorted { $0.1 < $1.1 }
            .map(\.0)
        let never = unique.filter { played[$0.id] == nil }
        return Array((old + never).prefix(limit))
    }
}
