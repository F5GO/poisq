import PoisqCore

/// Подборки треков для главной: чистые функции, чтобы порядок и ограничения проверялись тестами.
public enum HomeMix {
    /// «Быстрый выбор»: похожие на недавнее, любимые и случайные — по очереди, без повторов,
    /// не подряд с одного альбома или от одного исполнителя. Перемешивает вызывающий: здесь порядок детерминирован.
    /// `after` — трек, который будет играть перед подборкой: на стыке порций тоже не повторяем его альбом.
    public static func quickPicks(similar: [Song], favorites: [Song], random: [Song], limit: Int = 20, after: Song? = nil) -> [Song] {
        guard limit > 0 else { return [] }
        let sources = [similar, favorites, random]
        var seen = Set<String>()
        var pool: [Song] = []
        let longest = sources.map(\.count).max() ?? 0
        for i in 0..<longest {
            for list in sources where i < list.count && seen.insert(list[i].id).inserted {
                pool.append(list[i])
            }
        }
        return Player.spread(pool, after: after, maxPerAlbum: 2, limit: limit)
    }

    /// Готовый список станции без соседей с одного альбома или от одного исполнителя и не больше двух треков с альбома.
    public static func station(_ songs: [Song], after: Song? = nil) -> [Song] {
        Player.spread(songs, after: after, maxPerAlbum: 2, limit: songs.count)
    }

    /// Самые частые жанры: по убыванию числа треков, при равенстве — по алфавиту. Пустые жанры не считаются.
    public static func topGenres(_ songs: [Song], limit: Int = 3) -> [String] {
        guard limit > 0 else { return [] }
        var counts: [String: Int] = [:]
        for song in songs {
            guard let genre = song.genre?.trimmingCharacters(in: .whitespacesAndNewlines), !genre.isEmpty else { continue }
            counts[genre, default: 0] += 1
        }
        return counts.sorted { $0.value != $1.value ? $0.value > $1.value : $0.key.localizedStandardCompare($1.key) == .orderedAscending }
            .prefix(limit)
            .map(\.key)
    }
}
