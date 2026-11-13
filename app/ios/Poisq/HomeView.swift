import SwiftUI
import PoisqCore
import PoisqPlayback

@MainActor @Observable
private final class HomeModel {
    var albums: [HomeItem: [Album]] = [:]
    var playlists: [Playlist] = []
    var favoriteSongs: [Song] = []
    var favoriteArtists: [Artist] = []
    var isLoading = false
    var failed = false

    /// Базовая загрузка стандартным API: только видимые блоки, параллельно.
    /// (Расширение poisq позже заменит это одним запросом.)
    func load(client: SubsonicClient?, library: String?, items: [HomeItem]) async {
        guard let client else { return }
        // Недавние и случайные альбомы нужны быстрым действиям, даже если их полки скрыты.
        let requested = Set(items.filter { $0.albumListType != nil }).union([.recent, .random])
        let wanted = HomeItem.allCases.filter { requested.contains($0) }
        // Любимые треки всегда доступны как быстрое действие.
        let needsStarred = true
        guard !wanted.isEmpty || needsStarred || items.contains(.playlists) else { albums = [:]; failed = false; return }
        isLoading = true
        defer { isLoading = false }
        // Остальные блоки (плейлисты, избранные песни и артисты) — своими запросами, ошибка одного не ломает главную.
        async let pl: [Playlist]? = items.contains(.playlists) ? try? await client.playlists() : nil
        async let st: SearchResults? = needsStarred ? try? await client.starred(musicFolderId: library) : nil
        let (gotPlaylists, gotStarred) = await (pl, st)
        playlists = gotPlaylists ?? []
        favoriteSongs = gotStarred?.songs ?? []
        favoriteArtists = Array((gotStarred?.artists ?? []).prefix(20))
        guard !wanted.isEmpty else { albums = [:]; failed = false; return }
        var loaded: [HomeItem: [Album]] = [:]
        var errors = 0
        await withTaskGroup(of: (HomeItem, [Album]?).self) { group in
            for item in wanted {
                guard let type = item.albumListType else { continue }
                group.addTask { (item, try? await client.albumList(type: type, size: 20, musicFolderId: library)) }
            }
            for await (item, list) in group {
                if let list { loaded[item] = list } else { errors += 1 }
            }
        }
        failed = errors == wanted.count
        if !failed { albums = loaded }     // при частичной ошибке оставляем то, что загрузилось
    }
}

private enum HomeDestination: Hashable {
    case downloads, queue
}

/// Заголовок — имя сервера. Справа — кнопка-аватар с меню; по нажатию на заголовок — выбор сервера и медиатеки.
struct HomeView: View {
    @Environment(AppModel.self) private var model
    @State private var home = HomeModel()
    @State private var editing = false
    @State private var editingTabs = false
    @Binding var path: NavigationPath
    @AppStorage("homeSongsMode") private var songsMode = false

    /// Меняется при смене сервера, медиатеки или набора видимых блоков.
    private var loadKey: String { model.contentKey + "|\(model.offline)|" + model.homeLayout.visible.map(\.rawValue).joined(separator: ",") }

