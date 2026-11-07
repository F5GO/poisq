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
        let wanted = items.filter { $0.albumListType != nil }
        let needsStarred = items.contains(.favoriteSongs) || items.contains(.favoriteArtists)
        guard !wanted.isEmpty || needsStarred || items.contains(.playlists) else { albums = [:]; failed = false; return }
        isLoading = true
        defer { isLoading = false }
        // Остальные блоки (плейлисты, избранные песни и артисты) — своими запросами, ошибка одного не ломает главную.
        async let pl: [Playlist]? = items.contains(.playlists) ? try? await client.playlists() : nil
        async let st: SearchResults? = needsStarred ? try? await client.starred(musicFolderId: library) : nil
        let (gotPlaylists, gotStarred) = await (pl, st)
        playlists = gotPlaylists ?? []
        favoriteSongs = Array((gotStarred?.songs ?? []).prefix(6))
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

/// Заголовок — имя сервера. Справа — кнопка-аватар с меню; по нажатию на заголовок — выбор сервера и медиатеки.
struct HomeView: View {
    @Environment(AppModel.self) private var model
    @State private var home = HomeModel()
    @State private var editing = false
    @State private var path = NavigationPath()
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
                        SongsFeed()
                    } else if model.offline {
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
                .animation(.smooth, value: model.player?.isActive)
                .animation(.smooth, value: model.homeLayout.entries)
                .animation(.smooth, value: model.offline)
                .animation(.smooth, value: model.showBackOnline)
            }
            .overlay { if home.albums.isEmpty && home.isLoading && !songsMode { ProgressView() } }
            .refreshable { await reload() }
            .task(id: loadKey) { await reload() }
            .toolbarVisibility(.hidden, for: .navigationBar)
            // Панели навигации нет — системный эффект края прокрутки (iOS 26) мягко скрывает контент под статус-баром, без цветной плашки.
            .safeAreaBar(edge: .top, spacing: 0) { Color.clear.frame(height: 0) }
            .scrollEdgeEffectStyle(.soft, for: .top)
            .musicDestinations()
            .sheet(isPresented: $editing) { HomeEditView() }
            .onChange(of: model.pendingAlbum) { _, album in
                guard let album else { return }
                path.append(album)
                model.pendingAlbum = nil
            }
            .onChange(of: model.pendingArtist) { _, artist in
                guard let artist else { return }
                path.append(artist)
                model.pendingArtist = nil
            }
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
            HomeMenu(onEdit: { editing = true })
        }
        .padding(.horizontal, 20)
        .padding(.top, 4)
    }

    private func titleLabel(showsChevron: Bool) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack(spacing: 6) {
                Text(model.serverName).font(.largeTitle.bold()).lineLimit(1)
                if showsChevron {
                    Image(systemName: "chevron.down").font(.footnote.weight(.bold)).foregroundStyle(.secondary)
                }
            }
            if let library = model.libraryTitle {
                Text(library).font(.subheadline).foregroundStyle(.secondary)
            }
        }
        .contentShape(.rect)
    }

    @ViewBuilder private func block(_ item: HomeItem) -> some View {
        if item == .upNext {
            if let player = model.player, player.isActive { UpNextSection(player: player) }
        } else if item == .pinned {
            PinnedShelf(pins: model.pins.pins)
        } else if item == .playlists {
            if !home.playlists.isEmpty { PlaylistShelf(playlists: home.playlists) }
        } else if item == .favoriteSongs {
            if !home.favoriteSongs.isEmpty { SongRowsSection(title: item.title, songs: home.favoriteSongs) }
        } else if item == .favoriteArtists {
            if !home.favoriteArtists.isEmpty { ArtistShelf(artists: home.favoriteArtists) }
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
        if let player = model.player, player.isActive { UpNextSection(player: player) }
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
    }
}

/// Круглая кнопка с буквой сервера, как профиль в Apple Music: меню главной, серверов и медиатек.
private struct HomeMenu: View {
    @Environment(AppModel.self) private var model
    let onEdit: () -> Void

    var body: some View {
        Menu {
            @Bindable var m = model
            Picker("Show", selection: $m.downloadedOnly) {
                Text("All Songs").tag(false)
                Text("Downloaded").tag(true)
            }
            .pickerStyle(.inline)
            Section {
                Button("Customize Home", systemImage: "slider.horizontal.3", action: onEdit)
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

/// Закреплённые альбомы и плейлисты.
private struct PinnedShelf: View {
    let pins: [Pin]
    var body: some View {
        if !pins.isEmpty {
            VStack(alignment: .leading, spacing: 12) {
                Text("Pinned").font(.title2.bold()).padding(.horizontal, 20)
                ScrollView(.horizontal, showsIndicators: false) {
                    LazyHStack(alignment: .top, spacing: 14) {
                        ForEach(pins) { pin in
                            if let album = pin.album {
                                NavigationLink(value: album) { AlbumCard(album: album) }.buttonStyle(.plain).albumMenu(album)
                            } else if let playlist = pin.playlist {
                                NavigationLink(value: playlist) { PlaylistCard(playlist: playlist) }.buttonStyle(.plain).playlistPinMenu(playlist)
                            }
                        }
                    }
                    .scrollTargetLayout()
                    .padding(.horizontal, 20)
                }
                .scrollTargetBehavior(.viewAligned)
            }
        }
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
                Button { model.player?.play(songs, startAt: i) } label: {
                    HStack(spacing: 12) {
                        CoverArtView(id: song.coverArt, size: 48, radius: 8)
                        VStack(alignment: .leading, spacing: 2) {
                            Text(song.title).lineLimit(1)
                            Text(song.displayArtist ?? "").font(.subheadline).foregroundStyle(.secondary).lineLimit(1)
                        }
                        Spacer()
                    }
                    .padding(.horizontal, 20)
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .songMenu(song)
            }
        }
    }
}

/// Ближайшие треки из очереди, как «Up Next» в Apple Music.
private struct UpNextSection: View {
    let player: Player
    var body: some View {
        let upcoming = Array(player.queue.dropFirst().prefix(4))
        if !upcoming.isEmpty {
            VStack(alignment: .leading, spacing: 8) {
                Text("Up Next").font(.title2.bold()).padding(.horizontal, 20)
                ForEach(upcoming) { song in
                    HStack(spacing: 12) {
                        CoverArtView(id: song.coverArt, size: 48, radius: 8)
                        VStack(alignment: .leading, spacing: 2) {
                            Text(song.title).lineLimit(1)
                            Text(song.displayArtist ?? "").font(.subheadline).foregroundStyle(.secondary).lineLimit(1)
                        }
                        Spacer()
                    }
                    .padding(.horizontal, 20)
                }
            }
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
