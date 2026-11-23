import SwiftUI

/// Чип-источник над карточкой плеера: нажатие сразу включает музыку. Состав и порядок настраиваются отдельно от блоков.
enum HomeQuickAction: String, CaseIterable, Codable, Identifiable {
    case flow, shuffleAll, downloads, favoriteSongs, newest, randomAlbum
    var id: String { rawValue }

    var title: LocalizedStringKey {
        switch self {
        case .flow: "Flow"
        case .shuffleAll: "Shuffle Library"
        case .randomAlbum: "Random Album"
        case .favoriteSongs: "Loved"
        case .downloads: "Downloaded"
        case .newest: "New"
        }
    }

    var icon: String {
        switch self {
        case .flow: "infinity"
        case .shuffleAll: "shuffle"
        case .newest: "sparkles"
        case .randomAlbum: "square.stack"
        case .favoriteSongs: "heart.fill"
        case .downloads: "arrow.down.circle.fill"
        }
    }
}

/// Блок главной, который можно скрыть или переставить.
enum HomeItem: String, CaseIterable, Codable, Identifiable {
    case quickPicks, quickActions, upNext, stations, recentSongs, newSongs, rediscover, pinned, recent, newest, frequent, favorites, random, playlists, favoriteSongs, favoriteArtists
    var id: String { rawValue }

    var title: LocalizedStringKey {
        switch self {
        case .quickActions: "Sources"
        case .quickPicks: "Quick Picks"
        case .stations: "Stations"
        case .recentSongs: "Recent Tracks"
        case .newSongs: "New in Library"
        case .rediscover: "Haven't Heard in a While"
        case .upNext: "Continue"
        case .recent: "Recently Played"
        case .newest: "Recently Added"
        case .frequent: "Most Played"
        case .favorites: "Favorite Albums"
        case .random: "Discover"
        case .pinned: "Quick Access"
        case .playlists: "Playlists"
        case .favoriteSongs: "Favorite Songs"
        case .favoriteArtists: "Favorite Artists"
        }
    }

    var icon: String {
        switch self {
        case .quickActions: "square.grid.2x2"
        case .upNext: "play.circle"
        case .quickPicks: "music.note"
        case .stations: "dot.radiowaves.left.and.right"
        case .recentSongs: "clock.arrow.circlepath"
        case .newSongs: "sparkles"
        case .rediscover: "arrow.counterclockwise.heart"
        case .favorites, .favoriteSongs, .favoriteArtists: "heart.fill"
        case .pinned: "rectangle.grid.2x2"
        case .playlists: "music.note.list"
        default: "square.stack"
        }
    }