    var body: some View {
        NavigationStack(path: $path) {
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 28) {
                    header
                    statusBanner
                    if songsMode {
                        ForEach(modeControls) { item in block(item) }
                        SongsFeed()
                    } else if model.offline {
                        ForEach(modeControls) { item in block(item) }
                        offlineContent
                    } else {
                        ForEach(model.homeLayout.visible) { item in
                            block(item)
                        }
                        if model.homeLayout.visible.isEmpty { emptyLayout }
                    }
                    if home.failed && !model.offline && !songsMode {
                        ContentUnavailableView {
                            Label("Couldn't load", systemImage: "wifi.exclamationmark")
                        } description: {
                            Text("Check the connection to the server.")
                        } actions: {
                            Button("Retry") { Task { await reload() } }.buttonStyle(.glass)
                        }
                    }
                }
                .padding(.vertical, 8)
            }
            .overlay { if home.albums.isEmpty && home.isLoading && !songsMode { ProgressView() } }
            .refreshable { await reload() }
            .task(id: loadKey) { await reload() }
            .toolbarVisibility(.hidden, for: .navigationBar)
            // Панели навигации нет — системный эффект края прокрутки (iOS 26) мягко скрывает контент под статус-баром, без цветной плашки.
            .safeAreaBar(edge: .top, spacing: 0) { Color.clear.frame(height: 0) }
            .scrollEdgeEffectStyle(.soft, for: .top)
            .musicDestinations()
            .navigationDestination(for: HomeDestination.self) { destination in
                switch destination {
                case .downloads: DownloadedView()
                case .queue:
                    if let player = model.player { QueueView(player: player) }
                }
            }
            .sheet(isPresented: $editing) { HomeEditView() }
            .sheet(isPresented: $editingTabs) { TabBarEditView() }
        }
    }

    /// Название сервера слева, кнопка меню справа — в одной строке.
    private var header: some View {
        HStack(alignment: .center, spacing: 12) {
            Group {
                if model.hasSwitcher {
                    Menu { SwitcherMenu() } label: { titleLabel(showsChevron: true) }.buttonStyle(.plain)
                } else {
                    titleLabel(showsChevron: false)
                }
            }
            Spacer(minLength: 0)
            HomeMenu(onEdit: { editing = true }, onEditTabs: { editingTabs = true })
        }
        .padding(.horizontal, 20)
        .padding(.top, 4)
    }

    private func titleLabel(showsChevron: Bool) -> some View {
        HStack(spacing: 6) {
            Text(libraryName).font(.title2.weight(.semibold)).lineLimit(1)
            if showsChevron {
                Image(systemName: "chevron.down").font(.caption.weight(.semibold)).foregroundStyle(.secondary)
            }
        }
        .contentShape(.rect)
    }

    private var libraryName: String {
        if let id = model.selectedLibraryID,
           let selected = model.libraries.first(where: { $0.id == id }) {
            return normalizedLibraryName(model.name(of: selected))
        }
        if model.libraries.count == 1, let only = model.libraries.first {
            return normalizedLibraryName(model.name(of: only))
        }
        if model.libraries.count > 1 { return String(localized: "All Libraries") }
        return model.serverName
    }

    private func normalizedLibraryName(_ name: String) -> String {
        name.localizedCaseInsensitiveCompare("Music library") == .orderedSame ? model.serverName : name
    }

    @ViewBuilder private var quickActions: some View {
        let actions = model.homeLayout.visibleQuickActions
        if !actions.isEmpty {
            LazyVGrid(columns: Array(repeating: GridItem(.flexible(), spacing: 8), count: min(actions.count, 4)), spacing: 8) {
                ForEach(actions) { action in
                    quickAction(action)
                }
            }
            .padding(.horizontal, 20)
        }
    }

    @ViewBuilder private func quickAction(_ action: HomeQuickAction) -> some View {
        switch action {
        case .shuffleAll:
            QuickActionTile(title: action.title, systemImage: action.icon) { shuffleAll() }
        case .randomAlbum:
            QuickActionTile(title: action.title, systemImage: action.icon) { openRandomAlbum() }
                .disabled(randomAlbum == nil)
        case .favoriteSongs:
            QuickActionTile(title: action.title, systemImage: action.icon) { playFavorites() }
                .disabled(activeFavoriteSongs.isEmpty)
        case .downloads:
            QuickActionTile(title: action.title, systemImage: action.icon) {
                path.append(HomeDestination.downloads)
            }
        }
    }

    private var randomAlbum: Album? {
        if model.offline { return model.downloadedAlbums.randomElement() }
        return home.albums[.random]?.randomElement()
    }

    private var activeFavoriteSongs: [Song] {
        guard let player = model.player else { return home.favoriteSongs }
        return home.favoriteSongs.filter(player.isFavorite)
    }

    private var modeControls: [HomeItem] {
        model.homeLayout.visible.filter { [.quickActions, .pinned, .upNext].contains($0) }
    }

    private func shuffleAll() {
        Task {
            let songs: [Song]
            if model.offline {
                songs = Array(model.downloadedSongs.shuffled().prefix(50))
            } else {
                songs = (try? await model.client?.randomSongs(count: 50, musicFolderId: model.selectedLibraryID)) ?? []
            }
            guard !songs.isEmpty else { return }
            model.player?.play(songs, shuffled: true)
        }
    }

    private func openRandomAlbum() {
        guard let album = randomAlbum else { return }
        path.append(album)
    }

    private func playFavorites() {
        let songs = activeFavoriteSongs
        guard !songs.isEmpty else { return }
        model.player?.play(songs, startAt: songs.indices.randomElement() ?? 0, shuffled: true)
    }

    @ViewBuilder private func block(_ item: HomeItem) -> some View {
        if item == .quickActions {
            quickActions
        } else if item == .pinned {
            QuickAccessGrid(pins: model.pins.pins, recent: home.albums[.recent] ?? [])
        } else if item == .upNext {
            if let player = model.player, player.current != nil {
                ResumeQueueCard(player: player) { path.append(HomeDestination.queue) }
            }
        } else if item == .playlists {
            if !home.playlists.isEmpty { PlaylistShelf(playlists: home.playlists) }
        } else if item == .favoriteSongs {
            if !activeFavoriteSongs.isEmpty { SongRowsSection(title: item.title, songs: Array(activeFavoriteSongs.prefix(6))) }
        } else if item == .favoriteArtists {
            if !home.favoriteArtists.isEmpty { ArtistShelf(artists: home.favoriteArtists) }
        } else if item == .recent {
            if let list = home.albums[.recent], !list.isEmpty {
                RecentAlbumRows(albums: list)
            }
        } else if let list = home.albums[item], !list.isEmpty {
            AlbumShelf(title: item.title, albums: list)
        }
    }

    private var emptyLayout: some View {
        ContentUnavailableView {
            Label("Nothing to show", systemImage: "eye.slash")
        } description: {
            Text("All sections on Home are hidden.")
        } actions: {
            Button("Customize Home") { editing = true }.buttonStyle(.glass)
        }
    }

    /// Подсказка о состоянии связи: офлайн включился сам / связь вернулась.
    @ViewBuilder private var statusBanner: some View {
        if model.isAutoOffline {
            Label(model.network.isConnected ? "Server unavailable — showing downloads" : "No connection — showing downloads", systemImage: "wifi.slash")
                .font(.footnote.weight(.medium)).foregroundStyle(.secondary)
                .padding(.horizontal, 14).padding(.vertical, 8)
                .background(.thinMaterial, in: .capsule)
                .padding(.horizontal, 20).transition(.opacity.combined(with: .move(edge: .top)))
        } else if model.showBackOnline {
            Label("Back online", systemImage: "wifi").font(.footnote.weight(.medium)).foregroundStyle(.tint)
                .padding(.horizontal, 14).padding(.vertical, 8)
                .background(.thinMaterial, in: .capsule)
                .padding(.horizontal, 20).transition(.opacity.combined(with: .move(edge: .top)))
        }
    }

    /// Режим «только загруженные»: то, что лежит на устройстве, без обращений к серверу.
    @ViewBuilder private var offlineContent: some View {
        let albums = model.downloadedAlbums
        if albums.isEmpty {
            ContentUnavailableView("No Downloads", systemImage: "arrow.down.circle",
                                   description: Text("Songs you download will appear here."))
        } else {
            AlbumShelf(title: "Downloaded", albums: albums)
        }
    }

    private func reload() async {
        guard !model.offline else { return }
        await home.load(client: model.client, library: model.selectedLibraryID, items: model.homeLayout.visible)
        model.player?.synchronizeFavorites(home.favoriteSongs)
    }
}

