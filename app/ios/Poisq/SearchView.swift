import SwiftUI
import PoisqCore
import PoisqPlayback

/// Поиск. Работает с любым Subsonic-сервером: если ничего не найдено, пробует исправить раскладку («rbyj» → «кино»).
/// Сервер poisq делает это и сам, поэтому для него повторный запрос не понадобится.
struct SearchView: View {
    @Environment(AppModel.self) private var model
    @State private var query = ""
    @State private var results = SearchResults()
    @State private var state: LoadState = .idle
    @State private var recents = RecentSearches()

    private enum LoadState { case idle, loading, loaded, failed }
    private var trimmed: String { query.trimmingCharacters(in: .whitespaces) }

    var body: some View {
        NavigationStack {
            content
                .navigationTitle("Search")
                .searchable(text: $query, prompt: "Artists, albums, songs")
                .searchSuggestions { }          // пустой список: убирает системные подсказки поверх нашего содержимого
                .onSubmit(of: .search) { recents.add(trimmed) }
                .task(id: SearchKey(text: trimmed, context: model.contentKey + "\(model.offline)")) { await runSearch() }
                .musicDestinations()
        }
    }

    private struct SearchKey: Hashable { let text: String; let context: String }

    @ViewBuilder private var content: some View {
        if trimmed.isEmpty {
            RecentsList(recents: recents) { query = $0 }
        } else if results.isEmpty {
            switch state {
            case .loading, .idle: ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
            case .failed: ContentUnavailableView("Couldn't load", systemImage: "wifi.exclamationmark", description: Text("Check the connection to the server."))
            case .loaded: ContentUnavailableView.search(text: trimmed)
            }
        } else {
            resultsList
        }
    }

    private var resultsList: some View {
        List {
            if !results.artists.isEmpty {
                Section("Artists") {
                    ForEach(results.artists) { artist in
                        NavigationLink(value: artist) { ArtistRow(artist: artist) }
                    }
                }
            }
            if !results.albums.isEmpty {
                Section("Albums") {
                    ForEach(results.albums) { album in
                        NavigationLink(value: album) { MediaRow(coverID: album.coverArt, title: album.name, subtitle: album.artist) }.albumMenu(album)
                    }
                }
            }
            if !results.songs.isEmpty {
                Section("Songs") {
                    ForEach(Array(results.songs.enumerated()), id: \.element.id) { index, song in
                        Button {
                            recents.add(trimmed)
                            model.player?.play(results.songs, startAt: index)
                        } label: { MediaRow(coverID: song.coverArt, title: song.title, subtitle: song.artist) }
                        .buttonStyle(.plain).songMenu(song)
                    }
                }
            }
        }
        .listStyle(.plain)
        .scrollDismissesKeyboard(.interactively)
    }

    private func localSearch(_ text: String) -> SearchResults {
        func matches(_ s: Song, _ q: String) -> Bool {
            [s.title, s.artist, s.album].compactMap { $0 }.contains { $0.localizedStandardContains(q) }
        }
        var found = model.downloadedSongs.filter { matches($0, text) }
        if found.isEmpty, let fixed = KeyboardLayout.swapped(text) { found = model.downloadedSongs.filter { matches($0, fixed) } }
        var out = SearchResults()
        out.songs = found
        return out
    }

    private func runSearch() async {
        guard !trimmed.isEmpty else { results = SearchResults(); state = .idle; return }
        try? await Task.sleep(for: .milliseconds(250))          // ждём, пока пользователь допечатает
        guard !Task.isCancelled, let client = model.client else { return }
        state = .loading
        if model.offline {                       // офлайн: ищем среди загруженных песен
            results = localSearch(trimmed)
            state = .loaded
            return
        }
        do {
            var found = try await client.search(trimmed, musicFolderId: model.selectedLibraryID)
            // Запасной путь для серверов без умного поиска.
            if found.isEmpty, !model.capabilities.hasPoisq, let fixed = KeyboardLayout.swapped(trimmed) {
                found = try await client.search(fixed, musicFolderId: model.selectedLibraryID)
            }
            guard !Task.isCancelled else { return }
            results = found
            state = .loaded
        } catch {
            if !Task.isCancelled { results = SearchResults(); state = .failed }
        }
    }
}

struct MediaRow: View {
    let coverID: String?
    let title: String
    let subtitle: String?
    var highlighted = false
    var body: some View {
        HStack(spacing: 12) {
            CoverArtView(id: coverID, size: 48, radius: 8)
            VStack(alignment: .leading, spacing: 2) {
                Text(title).lineLimit(1).foregroundStyle(highlighted ? Color.accentColor : .primary)
                if let subtitle { Text(subtitle).font(.subheadline).foregroundStyle(.secondary).lineLimit(1) }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .contentShape(.rect)   // нажимается вся строка, а не только текст
    }
}

struct ArtistRow: View {
    let artist: Artist
    var body: some View {
        HStack(spacing: 12) {
            CoverArtView(id: artist.coverArt, size: 48, radius: 24)
            Text(artist.name).lineLimit(1)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .contentShape(.rect)
    }
}

/// Последние запросы, пока строка поиска пуста.
private struct RecentsList: View {
    let recents: RecentSearches
    let onSelect: (String) -> Void

    var body: some View {
        if recents.items.isEmpty {
            ContentUnavailableView("Search Your Music", systemImage: "magnifyingglass", description: Text("Find artists, albums and songs."))
        } else {
            List {
                Section {
                    ForEach(recents.items, id: \.self) { item in
                        Button { onSelect(item) } label: {
                            Label(item, systemImage: "clock").foregroundStyle(.primary)
                        }
                    }
                    .onDelete { recents.remove(at: $0) }
                } header: {
                    HStack {
                        Text("Recent Searches")
                        Spacer()
                        Button("Clear") { recents.clear() }.font(.subheadline).textCase(nil)
                    }
                }
            }
        }
    }
}

/// Недавние поисковые запросы (на устройстве, до 10 штук).
@MainActor @Observable
final class RecentSearches {
    private(set) var items: [String]
    private static let key = "recentSearches"

    init() { items = UserDefaults.standard.stringArray(forKey: Self.key) ?? [] }

    func add(_ text: String) {
        let t = text.trimmingCharacters(in: .whitespaces)
        guard t.count > 1 else { return }
        items.removeAll { $0.caseInsensitiveCompare(t) == .orderedSame }
        items.insert(t, at: 0)
        items = Array(items.prefix(10))
        save()
    }
    func remove(at offsets: IndexSet) { items.remove(atOffsets: offsets); save() }
    func clear() { items = []; save() }
    private func save() { UserDefaults.standard.set(items, forKey: Self.key) }
}
