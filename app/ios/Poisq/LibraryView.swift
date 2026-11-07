import SwiftUI
import PoisqCore
import PoisqPlayback

/// Переходы на экраны альбома, исполнителя и плейлиста — одинаковые во всех вкладках.
struct MusicDestinations: ViewModifier {
    func body(content: Content) -> some View {
        content
            .navigationDestination(for: Album.self) { AlbumView(album: $0) }
            .navigationDestination(for: Artist.self) { ArtistView(artist: $0) }
            .navigationDestination(for: Playlist.self) { PlaylistView(playlist: $0) }
    }
}
extension View { func musicDestinations() -> some View { modifier(MusicDestinations()) } }

private enum LibrarySection: Hashable, CaseIterable {
    case downloads, artists, albums, songs, favorites, podcasts, radio
    var title: LocalizedStringKey {
        switch self { case .downloads: "Downloaded"; case .artists: "Artists"; case .albums: "Albums"; case .songs: "Songs"; case .favorites: "Favorites"; case .podcasts: "Podcasts"; case .radio: "Radio" }
    }
    var icon: String {
        switch self { case .downloads: "arrow.down.circle"; case .artists: "music.mic"; case .albums: "square.stack"; case .songs: "music.note"; case .favorites: "heart"; case .podcasts: "mic"; case .radio: "dot.radiowaves.left.and.right" }
    }
}

/// Медиатека: артисты, альбомы, песни, избранное.
struct LibraryView: View {
    @Environment(AppModel.self) private var model
    var body: some View {
        NavigationStack {
            List(LibrarySection.allCases.filter { section in
                if model.offline { return section == .downloads }
                // подкасты и радио — только если сервер их поддерживает
                return section == .podcasts ? model.capabilities.hasPodcasts : section == .radio ? model.capabilities.hasRadio : true
            }, id: \.self) { section in
                NavigationLink(value: section) {
                    Label(section.title, systemImage: section.icon).foregroundStyle(.primary)
                }
            }
            .listStyle(.plain)
            .navigationTitle("Library")
            .navigationDestination(for: LibrarySection.self) { section in
                switch section {
                case .downloads: DownloadedView()
                case .artists: ArtistsListView()
                case .albums: AlbumsListView()
                case .songs: SongsListView()
                case .favorites: FavoritesView()
                case .podcasts: PodcastsView()
                case .radio: RadioView()
                }
            }
            .musicDestinations()
        }
    }
}

private enum ArtistSort: String, CaseIterable, Identifiable {
    case name, albums
    var id: String { rawValue }
    var title: LocalizedStringKey { self == .name ? "Name" : "Most Albums" }
}

private struct ArtistsListView: View {
    @Environment(AppModel.self) private var model
    @AppStorage("artistsSort") private var sortRaw = ArtistSort.name.rawValue
    @State private var artists: [Artist] = []
    @State private var loaded = false
    private var sort: ArtistSort { ArtistSort(rawValue: sortRaw) ?? .name }

    var body: some View {
        List {
            if sort == .albums {
                ForEach(byAlbums) { artist in
                    NavigationLink(value: artist) { ArtistRow(artist: artist) }
                }
            } else {
                ForEach(grouped, id: \.letter) { group in
                    Section(group.letter) {
                        ForEach(group.artists) { artist in
                            NavigationLink(value: artist) { ArtistRow(artist: artist) }
                        }
                    }
                }
            }
        }
        .listStyle(.plain)
        .overlay { if !loaded { ProgressView() } }
        .navigationTitle("Artists")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                Menu {
                    Picker("Sort By", selection: $sortRaw) { ForEach(ArtistSort.allCases) { Text($0.title).tag($0.rawValue) } }
                } label: { Label("Sort", systemImage: "arrow.up.arrow.down") }
            }
        }
        .task(id: model.contentKey) {
            artists = (try? await model.client?.artists(musicFolderId: model.selectedLibraryID)) ?? []
            loaded = true
        }
    }

    private var byAlbums: [Artist] {
        artists.sorted { ($0.albumCount ?? 0, $1.name) > ($1.albumCount ?? 0, $0.name) }
    }

    /// Группы по первой букве; цифры и знаки — в «#».
    private var grouped: [(letter: String, artists: [Artist])] {
        let dict = Dictionary(grouping: artists) { a -> String in
            guard let first = a.name.first(where: { $0.isLetter || $0.isNumber }) else { return "#" }
            return first.isLetter ? String(first).uppercased() : "#"
        }
        return dict.keys.sorted { $0 == "#" ? false : $1 == "#" ? true : $0.localizedCompare($1) == .orderedAscending }
            .map { ($0, dict[$0] ?? []) }
    }
}