/// Круглая кнопка с буквой сервера, как профиль в Apple Music: меню главной, серверов и медиатек.
private struct HomeMenu: View {
    @Environment(AppModel.self) private var model
    let onEdit: () -> Void
    let onEditTabs: () -> Void

    var body: some View {
        Menu {
            Section {
                Button("Customize Home", systemImage: "slider.horizontal.3", action: onEdit)
                Button("Customize Tab Bar", systemImage: "rectangle.bottomthird.inset.filled", action: onEditTabs)
            }
            if model.hasSwitcher { SwitcherMenu() }
        } label: {
            ServerAvatar(name: model.serverName)
        }
        .buttonStyle(.plain)
        .accessibilityLabel("Home Options")
    }
}

struct ServerAvatar: View {
    let name: String
    private static let blue = Color(red: 5/255, green: 147/255, blue: 198/255)
    private static let pink = Color(red: 196/255, green: 43/255, blue: 135/255)

    var body: some View {
        Text(String(name.first.map { String($0) } ?? "P").uppercased())
            .font(.system(size: 17, weight: .bold, design: .rounded))
            .foregroundStyle(.white)
            .frame(width: 36, height: 36)
            .background(LinearGradient(colors: [Self.blue, Self.pink], startPoint: .topLeading, endPoint: .bottomTrailing), in: .circle)
    }
}