    /// Тип списка в getAlbumList2; nil — блок не из альбомов.
    var albumListType: String? {
        switch self {
        case .quickActions, .quickPicks, .upNext, .stations, .recentSongs, .newSongs, .rediscover, .pinned, .playlists, .favoriteSongs, .favoriteArtists: nil
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
    private(set) var quickActionEntries: [QuickActionEntry]
    private static let key = "homeLayout"
    private static let quickActionsKey = "homeQuickActions"
    /// Главная вокруг треков: быстрый выбор наверху, чипы источников и «Продолжить» — в зоне большого пальца.
    private static let tracksMigrationKey = "homeLayoutTracksV4"
    /// Трековые блоки ниже станций: новые блоки иначе встали бы в самый конец главной.
    private static let trackShelvesMigrationKey = "homeLayoutTrackShelvesV5"

    struct QuickActionEntry: Codable, Identifiable, Equatable {
        var item: HomeQuickAction
        var isVisible: Bool
        var id: String { item.id }
    }

    var quickAccessShowsPins: Bool = UserDefaults.standard.object(forKey: "quickAccessShowsPins") as? Bool ?? true {
        didSet { UserDefaults.standard.set(quickAccessShowsPins, forKey: "quickAccessShowsPins") }
    }
    var quickAccessShowsRecent: Bool = UserDefaults.standard.object(forKey: "quickAccessShowsRecent") as? Bool ?? true {
        didSet { UserDefaults.standard.set(quickAccessShowsRecent, forKey: "quickAccessShowsRecent") }
    }
    var quickAccessLimit: Int = {
        let saved = UserDefaults.standard.integer(forKey: "quickAccessLimit")
        return [2, 4, 6].contains(saved) ? saved : 4
    }() {
        didSet { UserDefaults.standard.set(quickAccessLimit, forKey: "quickAccessLimit") }
    }
    var recentlyPlayedLimit: Int = {
        let saved = UserDefaults.standard.integer(forKey: "recentlyPlayedLimit")
        return (1...6).contains(saved) ? saved : 6
    }() {
        didSet { UserDefaults.standard.set(recentlyPlayedLimit, forKey: "recentlyPlayedLimit") }
    }

    init() {
        let savedActions = UserDefaults.standard.data(forKey: Self.quickActionsKey)
            .flatMap { try? JSONDecoder().decode([QuickActionEntry].self, from: $0) } ?? []
        let knownActions = savedActions.filter { saved in HomeQuickAction.allCases.contains(saved.item) }
        let missingActions = HomeQuickAction.allCases.filter { item in !knownActions.contains { $0.item == item } }
        let mergedActions = knownActions + missingActions.map { QuickActionEntry(item: $0, isVisible: true) }
        // Чипы заменили панель действий: один раз ставим их в новый порядок, видимость сохраняем.
        quickActionEntries = UserDefaults.standard.bool(forKey: Self.tracksMigrationKey) ? mergedActions
            : HomeQuickAction.allCases.compactMap { item in mergedActions.first { $0.item == item } }

        let saved = UserDefaults.standard.data(forKey: Self.key).flatMap { try? JSONDecoder().decode([Entry].self, from: $0) } ?? []
        // Новые блоки из будущих версий добавляются в конец, исчезнувшие — отбрасываются.
        let known = saved.filter { s in HomeItem.allCases.contains(s.item) }
        let missing = HomeItem.allCases.filter { c in !known.contains { $0.item == c } }
        let complete = known + missing.map { Entry(item: $0, isVisible: $0.defaultVisible) }
        if !UserDefaults.standard.bool(forKey: Self.tracksMigrationKey) {
            let leading: [HomeItem] = [.quickPicks, .quickActions, .upNext, .stations]
            entries = leading.compactMap { item in complete.first { $0.item == item } }
                + complete.filter { !leading.contains($0.item) }
            UserDefaults.standard.set(true, forKey: Self.tracksMigrationKey)
            save()
            saveQuickActions()
        } else {
            entries = complete
        }
        if !UserDefaults.standard.bool(forKey: Self.trackShelvesMigrationKey) {
            let added: [HomeItem] = [.recentSongs, .newSongs, .rediscover]
            var rest = entries.filter { !added.contains($0.item) }
            let moved = added.compactMap { item in entries.first { $0.item == item } }
            let index = rest.firstIndex { $0.item == .stations }.map { $0 + 1 } ?? rest.count
            rest.insert(contentsOf: moved, at: index)
            entries = rest
            UserDefaults.standard.set(true, forKey: Self.trackShelvesMigrationKey)
            save()
        }
    }

    var visible: [HomeItem] { entries.filter(\.isVisible).map(\.item) }
    var visibleQuickActions: [HomeQuickAction] { quickActionEntries.filter(\.isVisible).map(\.item) }

    func move(from: IndexSet, to: Int) {
        entries.move(fromOffsets: from, toOffset: to)
        save()
    }

    func setVisible(_ item: HomeItem, _ value: Bool) {
        guard let i = entries.firstIndex(where: { $0.item == item }) else { return }
        entries[i].isVisible = value
        save()
    }

    func moveQuickActions(from: IndexSet, to: Int) {
        quickActionEntries.move(fromOffsets: from, toOffset: to)
        saveQuickActions()
    }

    func setQuickActionVisible(_ item: HomeQuickAction, _ value: Bool) {
        guard let i = quickActionEntries.firstIndex(where: { $0.item == item }) else { return }
        quickActionEntries[i].isVisible = value
        saveQuickActions()
    }

    func reset() {
        entries = HomeItem.allCases.map { Entry(item: $0, isVisible: $0.defaultVisible) }
        quickAccessShowsPins = true
        quickAccessShowsRecent = true
        quickAccessLimit = 4
        recentlyPlayedLimit = 6
        quickActionEntries = HomeQuickAction.allCases.map { QuickActionEntry(item: $0, isVisible: true) }
        save()
        saveQuickActions()
    }

    var isDefault: Bool {
        entries == HomeItem.allCases.map { Entry(item: $0, isVisible: $0.defaultVisible) }
            && quickAccessShowsPins && quickAccessShowsRecent && quickAccessLimit == 4
            && recentlyPlayedLimit == 6
            && quickActionEntries == HomeQuickAction.allCases.map { QuickActionEntry(item: $0, isVisible: true) }
    }

    private func save() {
        UserDefaults.standard.set(try? JSONEncoder().encode(entries), forKey: Self.key)
    }

    private func saveQuickActions() {
        UserDefaults.standard.set(try? JSONEncoder().encode(quickActionEntries), forKey: Self.quickActionsKey)
    }
}