/// Порядок альбомов — типы `getAlbumList2` стандартного Subsonic API (сортирует сервер).
private enum AlbumSort: String, CaseIterable, Identifiable {
    case name = "alphabeticalByName", artist = "alphabeticalByArtist", newest, recent, frequent, highest, starred, random
    var id: String { rawValue }
    var title: LocalizedStringKey {
        switch self {
        case .name: "Title"; case .artist: "Artist"; case .newest: "Recently Added"; case .recent: "Recently Played"
        case .frequent: "Most Played"; case .highest: "Top Rated"; case .starred: "Favorites"; case .random: "Random"
        }
    }
}

private struct AlbumsListView: View {
    @AppStorage("albumsSort") private var sortRaw = AlbumSort.name.rawValue
    private var sort: AlbumSort { AlbumSort(rawValue: sortRaw) ?? .name }
    @Environment(AppModel.self) private var model
    @State private var albums: [Album] = []
    @State private var finished = false
    @State private var loading = false
    private let pageSize = 60
    private let columns = [GridItem(.adaptive(minimum: 150), spacing: 14)]

    var body: some View {
        ScrollView {
            LazyVGrid(columns: columns, spacing: 18) {
                ForEach(albums) { album in
                    NavigationLink(value: album) { AlbumCard(album: album).frame(maxWidth: .infinity, alignment: .leading) }.albumMenu(album)
                        .buttonStyle(.plain)
                        .onAppear { if album.id == albums.last?.id { Task { await loadMore() } } }
                }
            }
            .padding(20)
        }
        .overlay { if albums.isEmpty && !finished { ProgressView() } }
        .navigationTitle("Albums")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                Menu {
                    Picker("Sort By", selection: $sortRaw) { ForEach(AlbumSort.allCases) { Text($0.title).tag($0.rawValue) } }
                } label: { Label("Sort", systemImage: "arrow.up.arrow.down") }
            }
        }
        .task(id: model.contentKey + "|" + sortRaw) { albums = []; finished = false; await loadMore() }
    }

    private func loadMore() async {
        guard !loading, !finished, let client = model.client else { return }
        loading = true
        defer { loading = false }
        let page = (try? await client.albumList(type: sort.rawValue, size: pageSize, offset: albums.count,
                                                musicFolderId: model.selectedLibraryID)) ?? []
        albums += page
        if page.count < pageSize { finished = true }
    }
}

/// Порядок песен. Сервер отдаёт их постранично без сортировки, поэтому остальные порядки считаем на устройстве по всей медиатеке.
private enum SongSort: String, CaseIterable, Identifiable {
    case server, title, artist, album, year, rating, duration
    var id: String { rawValue }
    var title: LocalizedStringKey {
        switch self {
        case .server: "As on Server"; case .title: "Title"; case .artist: "Artist"; case .album: "Album"
        case .year: "Year"; case .rating: "Rating"; case .duration: "Duration"
        }
    }
    func sorted(_ list: [Song], rating: (Song) -> Int) -> [Song] {
        func cmp(_ a: String?, _ b: String?) -> Bool { (a ?? "").localizedStandardCompare(b ?? "") == .orderedAscending }
        switch self {
        case .server: return list
        case .title: return list.sorted { cmp($0.title, $1.title) }
        case .artist: return list.sorted { $0.artist == $1.artist ? cmp($0.title, $1.title) : cmp($0.artist, $1.artist) }
        case .album: return list.sorted { $0.album == $1.album ? ($0.track ?? 0) < ($1.track ?? 0) : cmp($0.album, $1.album) }
        case .year: return list.sorted { ($0.year ?? 0) > ($1.year ?? 0) }
        case .rating: return list.sorted { rating($0) > rating($1) }
        case .duration: return list.sorted { ($0.duration ?? 0) > ($1.duration ?? 0) }
        }
    }
}

private enum SongFilter: String, CaseIterable, Identifiable {
    case all, favorites, downloaded
    var id: String { rawValue }
    var title: LocalizedStringKey { switch self { case .all: "All Songs"; case .favorites: "Favorites"; case .downloaded: "Downloaded" } }
}

private struct SongsListView: View {
    @Environment(AppModel.self) private var model
    @AppStorage("songsSort") private var sortRaw = SongSort.server.rawValue
    @State private var filter: SongFilter = .all
    @State private var genre: String?
    @State private var songs: [Song] = []
    @State private var finished = false
    @State private var loading = false
    private var sort: SongSort { SongSort(rawValue: sortRaw) ?? .server }
    /// Для сортировки и фильтров нужна вся медиатека — подгружаем её целиком крупными страницами.
    private var needsAll: Bool { sort != .server || filter != .all || genre != nil }
    private var pageSize: Int { needsAll ? 500 : 100 }