/// Компактное системное действие в верхней части главной.
private struct QuickActionTile: View {
    let title: LocalizedStringKey
    let systemImage: String
    let action: () -> Void

    var body: some View {
        VStack(spacing: 7) {
            Button(action: action) {
                Image(systemName: systemImage)
                    .font(.system(size: 19, weight: .semibold))
                    .frame(width: 52, height: 52)
                    .contentShape(.rect)
            }
            .buttonStyle(.glass)
            .buttonBorderShape(.roundedRectangle(radius: 16))
            .accessibilityLabel(title)
            Text(title)
                .font(.caption2)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                .lineLimit(2)
                .minimumScaleFactor(0.8)
                .frame(maxWidth: .infinity, minHeight: 24, alignment: .top)
        }
    }
}

/// Горизонтальная полка с альбомами.
private struct AlbumShelf: View {
    let title: LocalizedStringKey
    let albums: [Album]

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(title).font(.title2.bold()).padding(.horizontal, 20)
            ScrollView(.horizontal, showsIndicators: false) {
                LazyHStack(alignment: .top, spacing: 14) {
                    ForEach(albums) { album in
                        NavigationLink(value: album) { AlbumCard(album: album) }.buttonStyle(.plain).albumMenu(album)
                    }
                }
                .scrollTargetLayout()
                .padding(.horizontal, 20)
            }
            .scrollTargetBehavior(.viewAligned)
        }
    }
}

/// Недавние альбомы — компактными строками, чтобы главная не состояла только из квадратных полок.
private struct RecentAlbumRows: View {
    @Environment(AppModel.self) private var model
    let albums: [Album]
    @State private var loadingAlbumID: String?
    private var shown: [Album] { Array(albums.prefix(model.homeLayout.recentlyPlayedLimit)) }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Recently Played").font(.title2.bold()).padding(.horizontal, 20)
            VStack(spacing: 0) {
                ForEach(Array(shown.enumerated()), id: \.element.id) { index, album in
                    HStack(spacing: 12) {
                        NavigationLink(value: album) {
                            HStack(spacing: 12) {
                                CoverArtView(id: album.coverArt, size: 58, radius: 10)
                                VStack(alignment: .leading, spacing: 3) {
                                    Text(album.name).font(.subheadline.weight(.semibold)).lineLimit(1)
                                    Text(albumSubtitle(album)).font(.caption).foregroundStyle(.secondary).lineLimit(1)
                                }
                                Spacer(minLength: 0)
                            }
                            .contentShape(.rect)
                        }
                        .buttonStyle(.plain)
                        .albumMenu(album)

                        Button { play(album) } label: {
                            if loadingAlbumID == album.id {
                                ProgressView().controlSize(.small)
                            } else {
                                Image(systemName: "play.fill").font(.subheadline.weight(.semibold))
                            }
                        }
                        .buttonStyle(.glass)
                        .buttonBorderShape(.circle)
                        .frame(width: 40, height: 40)
                        .disabled(loadingAlbumID != nil)
                        .accessibilityLabel("Play")
                    }
                    .padding(.horizontal, 12)
                    .padding(.vertical, 8)
                    if index < shown.count - 1 {
                        Divider().padding(.leading, 82)
                    }
                }
            }
            .background(.regularMaterial, in: .rect(cornerRadius: 18))
            .padding(.horizontal, 20)
        }
    }

    private func albumSubtitle(_ album: Album) -> String {
        let count = album.songCount.map { String(localized: "\($0) songs") }
        return [album.artist, count].compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: " · ")
    }

    private func play(_ album: Album) {
        guard loadingAlbumID == nil else { return }
        loadingAlbumID = album.id
        Task {
            let local = model.downloadedSongs.filter { $0.albumId == album.id }.sorted { ($0.track ?? 0) < ($1.track ?? 0) }
            let songs = model.offline ? local : ((try? await model.client?.album(id: album.id).songs) ?? local)
            if !songs.isEmpty { model.player?.play(songs) }
            loadingAlbumID = nil
        }
    }
}

