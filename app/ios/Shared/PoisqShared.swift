import Foundation
import AppIntents
import ActivityKit

// Код, общий для приложения и расширения виджетов (оба таргета компилируют папку Shared/).
// Расширение не ходит в сеть: оно читает только то, что приложение положило в общий контейнер App Group.

enum PoisqShared {
    static let appGroup = "group.app.poisq.ios"
    static let scheme = "poisq"

    static var container: URL? { FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: appGroup) }
    static var coversDirectory: URL? {
        guard let dir = container?.appending(path: "widget-covers", directoryHint: .isDirectory) else { return nil }
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir
    }
    static func coverURL(_ file: String?) -> URL? {
        guard let file, !file.isEmpty else { return nil }
        return coversDirectory?.appending(path: file)
    }
}

/// Что сейчас играет (или играло последним). nil в хранилище — ничего.
struct SharedTrack: Codable, Equatable, Sendable {
    var title: String
    var artist: String
    var albumID: String?
    var albumName: String?
    var isPlaying: Bool
    var coverFile: String?
}

struct SharedAlbum: Codable, Equatable, Sendable, Identifiable {
    var id: String
    var name: String
    var artist: String?
    var coverFile: String?
}

/// JSON-файлы в общем контейнере. Пишет приложение, читает расширение.
enum SharedStore {
    private static func url(_ name: String) -> URL? { PoisqShared.container?.appending(path: name) }

    static func read<T: Decodable>(_ type: T.Type, _ name: String) -> T? {
        guard let url = url(name), let data = try? Data(contentsOf: url) else { return nil }
        return try? JSONDecoder().decode(type, from: data)
    }
    static func write<T: Encodable>(_ value: T?, _ name: String) {
        guard let url = url(name) else { return }
        guard let value, let data = try? JSONEncoder().encode(value) else { try? FileManager.default.removeItem(at: url); return }
        try? data.write(to: url, options: .atomic)
    }
    static var track: SharedTrack? { read(SharedTrack.self, "nowplaying.json") }
    static var recentAlbums: [SharedAlbum] { read([SharedAlbum].self, "recent-albums.json") ?? [] }
}

/// Ссылки вида poisq://album/<id>?name=…
enum DeepLink {
    enum Target: Equatable {
        case album(id: String, name: String)
        case artist(id: String, name: String)
        case playlist(id: String, name: String)
        /// Открыть полный экран плеера (нажатие на виджет «Сейчас играет» и Live Activity).
        case player
    }

    static func url(_ kind: String, id: String, name: String) -> URL {
        var c = URLComponents()
        c.scheme = PoisqShared.scheme; c.host = kind
        c.path = "/" + id
        c.queryItems = [URLQueryItem(name: "name", value: name)]
        return c.url ?? URL(string: "\(PoisqShared.scheme)://")!
    }
    static func album(id: String, name: String) -> URL { url("album", id: id, name: name) }
    static var player: URL { URL(string: "\(PoisqShared.scheme)://player")! }

    static func parse(_ url: URL) -> Target? {
        if url.scheme == PoisqShared.scheme, url.host == "player" { return .player }
        guard url.scheme == PoisqShared.scheme, let kind = url.host,
              let c = URLComponents(url: url, resolvingAgainstBaseURL: false) else { return nil }
        let id = String(c.path.dropFirst())
        guard !id.isEmpty else { return nil }
        let name = c.queryItems?.first { $0.name == "name" }?.value ?? ""
        switch kind {
        case "album": return .album(id: id, name: name)
        case "artist": return .artist(id: id, name: name)
        case "playlist": return .playlist(id: id, name: name)
        default: return nil
        }
    }
}

// MARK: Управление воспроизведением

enum PlaybackCommand: Sendable { case toggle, pause, next, previous }

enum PlaybackCommands {
    /// Ставит приложение при запуске. В расширении nil: `AudioPlaybackIntent` система исполняет в процессе приложения.
    @MainActor static var handler: ((PlaybackCommand) -> Void)?
}

struct PlayPauseIntent: AudioPlaybackIntent {
    static let title: LocalizedStringResource = "Play/Pause"
    static let isDiscoverable = false
    @MainActor func perform() async throws -> some IntentResult {
        PlaybackCommands.handler?(.toggle)
        return .result()
    }
}

struct NextTrackIntent: AudioPlaybackIntent {
    static let title: LocalizedStringResource = "Next Song"
    @MainActor func perform() async throws -> some IntentResult {
        PlaybackCommands.handler?(.next)
        return .result()
    }
}

struct PreviousTrackIntent: AudioPlaybackIntent {
    static let title: LocalizedStringResource = "Previous Song"
    @MainActor func perform() async throws -> some IntentResult {
        PlaybackCommands.handler?(.previous)
        return .result()
    }
}

// MARK: Live Activity

struct PlaybackActivityAttributes: ActivityAttributes {
    struct ContentState: Codable, Hashable {
        var title: String
        var artist: String
        var isPlaying: Bool
        var coverFile: String?
    }
}
