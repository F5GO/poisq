import Foundation
import CryptoKit
import os

public enum SubsonicError: Error, Sendable {
    case invalidURL
    case http(Int)
    case server(code: Int, message: String)
    case decoding
}

/// Клиент стандартного Subsonic/OpenSubsonic API. Работает с любым сервером.
public final class SubsonicClient: Sendable {
    private let base: OSAllocatedUnfairLock<URL>
    /// Текущий рабочий адрес. Может смениться на запасной (`setBaseURL`), поэтому читается каждый раз.
    public var baseURL: URL { base.withLock { $0 } }
    public func setBaseURL(_ url: URL) { base.withLock { $0 = url } }
    /// Свои HTTP-заголовки (Cloudflare Access, Authelia и т. п.) — уходят с каждым запросом к серверу.
    public let headers: [String: String]
    private let username: String
    private let password: String
    private let salt: String
    private let session: URLSession
    private static let apiVersion = "1.16.1"
    private static let clientName = "poisq"

    public init(baseURL: URL, username: String, password: String, headers: [String: String] = [:], session: URLSession = .shared) {
        self.base = OSAllocatedUnfairLock(initialState: baseURL)
        self.headers = headers
        self.username = username
        self.password = password
        self.session = session
        // Одна соль на клиента: ссылки на стрим стабильны для одного трека.
        self.salt = String((0..<12).map { _ in "abcdefghijklmnopqrstuvwxyz0123456789".randomElement()! })
    }

    // MARK: URL building

    func url(_ method: String, _ extra: [URLQueryItem] = []) throws -> URL {
        guard var c = URLComponents(url: baseURL.appending(path: "rest/\(method)"), resolvingAgainstBaseURL: false)
        else { throw SubsonicError.invalidURL }
        let token = Insecure.MD5.hash(data: Data((password + salt).utf8)).map { String(format: "%02x", $0) }.joined()
        c.queryItems = [
            .init(name: "u", value: username), .init(name: "t", value: token), .init(name: "s", value: salt),
            .init(name: "v", value: Self.apiVersion), .init(name: "c", value: Self.clientName), .init(name: "f", value: "json"),
        ] + extra
        guard let url = c.url else { throw SubsonicError.invalidURL }
        return url
    }

    /// Ссылка на стрим. Оригинал отдаётся как есть, остальное сервер перекодирует.
    public func streamURL(for song: Song, quality: StreamQuality = .original) throws -> URL {
        var q = [URLQueryItem(name: "id", value: song.id)]
        if let format = quality.format, let rate = quality.maxBitRate {
            q.append(.init(name: "format", value: format))
            q.append(.init(name: "maxBitRate", value: String(rate)))
        }
        return try url("stream", q)
    }

    /// Ссылка на файл для скачивания: оригинал через `download`, иное качество — перекодированный стрим.
    public func fileURL(for song: Song, quality: StreamQuality) throws -> URL {
        quality == .original ? try downloadURL(for: song) : try streamURL(for: song, quality: quality)
    }

    /// Оригинальный файл без перекодирования — для загрузок на устройство.
    public func downloadURL(for song: Song) throws -> URL {
        try url("download", [URLQueryItem(name: "id", value: song.id)])
    }

    public func coverArtURL(id: String, size: Int = 600) throws -> URL {
        try url("getCoverArt", [.init(name: "id", value: id), .init(name: "size", value: String(size))])
    }

    // MARK: Transport

    /// Запрос с нашими заголовками. Для загрузок, кеша и обложек — те же заголовки, что и у API.
    public func request(for url: URL) -> URLRequest {
        var r = URLRequest(url: url)
        for (k, v) in headers { r.setValue(v, forHTTPHeaderField: k) }
        return r
    }

    private func call(_ method: String, _ extra: [URLQueryItem] = []) async throws -> [String: Any] {
        let (data, response) = try await session.data(for: request(for: url(method, extra)))
        if let http = response as? HTTPURLResponse, !(200..<300).contains(http.statusCode) {
            throw SubsonicError.http(http.statusCode)
        }
        guard let root = try JSONSerialization.jsonObject(with: data) as? [String: Any],
              let body = root["subsonic-response"] as? [String: Any] else { throw SubsonicError.decoding }
        if body["status"] as? String == "failed" {
            let err = body["error"] as? [String: Any]
            throw SubsonicError.server(code: err?["code"] as? Int ?? 0, message: err?["message"] as? String ?? "")
        }
        return body
    }