struct AlbumCard: View {
    let album: Album
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            CoverArtView(id: album.coverArt, size: 160, radius: 12)
            Text(album.name).font(.subheadline).lineLimit(1)
            Text(album.artist ?? "").font(.caption).foregroundStyle(.secondary).lineLimit(1)
        }
        .frame(width: 160, alignment: .leading)
        .contentShape(.rect)
    }
}

/// Закреплённое дополняется недавно открытыми альбомами до четырёх элементов.
private struct QuickAccessGrid: View {
    @Environment(AppModel.self) private var model
    let pins: [Pin]
    let recent: [Album]

    private var limit: Int { model.homeLayout.quickAccessLimit }
    private var visiblePins: [Pin] {
        model.homeLayout.quickAccessShowsPins ? Array(pins.prefix(limit)) : []
    }
    private var recentFill: [Album] {
        guard model.homeLayout.quickAccessShowsRecent else { return [] }
        let pinnedAlbums = Set(visiblePins.compactMap { $0.album?.id })
        let count = max(0, limit - visiblePins.count)
        return Array(recent.lazy.filter { !pinnedAlbums.contains($0.id) }.prefix(count))
    }

    var body: some View {
        if !visiblePins.isEmpty || !recentFill.isEmpty {
            VStack(alignment: .leading, spacing: 12) {
                Text("Quick Access").font(.title2.bold()).padding(.horizontal, 20)
                LazyVGrid(columns: [GridItem(.flexible(), spacing: 10), GridItem(.flexible())], spacing: 10) {
                    ForEach(visiblePins) { pin in
                        if let album = pin.album {
                            NavigationLink(value: album) {
                                QuickAccessCard(coverID: album.coverArt, title: album.name, subtitle: album.artist)
                            }
                            .buttonStyle(.plain)
                            .albumMenu(album)
                        } else if let playlist = pin.playlist {
                            NavigationLink(value: playlist) {
                                QuickAccessCard(coverID: playlist.coverArt, title: playlist.name, subtitle: String(localized: "Playlist"))
                            }
                            .buttonStyle(.plain)
                            .playlistPinMenu(playlist)
                        }
                    }
                    ForEach(recentFill) { album in
                        NavigationLink(value: album) {
                            QuickAccessCard(coverID: album.coverArt, title: album.name, subtitle: album.artist)
                        }
                        .buttonStyle(.plain)
                        .albumMenu(album)
                    }
                }
                .padding(.horizontal, 20)
            }
        }
    }
}

private struct QuickAccessCard: View {
    let coverID: String?
    let title: String
    let subtitle: String?

    var body: some View {
        HStack(spacing: 10) {
            CoverArtView(id: coverID, size: 50, radius: 9)
            VStack(alignment: .leading, spacing: 3) {
                Text(title).font(.subheadline.weight(.semibold)).lineLimit(2)
                if let subtitle, !subtitle.isEmpty {
                    Text(subtitle).font(.caption2).foregroundStyle(.secondary).lineLimit(1)
                }
            }
            Spacer(minLength: 0)
        }
        .padding(7)
        .frame(maxWidth: .infinity, minHeight: 64, alignment: .leading)
        .background(.regularMaterial, in: .rect(cornerRadius: 16))
        .contentShape(.rect)
    }
}

struct PlaylistCard: View {
    let playlist: Playlist
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            CoverArtView(id: playlist.coverArt, size: 160, radius: 12)
            Text(playlist.name).font(.subheadline).lineLimit(1)
            Text(playlist.songCount.map { String(localized: "\($0) songs") } ?? "").font(.caption).foregroundStyle(.secondary).lineLimit(1)
        }
        .frame(width: 160, alignment: .leading)
        .contentShape(.rect)
    }
}

private struct PlaylistShelf: View {
    let playlists: [Playlist]
    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Playlists").font(.title2.bold()).padding(.horizontal, 20)
            ScrollView(.horizontal, showsIndicators: false) {
                LazyHStack(alignment: .top, spacing: 14) {
                    ForEach(playlists) { p in
                        NavigationLink(value: p) { PlaylistCard(playlist: p) }.buttonStyle(.plain).playlistPinMenu(p)
                    }
                }
                .scrollTargetLayout()
                .padding(.horizontal, 20)
            }
            .scrollTargetBehavior(.viewAligned)
        }
    }
}

