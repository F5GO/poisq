import AppIntents
import CoreSpotlight
import PoisqCore

// Сущности для Siri, Команд и Spotlight. Данные берём с активного сервера через AppModel.

@MainActor private var client: SubsonicClient? { AppModel.shared.client }

struct PlaylistEntity: AppEntity, IndexedEntity {
    static let typeDisplayRepresentation = TypeDisplayRepresentation(name: "Playlist")
    static let defaultQuery = PlaylistQuery()

    var id: String
    @Property(title: "Name") var name: String

    init(_ p: Playlist) { id = p.id; name = p.name }
    var displayRepresentation: DisplayRepresentation { DisplayRepresentation(title: "\(name)") }

    var attributeSet: CSSearchableItemAttributeSet {
        let a = defaultAttributeSet
        a.displayName = name
        a.contentDescription = String(localized: "Playlist")
        return a
    }
}

struct PlaylistQuery: EntityStringQuery {
    @MainActor func entities(for identifiers: [String]) async throws -> [PlaylistEntity] {
        let all = try await client?.playlists() ?? []
        return all.filter { identifiers.contains($0.id) }.map(PlaylistEntity.init)
    }
    @MainActor func entities(matching string: String) async throws -> [PlaylistEntity] {
        let all = try await client?.playlists() ?? []
        return all.filter { $0.name.localizedStandardContains(string) }.map(PlaylistEntity.init)
    }
    @MainActor func suggestedEntities() async throws -> [PlaylistEntity] {
        (try await client?.playlists() ?? []).map(PlaylistEntity.init)
    }
}

struct AlbumEntity: AppEntity, IndexedEntity {
    static let typeDisplayRepresentation = TypeDisplayRepresentation(name: "Album")
    static let defaultQuery = AlbumQuery()

    var id: String
    @Property(title: "Title") var name: String
    @Property(title: "Artist") var artist: String?

    init(_ a: Album) { id = a.id; name = a.name; artist = a.artist }
    var displayRepresentation: DisplayRepresentation { DisplayRepresentation(title: "\(name)", subtitle: artist.map { "\($0)" }) }

    var attributeSet: CSSearchableItemAttributeSet {
        let a = defaultAttributeSet
        a.displayName = name
        a.artist = artist
        a.contentDescription = [String(localized: "Album"), artist].compactMap { $0 }.joined(separator: " · ")
        return a
    }
}

struct AlbumQuery: EntityStringQuery {
    @MainActor func entities(for identifiers: [String]) async throws -> [AlbumEntity] {
        guard let client else { return [] }
        var out: [AlbumEntity] = []
        for id in identifiers { if let r = try? await client.album(id: id) { out.append(AlbumEntity(r.album)) } }
        return out
    }
    @MainActor func entities(matching string: String) async throws -> [AlbumEntity] {
        try await client?.search(string, limit: 10).albums.map(AlbumEntity.init) ?? []
    }
    @MainActor func suggestedEntities() async throws -> [AlbumEntity] {
        try await client?.albumList(type: "newest", size: 10).map(AlbumEntity.init) ?? []
    }
}

struct ArtistEntity: AppEntity, IndexedEntity {
    static let typeDisplayRepresentation = TypeDisplayRepresentation(name: "Artist")
    static let defaultQuery = ArtistQuery()

    var id: String
    @Property(title: "Name") var name: String

    init(_ a: Artist) { id = a.id; name = a.name }
    var displayRepresentation: DisplayRepresentation { DisplayRepresentation(title: "\(name)") }

    var attributeSet: CSSearchableItemAttributeSet {
        let a = defaultAttributeSet
        a.displayName = name
        a.contentDescription = String(localized: "Artist")
        return a
    }
}

struct ArtistQuery: EntityStringQuery {
    @MainActor func entities(for identifiers: [String]) async throws -> [ArtistEntity] {
        let all = try await client?.artists() ?? []
        return all.filter { identifiers.contains($0.id) }.map(ArtistEntity.init)
    }
    @MainActor func entities(matching string: String) async throws -> [ArtistEntity] {
        try await client?.search(string, limit: 10).artists.map(ArtistEntity.init) ?? []
    }
    @MainActor func suggestedEntities() async throws -> [ArtistEntity] { [] }
}