    // MARK: Capabilities

    public func detectCapabilities() async -> Capabilities {
        var caps = Capabilities.baseline
        guard let ping = try? await call("ping") else { return caps }
        // Подкасты и радио есть не у каждого сервера: раздел показываем, только если запрос проходит.
        async let pod: [PodcastChannel]? = try? await podcasts(includeEpisodes: false)
        async let radio: [RadioStation]? = try? await radioStations()
        caps.hasPodcasts = await pod != nil
        caps.hasRadio = await radio != nil
        caps.isOpenSubsonic = ping["openSubsonic"] as? Bool ?? false
        guard caps.isOpenSubsonic,
              let ext = try? await call("getOpenSubsonicExtensions"),
              let list = ext["openSubsonicExtensions"] as? [[String: Any]] else { return caps }
        if let poisq = list.first(where: { $0["name"] as? String == "poisq" }),
           let versions = poisq["versions"] as? [Int] {
            caps.poisqVersion = versions.max()
            caps.hasCast = (try? await castZones())?.isEmpty == false    // эндпоинты трансляции есть только у poisq
        }
        return caps
    }

    public func ping() async throws { _ = try await call("ping") }

    // MARK: Трансляция poisq

    /// Колонка Music Assistant, разрешённая в poisq Studio.
    public struct CastZone: Sendable, Identifiable, Hashable {
        public var id: String
        public var name: String
        public var state: String            // idle / paused / playing
        public var volume: Int              // 0…100, -1 — колонка не сообщает
        public init(id: String, name: String, state: String = "idle", volume: Int = -1) {
            self.id = id; self.name = name; self.state = state; self.volume = volume
        }
    }

    /// Состояние колонки. `index` — номер песни в очереди колонки (-1 — нет), `position` — секунды внутри песни.
    public struct CastStatus: Sendable {
        public var state: String
        public var index: Int
        public var position: Double
        public var volume: Int
        public init(state: String, index: Int, position: Double, volume: Int) {
            self.state = state; self.index = index; self.position = position; self.volume = volume
        }
    }

    public func castZones() async throws -> [CastZone] {
        let body = try await call("poisqCastZones")
        let list = body["castZones"] as? [[String: Any]] ?? []
        return list.compactMap { z in
            guard let id = z["id"] as? String else { return nil }
            return CastZone(id: id, name: z["name"] as? String ?? id, state: z["state"] as? String ?? "idle", volume: z["volume"] as? Int ?? -1)
        }
    }

    /// Заменить очередь колонки песнями и играть с `index`, `offset` секунд.
    public func castPlay(zone: String, ids: [String], index: Int = 0, offset: Int = 0) async throws {
        var q = [URLQueryItem(name: "zone", value: zone), .init(name: "index", value: String(index)), .init(name: "offset", value: String(offset))]
        q += ids.map { URLQueryItem(name: "id", value: $0) }
        _ = try await call("poisqCastPlay", q)
    }

    /// Пульт: play, pause, stop, clear, next, previous, seek (position), volume (0…100), add (ids), remove (position — индекс).
    public func castControl(zone: String, action: String, position: Int? = nil, volume: Int? = nil, ids: [String] = []) async throws {
        var q = [URLQueryItem(name: "zone", value: zone), .init(name: "action", value: action)]
        if let position { q.append(.init(name: "position", value: String(position))) }
        if let volume { q.append(.init(name: "volume", value: String(volume))) }
        q += ids.map { URLQueryItem(name: "id", value: $0) }
        _ = try await call("poisqCastControl", q)
    }

    public func castStatus(zone: String) async throws -> CastStatus {
        let body = try await call("poisqCastStatus", [.init(name: "zone", value: zone)])
        guard let s = body["castStatus"] as? [String: Any] else { throw SubsonicError.decoding }
        return CastStatus(state: s["state"] as? String ?? "idle", index: s["index"] as? Int ?? -1,
                          position: (s["position"] as? NSNumber)?.doubleValue ?? 0, volume: s["volume"] as? Int ?? -1)
    }

    // MARK: Library

    /// Медиатеки, доступные пользователю. Если параметр musicFolderId не передавать, сервер использует все.
    public func musicFolders() async throws -> [MusicFolder] {
        let body = try await call("getMusicFolders")
        let list = (body["musicFolders"] as? [String: Any])?["musicFolder"] as? [[String: Any]] ?? []
        return list.compactMap { d in
            guard let id = d["id"] else { return nil }
            return MusicFolder(id: "\(id)", name: d["name"] as? String ?? "\(id)")
        }
    }