    private var shown: [Song] {
        var list = songs
        if let genre { list = list.filter { $0.genre == genre } }
        switch filter {
        case .all: break
        case .favorites: list = list.filter { model.player?.isFavorite($0) ?? ($0.starred != nil) }
        case .downloaded: list = list.filter { model.isDownloaded($0) }
        }
        return sort.sorted(list) { model.player?.rating(of: $0) ?? $0.userRating ?? 0 }
    }

    private var genres: [String] { Array(Set(songs.compactMap(\.genre).filter { !$0.isEmpty })).sorted { $0.localizedStandardCompare($1) == .orderedAscending } }

    var body: some View {
        let list = shown
        List {
            ForEach(Array(list.enumerated()), id: \.element.id) { index, song in
                Button { model.player?.play(list, startAt: index) } label: {
                    MediaRow(coverID: song.coverArt, title: song.title, subtitle: song.displayArtist)
                }
                .buttonStyle(.plain)
                .songMenu(song)
                .onAppear { if !needsAll, index >= list.count - 10 { Task { await loadMore() } } }
            }
            if needsAll && !finished { ProgressView().frame(maxWidth: .infinity).listRowSeparator(.hidden) }
        }
        .listStyle(.plain)
        .overlay {
            if songs.isEmpty && !finished { ProgressView() }
            else if list.isEmpty && finished { ContentUnavailableView("No Songs", systemImage: "music.note") }
        }
        .navigationTitle("Songs")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                Menu {
                    Picker("Sort By", selection: $sortRaw) { ForEach(SongSort.allCases) { Text($0.title).tag($0.rawValue) } }
                    Picker("Show", selection: $filter) { ForEach(SongFilter.allCases) { Text($0.title).tag($0) } }
                    if !genres.isEmpty {
                        Picker("Genre", selection: $genre) {
                            Text("All Genres").tag(String?.none)
                            ForEach(genres, id: \.self) { Text($0).tag(String?.some($0)) }
                        }
                    }
                } label: {
                    Label("Sort", systemImage: needsAll ? "line.3.horizontal.decrease.circle.fill" : "line.3.horizontal.decrease.circle")
                }
            }
        }
        .task(id: model.contentKey) { songs = []; finished = false; await loadMore() }
        .task(id: needsAll) { while needsAll, !finished { await loadMore(); if loading { try? await Task.sleep(for: .milliseconds(100)) } } }
    }

    private func loadMore() async {
        guard !loading, !finished, let client = model.client else { return }
        loading = true
        defer { loading = false }
        let page = (try? await client.allSongs(offset: songs.count, count: pageSize, musicFolderId: model.selectedLibraryID)) ?? []
        songs += page
        if page.count < pageSize { finished = true }
    }
}

private struct FavoritesView: View {
    @Environment(AppModel.self) private var model
    @State private var results = SearchResults()
    @State private var loaded = false

    var body: some View {
        List {
            if !results.artists.isEmpty {
                Section("Artists") { ForEach(results.artists) { a in NavigationLink(value: a) { ArtistRow(artist: a) } } }
            }
            if !results.albums.isEmpty {
                Section("Albums") {
                    ForEach(results.albums) { a in NavigationLink(value: a) { MediaRow(coverID: a.coverArt, title: a.name, subtitle: a.artist) }.albumMenu(a) }
                }
            }
            if !results.songs.isEmpty {
                Section("Songs") {
                    ForEach(Array(results.songs.enumerated()), id: \.element.id) { i, s in
                        Button { model.player?.play(results.songs, startAt: i) } label: {
                            MediaRow(coverID: s.coverArt, title: s.title, subtitle: s.displayArtist)
                        }
                        .buttonStyle(.plain).songMenu(s)
                    }
                }
            }
        }
        .listStyle(.plain)
        .overlay {
            if !loaded { ProgressView() }
            else if results.isEmpty { ContentUnavailableView("No Favorites", systemImage: "heart", description: Text("Tap the heart in the player to add songs here.")) }
        }
        .navigationTitle("Favorites")
        .navigationBarTitleDisplayMode(.inline)
        .task(id: model.contentKey) {
            results = (try? await model.client?.starred(musicFolderId: model.selectedLibraryID)) ?? SearchResults()
            loaded = true
        }
    }
}
