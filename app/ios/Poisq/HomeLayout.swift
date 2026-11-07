import SwiftUI

/// Блок главной, который можно скрыть или переставить.
enum HomeItem: String, CaseIterable, Codable, Identifiable {
    case upNext, pinned, recent, newest, frequent, favorites, random, playlists, favoriteSongs, favoriteArtists
    var id: String { rawValue }

    var title: LocalizedStringKey {
        switch self {
        case .upNext: "Up Next"
        case .recent: "Recently Played"
        case .newest: "Recently Added"
        case .frequent: "Most Played"
        case .favorites: "Favorite Albums"
        case .random: "Discover"
        case .pinned: "Pinned"
        case .playlists: "Playlists"
        case .favoriteSongs: "Favorite Songs"
        case .favoriteArtists: "Favorite Artists"
        }
    }

    var icon: String {
        switch self {
        case .upNext: "music.note"
        case .favorites, .favoriteSongs, .favoriteArtists: "heart.fill"
        case .pinned: "pin.fill"
        case .playlists: "music.note.list"
        default: "square.stack"
        }
    }

    /// Тип списка в getAlbumList2; nil — блок не из альбомов.
    var albumListType: String? {
        switch self {
        case .upNext, .pinned, .playlists, .favoriteSongs, .favoriteArtists: nil
        case .recent: "recent"
        case .newest: "newest"
        case .frequent: "frequent"
        case .favorites: "starred"
        case .random: "random"
        }
    }

    /// Включено по умолчанию. «Избранное» по умолчанию скрыто, чтобы главная не была пустой полкой у новых серверов.
    var defaultVisible: Bool { ![.favorites, .playlists, .favoriteSongs, .favoriteArtists].contains(self) }
}

/// Порядок и видимость блоков главной. Хранится на устройстве, общий для всех серверов.
@MainActor @Observable
final class HomeLayout {
    struct Entry: Codable, Identifiable, Equatable {
        var item: HomeItem
        var isVisible: Bool
        var id: String { item.id }
    }

    private(set) var entries: [Entry]
    private static let key = "homeLayout"

    init() {
        let saved = UserDefaults.standard.data(forKey: Self.key).flatMap { try? JSONDecoder().decode([Entry].self, from: $0) } ?? []
        // Новые блоки из будущих версий добавляются в конец, исчезнувшие — отбрасываются.
        let known = saved.filter { s in HomeItem.allCases.contains(s.item) }
        let missing = HomeItem.allCases.filter { c in !known.contains { $0.item == c } }
        entries = known + missing.map { Entry(item: $0, isVisible: $0.defaultVisible) }
    }

    var visible: [HomeItem] { entries.filter(\.isVisible).map(\.item) }

    func move(from: IndexSet, to: Int) {
        entries.move(fromOffsets: from, toOffset: to)
        save()
    }

    func setVisible(_ item: HomeItem, _ value: Bool) {
        guard let i = entries.firstIndex(where: { $0.item == item }) else { return }
        entries[i].isVisible = value
        save()
    }

    func reset() {
        entries = HomeItem.allCases.map { Entry(item: $0, isVisible: $0.defaultVisible) }
        save()
    }

    var isDefault: Bool { entries == HomeItem.allCases.map { Entry(item: $0, isVisible: $0.defaultVisible) } }

    private func save() {
        UserDefaults.standard.set(try? JSONEncoder().encode(entries), forKey: Self.key)
    }
}
