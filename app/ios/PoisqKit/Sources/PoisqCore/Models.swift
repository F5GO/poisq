import Foundation

public struct Song: Identifiable, Hashable, Sendable, Codable {
    public let id: String
    public let title: String
    public let artist: String?
    public let album: String?
    public let albumId: String?
    public let artistId: String?
    public let coverArt: String?
    public let duration: Int?
    public let track: Int?
    public let suffix: String?
    // Технические данные (есть у большинства серверов; OpenSubsonic добавляет разрядность и частоту)
    public let bitRate: Int?
    public let size: Int?
    public let year: Int?
    public let genre: String?
    public let path: String?
    public let contentType: String?
    public let bitDepth: Int?
    public let samplingRate: Int?
    public let channelCount: Int?
    /// Дата добавления в избранное; nil — не в избранном.
    public let starred: String?
    /// Оценка пользователя 1–5; nil — не оценена.
    public let userRating: Int?
    /// Данные ReplayGain (OpenSubsonic); nil — сервер их не отдаёт.
    public let replayGain: ReplayGain?
    /// Все исполнители песни с их id (OpenSubsonic); nil — сервер отдаёт только одну строку `artist`.
    public let artists: [Artist]?

    /// Исполнители для показа: «A, B и C» по правилам языка, если их несколько, иначе строка `artist`.
    public var displayArtist: String? {
        guard let artists, artists.count > 1 else { return artist }
        return ListFormatter.localizedString(byJoining: artists.map(\.name))
    }

    public init(id: String, title: String, artist: String? = nil, album: String? = nil,
                albumId: String? = nil, artistId: String? = nil, coverArt: String? = nil, duration: Int? = nil,
                track: Int? = nil, suffix: String? = nil, bitRate: Int? = nil, size: Int? = nil,
                year: Int? = nil, genre: String? = nil, path: String? = nil, contentType: String? = nil,
                bitDepth: Int? = nil, samplingRate: Int? = nil, channelCount: Int? = nil, starred: String? = nil, userRating: Int? = nil, replayGain: ReplayGain? = nil, artists: [Artist]? = nil) {
        self.id = id; self.title = title; self.artist = artist; self.album = album
        self.albumId = albumId; self.artistId = artistId; self.coverArt = coverArt; self.duration = duration
        self.track = track; self.suffix = suffix; self.bitRate = bitRate; self.size = size
        self.year = year; self.genre = genre; self.path = path; self.contentType = contentType
        self.bitDepth = bitDepth; self.samplingRate = samplingRate; self.channelCount = channelCount
        self.starred = starred; self.userRating = userRating; self.replayGain = replayGain
        self.artists = artists
    }
}

/// ReplayGain из OpenSubsonic: усиление в дБ, чтобы выровнять громкость, и пики для защиты от клиппинга.
public struct ReplayGain: Hashable, Sendable, Codable {
    public let trackGain: Double?
    public let albumGain: Double?
    public let trackPeak: Double?
    public let albumPeak: Double?
    public let baseGain: Double?
    public let fallbackGain: Double?

    /// Усиление в дБ для режима; пик ограничивает его, чтобы не получить клиппинг. nil — данных нет.
    public func gainDb(album: Bool) -> Double? {
        let g = album ? (albumGain ?? trackGain) : (trackGain ?? albumGain)
        guard var db = g ?? fallbackGain else { return nil }
        let peak = album ? (albumPeak ?? trackPeak) : (trackPeak ?? albumPeak)
        if let peak, peak > 0 { db = min(db, -20 * log10(peak)) }
        return min(max(db, -24), 24)
    }
}

/// Текст песни. Если сервер отдаёт построчные тайминги, `isSynced` = true.
public struct Lyrics: Sendable, Equatable {
    public struct Line: Sendable, Equatable, Identifiable {
        public let id: Int
        /// Начало строки в миллисекундах; nil у несинхронизированного текста.
        public let startMs: Int?
        public let text: String
        public init(id: Int, startMs: Int?, text: String) { self.id = id; self.startMs = startMs; self.text = text }
    }
    public let lines: [Line]
    public init(lines: [Line]) { self.lines = lines }
    public var isSynced: Bool { lines.contains { $0.startMs != nil } }
}