    /// Похожие на трек песни (Navidrome 0.60+: Last.fm, Deezer, ListenBrainz или плагин). На старых серверах пусто.
    public func similarSongs(id: String, count: Int = 30) async throws -> [Song] {
        let body = try await call("getSimilarSongs2", [.init(name: "id", value: id), .init(name: "count", value: String(count))])
        return try decode([Song].self, from: (body["similarSongs2"] as? [String: Any])?["song"])
    }

    public func randomSongs(count: Int = 50, genre: String? = nil, musicFolderId: String? = nil) async throws -> [Song] {
        var q = [URLQueryItem(name: "size", value: String(count))]
        if let genre { q.append(.init(name: "genre", value: genre)) }
        if let musicFolderId { q.append(.init(name: "musicFolderId", value: musicFolderId)) }
        let body = try await call("getRandomSongs", q)
        return try decode([Song].self, from: (body["randomSongs"] as? [String: Any])?["song"])
    }

    /// type: recent, newest, frequent, random, starred, alphabeticalByName…
    public func albumList(type: String, size: Int = 20, offset: Int = 0, musicFolderId: String? = nil) async throws -> [Album] {
        var q = [URLQueryItem(name: "type", value: type), .init(name: "size", value: String(size)),
                 .init(name: "offset", value: String(offset))]
        if let musicFolderId { q.append(.init(name: "musicFolderId", value: musicFolderId)) }
        let body = try await call("getAlbumList2", q)
        return try decode([Album].self, from: (body["albumList2"] as? [String: Any])?["album"])
    }

    /// Альбом и его треки.
    public func album(id: String) async throws -> (album: Album, songs: [Song]) {
        let body = try await call("getAlbum", [.init(name: "id", value: id)])
        guard let a = body["album"] as? [String: Any] else { throw SubsonicError.decoding }
        let data = try JSONSerialization.data(withJSONObject: a)
        guard let album = try? JSONDecoder().decode(Album.self, from: data) else { throw SubsonicError.decoding }
        return (album, try decode([Song].self, from: a["song"]))
    }

    // MARK: Прослушивание и очередь на сервере

    /// Отчёт о прослушивании. `submission == false` — «сейчас играет» (не считается прослушиванием),
    /// `true` — песня засчитана; `time` — когда трек начался (важно для отложенной отправки).
    public func scrobble(songID: String, time: Date? = nil, submission: Bool) async throws {
        var q = [URLQueryItem(name: "id", value: songID), .init(name: "submission", value: submission ? "true" : "false")]
        if let time { q.append(.init(name: "time", value: String(Int(time.timeIntervalSince1970 * 1000)))) }
        _ = try await call("scrobble", q)
    }

    /// Сохраняет очередь на сервере, чтобы продолжить с другого устройства.
    public func savePlayQueue(ids: [String], current: String?, positionMs: Int) async throws {
        var q = ids.map { URLQueryItem(name: "id", value: $0) }
        if let current { q.append(.init(name: "current", value: current)) }
        q.append(.init(name: "position", value: String(positionMs)))
        _ = try await call("savePlayQueue", q)
    }

    public struct ServerPlayQueue: Sendable {
        public let songs: [Song]
        public let current: String?
        public let positionMs: Int
        public let changed: Date?
        public let changedBy: String?
    }

    /// Очередь, сохранённая на сервере (nil — пусто или сервер не поддерживает).
    public func playQueue() async throws -> ServerPlayQueue? {
        let body = try await call("getPlayQueue")
        guard let q = body["playQueue"] as? [String: Any] else { return nil }
        let songs = try decode([Song].self, from: q["entry"])
        guard !songs.isEmpty else { return nil }
        let changed = (q["changed"] as? String).flatMap { Self.parseDate($0) }
        return ServerPlayQueue(songs: songs, current: q["current"].map { "\($0)" },
                               positionMs: q["position"] as? Int ?? 0, changed: changed, changedBy: q["changedBy"] as? String)
    }

    public static func parseDate(_ text: String) -> Date? {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let d = f.date(from: text) { return d }
        f.formatOptions = [.withInternetDateTime]
        return f.date(from: text)
    }