private struct ArtistShelf: View {
    let artists: [Artist]
    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Favorite Artists").font(.title2.bold()).padding(.horizontal, 20)
            ScrollView(.horizontal, showsIndicators: false) {
                LazyHStack(alignment: .top, spacing: 14) {
                    ForEach(artists) { a in
                        NavigationLink(value: a) {
                            VStack(spacing: 6) {
                                CoverArtView(id: a.coverArt, size: 110, radius: 55)
                                Text(a.name).font(.subheadline).lineLimit(1)
                            }
                            .frame(width: 110)
                        }
                        .buttonStyle(.plain)
                    }
                }
                .padding(.horizontal, 20)
            }
        }
    }
}

/// Несколько песен списком: нажатие запускает воспроизведение отсюда.
private struct SongRowsSection: View {
    let title: LocalizedStringKey
    let songs: [Song]
    @Environment(AppModel.self) private var model
    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(title).font(.title2.bold()).padding(.horizontal, 20)
            ForEach(Array(songs.enumerated()), id: \.element.id) { i, song in
                PlayableSongRow(song: song) { model.player?.play(songs, startAt: i) }
                .padding(.horizontal, 20)
                .songMenu(song)
            }
        }
    }
}

/// Текущая сессия: системная карточка продолжения вместо дублирования нескольких строк очереди.
private struct ResumeQueueCard: View {
    let player: Player
    let openQueue: () -> Void

    var body: some View {
        if let song = player.current {
            ZStack {
                Button(action: openQueue) {
                    Color.clear
                        .frame(maxWidth: .infinity, minHeight: 76)
                        .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Queue")

                HStack(spacing: 13) {
                    HStack(spacing: 13) {
                        CoverArtView(id: song.coverArt, size: 76, radius: 14)
                        VStack(alignment: .leading, spacing: 3) {
                            Text(player.isPlaying ? String(localized: "Now Playing") : String(localized: "Continue"))
                                .font(.caption2.weight(.bold))
                                .foregroundStyle(.tint)
                                .textCase(.uppercase)
                            Text(song.title).font(.headline).lineLimit(1)
                            if player.upcoming.isEmpty {
                                Text(song.displayArtist ?? "").font(.caption).foregroundStyle(.secondary).lineLimit(1)
                            } else {
                                Text("\(player.upcoming.count) songs").font(.caption).foregroundStyle(.secondary).lineLimit(1)
                            }
                        }
                        Spacer(minLength: 0)
                    }
                    .allowsHitTesting(false)

                    Button { player.togglePlayPause() } label: {
                        Image(systemName: player.isPlaying ? "pause.fill" : "play.fill")
                            .font(.title3.weight(.semibold))
                            .contentTransition(.symbolEffect(.replace))
                            .frame(width: 44, height: 44)
                    }
                    .buttonStyle(.glassProminent)
                    .buttonBorderShape(.circle)
                    .accessibilityLabel(player.isPlaying ? String(localized: "Pause") : String(localized: "Play"))
                }
            }
            .padding(12)
            .background(.regularMaterial, in: .rect(cornerRadius: 22))
            .padding(.horizontal, 20)
            .sensoryFeedback(.impact(weight: .light), trigger: player.isPlaying)
        }
    }
}

/// Содержимое меню заголовка: серверы, затем медиатеки текущего сервера.
struct SwitcherMenu: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        if model.servers.count > 1 { ServerPicker() }
        if model.libraries.count > 1 { LibraryPicker() }
    }
}

struct ServerPicker: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        Picker("Server", selection: Binding(get: { model.activeID ?? UUID() },
                                            set: { model.switchServer(to: $0) })) {
            ForEach(model.servers) { Text($0.name).tag($0.id) }
        }
        .pickerStyle(.inline)
    }
}

struct LibraryPicker: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        Picker("Library", selection: Binding(get: { model.selectedLibraryID ?? "" },
                                             set: { model.select($0.isEmpty ? nil : $0) })) {
            Label("All Libraries", systemImage: "square.stack.3d.up").tag("")
            ForEach(model.libraries) { Text(model.name(of: $0)).tag($0.id) }
        }
        .pickerStyle(.inline)
    }
}