public struct Album: Identifiable, Hashable, Sendable, Codable {
    public let id: String
    public let name: String
    public let artist: String?
    public let artistId: String?
    /// Все исполнители альбома с их id (OpenSubsonic).
    public let artists: [Artist]?
    public let coverArt: String?
    public let songCount: Int?
    public let year: Int?

    public init(id: String, name: String, artist: String? = nil, artistId: String? = nil, artists: [Artist]? = nil, coverArt: String? = nil, songCount: Int? = nil, year: Int? = nil) {
        self.id = id; self.name = name; self.artist = artist; self.artistId = artistId; self.artists = artists; self.coverArt = coverArt
        self.songCount = songCount; self.year = year
    }
}

public struct Artist: Identifiable, Hashable, Sendable, Codable {
    public let id: String
    public let name: String
    public let albumCount: Int?
    public let coverArt: String?

    public init(id: String, name: String, albumCount: Int? = nil, coverArt: String? = nil) {
        self.id = id; self.name = name; self.albumCount = albumCount; self.coverArt = coverArt
    }
}

/// Описание артиста и похожие исполнители (`getArtistInfo2`). Пусто, если на сервере не настроен источник данных.
public struct ArtistInfo: Sendable {
    public var biography: String?
    public var similar: [Artist]
    public init(biography: String? = nil, similar: [Artist] = []) { self.biography = biography; self.similar = similar }
}

public struct Playlist: Identifiable, Hashable, Sendable, Codable {
    public let id: String
    public let name: String
    public let songCount: Int?
    public let duration: Int?
    public let coverArt: String?
    public let owner: String?
    public let comment: String?
}

public struct SearchResults: Sendable {
    public var songs: [Song] = []
    public var albums: [Album] = []
    public var artists: [Artist] = []
    public var isEmpty: Bool { songs.isEmpty && albums.isEmpty && artists.isEmpty }
    public init() {}
}

/// Медиатека на сервере (Subsonic «music folder»). Имя задаёт сервер; своё имя хранит приложение.
public struct MusicFolder: Identifiable, Hashable, Sendable {
    public let id: String
    public let name: String
    public init(id: String, name: String) { self.id = id; self.name = name }
}

// MARK: Подкасты и интернет-радио (стандартный Subsonic API)

/// Выпуск подкаста. Играть можно только скачанный на сервере (`streamId` есть, статус `completed`).
public struct PodcastEpisode: Identifiable, Hashable, Sendable, Codable {
    public let id: String
    /// id для `stream`; сервер отдаёт его только у скачанных выпусков.
    public let streamId: String?
    public let channelId: String?
    public let title: String
    public let description: String?
    public let publishDate: String?
    public let status: String?
    public let duration: Int?
    public let coverArt: String?
    public let size: Int?
    public let suffix: String?
    public let contentType: String?

    public var isDownloaded: Bool { streamId != nil && (status == nil || status == "completed") }
    public var isDownloading: Bool { status == "downloading" }
    public var published: Date? { publishDate.flatMap { SubsonicClient.parseDate($0) } }
}

public struct PodcastChannel: Identifiable, Hashable, Sendable, Codable {
    public let id: String
    public let url: String?
    public let title: String?
    public let description: String?
    public let coverArt: String?
    public let status: String?
    public let errorMessage: String?
    public var episode: [PodcastEpisode]?
    public var displayTitle: String { title ?? url ?? id }
    public var episodes: [PodcastEpisode] { episode ?? [] }
}

public struct RadioStation: Identifiable, Hashable, Sendable, Codable {
    public let id: String
    public let name: String
    public let streamUrl: String
    public let homePageUrl: String?
}