    // MARK: Избранное, оценки и тексты

    /// Публичная ссылка на песню (`createShare`). Бросает ошибку, если на сервере шары выключены.
    public func createShare(songID: String) async throws -> URL {
        let body = try await call("createShare", [.init(name: "id", value: songID)])
        let shares = (body["shares"] as? [String: Any])?["share"] as? [[String: Any]]
        guard let s = shares?.first?["url"] as? String, let url = URL(string: s) else { throw SubsonicError.decoding }
        return url
    }

    /// Оценка 1–5; 0 снимает оценку.
    public func setRating(_ rating: Int, songID: String) async throws {
        _ = try await call("setRating", [.init(name: "id", value: songID), .init(name: "rating", value: String(max(0, min(5, rating))))])
    }

    public func setStarred(_ starred: Bool, songID: String) async throws {
        _ = try await call(starred ? "star" : "unstar", [.init(name: "id", value: songID)])
    }

    /// Текст трека. Сначала OpenSubsonic (getLyricsBySongId: умеет тайминги), затем классический getLyrics.
    public func lyrics(for song: Song) async -> Lyrics? {
        if let body = try? await call("getLyricsBySongId", [.init(name: "id", value: song.id)]),
           let list = (body["lyricsList"] as? [String: Any])?["structuredLyrics"] as? [[String: Any]], !list.isEmpty {
            let best = list.first { $0["synced"] as? Bool == true } ?? list[0]
            let offset = best["offset"] as? Int ?? 0
            let raw = best["line"] as? [[String: Any]] ?? []
            let lines = raw.enumerated().compactMap { i, l -> Lyrics.Line? in
                guard let text = l["value"] as? String else { return nil }
                return .init(id: i, startMs: (l["start"] as? Int).map { $0 + offset }, text: text)
            }
            if !lines.isEmpty { return Lyrics(lines: lines) }
        }
        guard let artist = song.artist,
              let body = try? await call("getLyrics", [.init(name: "artist", value: artist), .init(name: "title", value: song.title)]),
              let text = (body["lyrics"] as? [String: Any])?["value"] as? String,
              !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return nil }
        let lines = text.split(separator: "\n", omittingEmptySubsequences: false).enumerated().map {
            Lyrics.Line(id: $0.offset, startMs: nil, text: String($0.element))
        }
        return Lyrics(lines: lines)
    }

    public func search(_ query: String, limit: Int = 20, musicFolderId: String? = nil) async throws -> SearchResults {
        var q: [URLQueryItem] = [
            .init(name: "query", value: query), .init(name: "songCount", value: String(limit)),
            .init(name: "albumCount", value: String(limit)), .init(name: "artistCount", value: "8"),
        ]
        if let musicFolderId { q.append(.init(name: "musicFolderId", value: musicFolderId)) }
        let body = try await call("search3", q)
        let r = body["searchResult3"] as? [String: Any]
        var out = SearchResults()
        out.songs = try decode([Song].self, from: r?["song"])
        out.albums = try decode([Album].self, from: r?["album"])
        out.artists = try decode([Artist].self, from: r?["artist"])
        return out
    }

    /// Все исполнители, по алфавиту.
    public func artists(musicFolderId: String? = nil) async throws -> [Artist] {
        var q: [URLQueryItem] = []
        if let musicFolderId { q.append(.init(name: "musicFolderId", value: musicFolderId)) }
        let body = try await call("getArtists", q)
        let indexes = (body["artists"] as? [String: Any])?["index"] as? [[String: Any]] ?? []
        var all: [Artist] = []
        for index in indexes { all += try decode([Artist].self, from: index["artist"]) }
        return all.sorted { $0.name.localizedCaseInsensitiveCompare($1.name) == .orderedAscending }
    }

    /// Все песни порциями. Пустой запрос search3 возвращает всю медиатеку (Navidrome и совместимые серверы).
    public func allSongs(offset: Int, count: Int = 100, musicFolderId: String? = nil) async throws -> [Song] {
        var q: [URLQueryItem] = [.init(name: "query", value: ""), .init(name: "songCount", value: String(count)),
                                 .init(name: "songOffset", value: String(offset)),
                                 .init(name: "albumCount", value: "0"), .init(name: "artistCount", value: "0")]
        if let musicFolderId { q.append(.init(name: "musicFolderId", value: musicFolderId)) }
        let body = try await call("search3", q)
        return try decode([Song].self, from: (body["searchResult3"] as? [String: Any])?["song"])
    }

    /// Избранное: песни, альбомы, исполнители.
    public func starred(musicFolderId: String? = nil) async throws -> SearchResults {
        var q: [URLQueryItem] = []
        if let musicFolderId { q.append(.init(name: "musicFolderId", value: musicFolderId)) }
        let body = try await call("getStarred2", q)
        let r = body["starred2"] as? [String: Any]
        var out = SearchResults()
        out.songs = try decode([Song].self, from: r?["song"])
        out.albums = try decode([Album].self, from: r?["album"])
        out.artists = try decode([Artist].self, from: r?["artist"])
        return out
    }

    public func playlists() async throws -> [Playlist] {
        let body = try await call("getPlaylists")
        return try decode([Playlist].self, from: (body["playlists"] as? [String: Any])?["playlist"])
    }

    public func playlist(id: String) async throws -> (playlist: Playlist, songs: [Song]) {
        let body = try await call("getPlaylist", [.init(name: "id", value: id)])
        guard let p = body["playlist"] as? [String: Any],
              let playlist = try? JSONDecoder().decode(Playlist.self, from: JSONSerialization.data(withJSONObject: p)) else { throw SubsonicError.decoding }
        return (playlist, try decode([Song].self, from: p["entry"]))
    }

    /// Популярные песни артиста (по данным сервера). Пустой список, если источника нет.
    public func topSongs(artist name: String, count: Int = 10) async throws -> [Song] {
        let body = try await call("getTopSongs", [.init(name: "artist", value: name), .init(name: "count", value: String(count))])
        return try decode([Song].self, from: (body["topSongs"] as? [String: Any])?["song"])
    }

    /// Биография и похожие артисты. Ошибки не пробрасываем: это необязательная часть страницы.
    public func artistInfo(id: String, similarCount: Int = 12) async -> ArtistInfo {
        guard let body = try? await call("getArtistInfo2", [.init(name: "id", value: id), .init(name: "count", value: String(similarCount))]),
              let info = body["artistInfo2"] as? [String: Any] else { return ArtistInfo() }
        let bio = (info["biography"] as? String).flatMap { Self.plainText($0) }
        let similar = (try? decode([Artist].self, from: info["similarArtist"])) ?? []
        return ArtistInfo(biography: bio, similar: similar)
    }

    /// Убирает HTML-ссылки и теги из биографии (Last.fm добавляет «Read more on…»).
    static func plainText(_ html: String) -> String? {
        var s = html.replacingOccurrences(of: "<br\\s*/?>", with: "\n", options: .regularExpression)
        s = s.replacingOccurrences(of: "<a [^>]*>.*?</a>", with: "", options: [.regularExpression])
        s = s.replacingOccurrences(of: "<[^>]+>", with: "", options: .regularExpression)
        for (k, v) in ["&amp;": "&", "&quot;": "\"", "&#39;": "'", "&lt;": "<", "&gt;": ">", "&nbsp;": " "] { s = s.replacingOccurrences(of: k, with: v) }
        s = s.trimmingCharacters(in: .whitespacesAndNewlines)
        return s.isEmpty ? nil : s
    }

    // MARK: Изменение плейлистов

    /// Создаёт плейлист и возвращает его id (если сервер его отдал).
    @discardableResult
    public func createPlaylist(name: String, songIDs: [String] = []) async throws -> String? {
        let body = try await call("createPlaylist", [.init(name: "name", value: name)] + songIDs.map { .init(name: "songId", value: $0) })
        return (body["playlist"] as? [String: Any])?["id"] as? String
    }

    public func addToPlaylist(id: String, songIDs: [String]) async throws {
        _ = try await call("updatePlaylist", [.init(name: "playlistId", value: id)] + songIDs.map { .init(name: "songIdToAdd", value: $0) })
    }

    /// Удаляет песни по позициям в плейлисте (индексы с нуля).
    public func removeFromPlaylist(id: String, indexes: [Int]) async throws {
        _ = try await call("updatePlaylist", [.init(name: "playlistId", value: id)] + indexes.map { .init(name: "songIndexToRemove", value: String($0)) })
    }

    public func renamePlaylist(id: String, name: String) async throws {
        _ = try await call("updatePlaylist", [.init(name: "playlistId", value: id), .init(name: "name", value: name)])
    }

    /// Полная замена состава (так сохраняется новый порядок): `createPlaylist` с существующим id перезаписывает песни.
    public func replacePlaylistSongs(id: String, songIDs: [String]) async throws {
        _ = try await call("createPlaylist", [.init(name: "playlistId", value: id)] + songIDs.map { .init(name: "songId", value: $0) })
    }

    public func deletePlaylist(id: String) async throws {
        _ = try await call("deletePlaylist", [.init(name: "id", value: id)])
    }

    /// Исполнитель и его альбомы.
    public func artist(id: String) async throws -> (artist: Artist, albums: [Album]) {
        let body = try await call("getArtist", [.init(name: "id", value: id)])
        guard let a = body["artist"] as? [String: Any],
              let artist = try? JSONDecoder().decode(Artist.self, from: JSONSerialization.data(withJSONObject: a)) else { throw SubsonicError.decoding }
        return (artist, try decode([Album].self, from: a["album"]))
    }

    // MARK: Подкасты

    /// Каналы подкастов (с выпусками или без). Бросает ошибку, если сервер не поддерживает подкасты.
    public func podcasts(includeEpisodes: Bool = true) async throws -> [PodcastChannel] {
        let body = try await call("getPodcasts", [.init(name: "includeEpisodes", value: includeEpisodes ? "true" : "false")])
        return try decode([PodcastChannel].self, from: (body["podcasts"] as? [String: Any])?["channel"])
    }

    public func newestPodcasts(count: Int = 30) async throws -> [PodcastEpisode] {
        let body = try await call("getNewestPodcasts", [.init(name: "count", value: String(count))])
        return try decode([PodcastEpisode].self, from: (body["newestPodcasts"] as? [String: Any])?["episode"])
    }

    /// Выпуски одного канала (`getPodcasts` с id канала).
    public func podcastEpisodes(channelID: String) async throws -> [PodcastEpisode] {
        let body = try await call("getPodcasts", [.init(name: "id", value: channelID), .init(name: "includeEpisodes", value: "true")])
        let channels = try decode([PodcastChannel].self, from: (body["podcasts"] as? [String: Any])?["channel"])
        return channels.first?.episodes ?? []
    }

    public func refreshPodcasts() async throws { _ = try await call("refreshPodcasts") }
    public func createPodcastChannel(url: String) async throws { _ = try await call("createPodcastChannel", [.init(name: "url", value: url)]) }
    public func deletePodcastChannel(id: String) async throws { _ = try await call("deletePodcastChannel", [.init(name: "id", value: id)]) }
    /// Просит сервер скачать выпуск.
    public func downloadPodcastEpisode(id: String) async throws { _ = try await call("downloadPodcastEpisode", [.init(name: "id", value: id)]) }
    public func deletePodcastEpisode(id: String) async throws { _ = try await call("deletePodcastEpisode", [.init(name: "id", value: id)]) }

    // MARK: Интернет-радио

    public func radioStations() async throws -> [RadioStation] {
        let body = try await call("getInternetRadioStations")
        return try decode([RadioStation].self, from: (body["internetRadioStations"] as? [String: Any])?["internetRadioStation"])
    }

    public func createRadioStation(name: String, streamUrl: String, homePageUrl: String?) async throws {
        _ = try await call("createInternetRadioStation", Self.radioQuery(name, streamUrl, homePageUrl))
    }

    public func updateRadioStation(id: String, name: String, streamUrl: String, homePageUrl: String?) async throws {
        _ = try await call("updateInternetRadioStation", [.init(name: "id", value: id)] + Self.radioQuery(name, streamUrl, homePageUrl))
    }

    public func deleteRadioStation(id: String) async throws { _ = try await call("deleteInternetRadioStation", [.init(name: "id", value: id)]) }

    private static func radioQuery(_ name: String, _ streamUrl: String, _ home: String?) -> [URLQueryItem] {
        var q: [URLQueryItem] = [.init(name: "name", value: name), .init(name: "streamUrl", value: streamUrl)]
        if let home, !home.isEmpty { q.append(.init(name: "homepageUrl", value: home)) }
        return q
    }

    private func decode<T: Decodable>(_ type: T.Type, from json: Any?) throws -> T where T: RangeReplaceableCollection {
        guard let json else { return T() }
        let data = try JSONSerialization.data(withJSONObject: json)
        do { return try JSONDecoder().decode(T.self, from: data) } catch { throw SubsonicError.decoding }
    }
}
