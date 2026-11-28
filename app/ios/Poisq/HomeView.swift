import SwiftUI
import PoisqCore
import PoisqPlayback

/// Последнее содержимое главной на устройстве: при запуске (в том числе на медленной сети) экран сразу
/// заполнен прежними полками, а ответы сервера заменяют их по мере прихода. Файл свой для сервера и медиатеки.
struct HomeSnapshot: Codable {
    var albums: [String: [Album]] = [:]
    var playlists: [Playlist] = []
    var favoriteSongs: [Song] = []
    var favoriteArtists: [Artist] = []
    var quickPicks: [Song] = []
    var newSongs: [Song] = []
    var stationLists: [String: [Song]] = [:]
    var builtAt = Date.distantPast

    private static let dir: URL = {
        let d = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0].appending(path: "home", directoryHint: .isDirectory)
        try? FileManager.default.createDirectory(at: d, withIntermediateDirectories: true)
        return d
    }()

    /// Ключ содержимого — «сервер|медиатека»; в имени файла остаются только безопасные символы.
    private static func file(_ key: String) -> URL {
        let safe = key.addingPercentEncoding(withAllowedCharacters: .alphanumerics.union(CharacterSet(charactersIn: "-"))) ?? ""
        return dir.appending(path: "\(safe).json")
    }

    static func load(key: String) -> HomeSnapshot? {
        (try? Data(contentsOf: file(key))).flatMap { try? JSONDecoder().decode(HomeSnapshot.self, from: $0) }
    }

    func save(key: String) {
        guard let data = try? JSONEncoder().encode(self) else { return }
        let url = Self.file(key)
        Task.detached(priority: .utility) { try? data.write(to: url, options: .atomic) }
    }

    /// Все медиатеки удалённого сервера.
    static func delete(serverID: UUID) {
        let prefix = serverID.uuidString
        for url in (try? FileManager.default.contentsOfDirectory(at: dir, includingPropertiesForKeys: nil)) ?? []
        where url.lastPathComponent.hasPrefix(prefix) {
            try? FileManager.default.removeItem(at: url)
        }
    }
}

@MainActor @Observable
private final class HomeModel {
    var albums: [HomeItem: [Album]] = [:]
    var playlists: [Playlist] = []
    var favoriteSongs: [Song] = []
    var favoriteArtists: [Artist] = []
    /// «Быстрый выбор»: похожие на недавнее, любимые и случайные треки.
    var quickPicks: [Song] = []
    /// Заранее собранные первые порции станций: обложка карточки — первый трек, запуск — без ожидания сети.
    var stationLists: [String: [Song]] = [:]
    /// «Новое в медиатеке»: треки недавно добавленных альбомов вперемешку. Тот же список играет станция «Новое».
    var newSongs: [Song] = []
    var isLoading = false
    var failed = false
    /// Первая загрузка завершилась: до неё на месте «Быстрого выбора» заглушка, чтобы главная не прыгала.
    var loadedOnce = false
    /// «Быстрый выбор» и станции не пересобираются при каждом появлении главной, иначе список меняется под пальцем.
    private var builtKey = ""
    private var builtAt = Date.distantPast
    private var preparing: Set<String> = []
    /// Станции, чей список пора обновить. Старый список (и обложка) остаётся на карточке, пока не придёт новый.
    private var refreshNeeded: Set<String> = []
    /// Меняется при смене сервера или медиатеки: ответы для прежней медиатеки отбрасываются.
    private var generation = 0
    private static let rebuildInterval: TimeInterval = 3 * 3600
    /// Медиатека, чьё содержимое сейчас на экране (из снимка или с сервера).
    private var shownKey = ""

    /// Показывает сохранённую главную медиатеки key; без снимка — пустой экран с заглушками, а не чужая медиатека.
    private func restore(key: String) {
        guard key != shownKey else { return }
        shownKey = key
        generation += 1
        let snap = HomeSnapshot.load(key: key)
        albums = Dictionary(uniqueKeysWithValues: (snap?.albums ?? [:]).compactMap { k, v in HomeItem(rawValue: k).map { ($0, v) } })
        playlists = snap?.playlists ?? []
        favoriteSongs = snap?.favoriteSongs ?? []
        favoriteArtists = snap?.favoriteArtists ?? []
        quickPicks = snap?.quickPicks ?? []
        newSongs = snap?.newSongs ?? []
        stationLists = snap?.stationLists ?? [:]
        refreshNeeded = []
        failed = false
        loadedOnce = snap != nil
        // Быстрый выбор и станции снимка живут по тем же правилам, что и собранные в этом запуске.
        builtKey = snap == nil ? "" : key
        builtAt = snap?.builtAt ?? .distantPast
    }

    private func persist() {
        guard !shownKey.isEmpty else { return }
        HomeSnapshot(albums: Dictionary(uniqueKeysWithValues: albums.map { ($0.key.rawValue, $0.value) }),
                     playlists: playlists, favoriteSongs: favoriteSongs, favoriteArtists: favoriteArtists,
                     quickPicks: quickPicks, newSongs: newSongs, stationLists: stationLists, builtAt: builtAt)
            .save(key: shownKey)
    }

    private static func similar(to seed: Song?, client: SubsonicClient) async -> [Song] {
        guard let seed else { return [] }
        return (try? await client.similarSongs(id: seed.id, count: 12)) ?? []
    }

    /// Базовая загрузка стандартным API: только видимые блоки, параллельно.
    /// (Расширение poisq позже заменит это одним запросом.)
    func load(client: SubsonicClient?, library: String?, items: [HomeItem], seed: Song?, key: String, force: Bool) async {
        restore(key: key)
        guard let client else { return }
        // Недавние и случайные альбомы нужны быстрым действиям, даже если их полки скрыты.
        let requested = Set(items.filter { $0.albumListType != nil }).union([.recent, .random])
        let wanted = HomeItem.allCases.filter { requested.contains($0) }
        // Любимые треки всегда доступны как быстрое действие.
        let needsStarred = true
        guard !wanted.isEmpty || needsStarred || items.contains(.playlists) else { albums = [:]; failed = false; return }
        isLoading = true
        let gen = generation
        defer {
            isLoading = false
            loadedOnce = true
            if gen == generation, !failed { persist() }
        }
        // Остальные блоки (плейлисты, избранные песни и артисты) — своими запросами, ошибка одного не ломает главную.
        async let pl: [Playlist]? = items.contains(.playlists) ? try? await client.playlists() : nil
        async let st: SearchResults? = needsStarred ? try? await client.starred(musicFolderId: library) : nil
        // Пересобираем по запросу (потянули вниз), при смене сервера или медиатеки и раз в несколько часов.
        let fresh = force || key != builtKey || Date().timeIntervalSince(builtAt) > Self.rebuildInterval
        let wantsPicks = fresh && items.contains(.quickPicks)
        async let rnd: [Song]? = wantsPicks ? try? await client.randomSongs(count: 20, musicFolderId: library) : nil
        let wantsNew = fresh && items.contains(.newSongs)
        async let fetchedNew: [Song] = wantsNew ? HomeStarter.prepare(.newest, client: client, folder: library, favorites: []) : []
        let (gotPlaylists, gotStarred) = await (pl, st)
        guard gen == generation else { return }       // пока ждали, сменилась медиатека
        // Нет ответа — остаётся то, что уже на экране (в том числе из снимка).
        if let gotPlaylists { playlists = gotPlaylists } else if !items.contains(.playlists) { playlists = [] }
        if let gotStarred {
            favoriteSongs = gotStarred.songs
            favoriteArtists = Array(gotStarred.artists.prefix(20))
        }
        // Похожие — на то, что играет, иначе на случайный любимый; идут параллельно с полками альбомов.
        let pickSeed = wantsPicks ? (seed ?? favoriteSongs.randomElement()) : nil
        async let sim = Self.similar(to: pickSeed, client: client)
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
        guard gen == generation else { return }
        failed = errors == wanted.count
        // Полка без ответа остаётся прежней, а не исчезает.
        if !failed { albums = loaded.merging(albums.filter { requested.contains($0.key) }) { new, _ in new } }
        if wantsPicks {
            let picks = HomeMix.quickPicks(similar: await sim, favorites: favoriteSongs.shuffled(), random: await rnd ?? [])
            if !picks.isEmpty { quickPicks = picks }   // сервер не ответил — оставляем прежний список
        }
        if fresh && gotStarred != nil {
            if key != builtKey, !wantsPicks { quickPicks = [] }   // чужие станции и треки уже убрал restore
            refreshNeeded = Set(stationLists.keys)  // вместе с быстрым выбором обновляются и станции
            if wantsNew {
                let fetched = await fetchedNew
                if !fetched.isEmpty || key != builtKey { newSongs = fetched }
                if !newSongs.isEmpty {
                    stationLists[HomeStation.newest.id] = newSongs
                    refreshNeeded.remove(HomeStation.newest.id)
                }
            }
            builtKey = key
            builtAt = Date()
        }
    }

    /// Готовит недостающие и устаревшие станции параллельно; каждая появляется, как только готова.
    /// Работает в своей задаче, а не в задаче экрана: смена ключа `.task` или уход с главной не обрывает подготовку
    /// на полпути (раньше оборванная станция так и оставалась без обложки).
    func prepareStations(_ stations: [HomeStation], client: SubsonicClient?, folder: String?, favorites: [Song],
                         warmCover: @escaping @MainActor @Sendable (String) async -> Void) {
        guard let client else { return }
        let ids = Set(stations.map(\.id))
        stationLists = stationLists.filter { ids.contains($0.key) }      // сменился текущий трек — «Похоже на…» другая
        refreshNeeded.formIntersection(ids)
        let todo = stations.filter { (stationLists[$0.id] == nil || refreshNeeded.contains($0.id)) && !preparing.contains($0.id) }
        guard !todo.isEmpty else { return }
        preparing.formUnion(todo.map(\.id))
        let gen = generation
        Task { [weak self] in
            await withTaskGroup(of: (String, [Song]).self) { group in
                for station in todo {
                    group.addTask { (station.id, await HomeStarter.prepare(station, client: client, folder: folder, favorites: favorites)) }
                }
                for await (id, songs) in group {
                    guard let self else { continue }
                    self.preparing.remove(id)
                    // Не вышло — остаётся прежний список, попробуем при следующем показе главной.
                    guard gen == self.generation, !songs.isEmpty else { continue }
                    self.stationLists[id] = songs
                    self.refreshNeeded.remove(id)
                    // Обложку грузим сразу, не дожидаясь, пока карточка появится на экране.
                    if let cover = songs.first?.coverArt { Task { await warmCover(cover) } }
                }
                if let self, gen == self.generation { self.persist() }
            }
        }
    }

    /// Станция запущена — её список израсходован, в следующий раз будет новый.
    func consume(_ station: HomeStation) { refreshNeeded.insert(station.id) }
}

private enum HomeDestination: Hashable {
    case downloads, queue
    /// Трековый блок главной целиком.
    case tracks(HomeItem)
}

/// Заголовок — имя сервера. Справа — кнопка-аватар с меню; по нажатию на заголовок — выбор сервера и медиатеки.
struct HomeView: View {
    @Environment(AppModel.self) private var model
    @State private var home = HomeModel()
    @State private var editing = false
    @State private var editingTabs = false
    @Binding var path: NavigationPath
    @AppStorage("homeSongsMode") private var songsMode = false
    @State private var chipBusy: HomeQuickAction?

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
                    if home.failed && home.albums.isEmpty && !model.offline && !songsMode {
                        ContentUnavailableView {
                            Label("Couldn't load", systemImage: "wifi.exclamationmark")
                        } description: {
                            Text("Check the connection to the server.")
                        } actions: {
                            Button("Retry") { Task { await reload(force: true) } }.buttonStyle(.glass)
                        }
                    }
                }
                .padding(.vertical, 8)
            }
            .overlay { if home.albums.isEmpty && home.isLoading && !songsMode { ProgressView() } }
            .refreshable { await reload(force: true) }
            .task(id: loadKey) { await reload() }
            // Новый текущий трек или жанры после загрузки — дособираем станции.
            .task(id: stations.map(\.id).joined(separator: "|")) { prepareStations() }
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
                case .tracks(let item): TrackList(title: item.title, songs: songs(for: item))
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
            if model.secureVerified {
                // Горит только после реальной проверки: ответ через канал расшифрован ключами сессии.
                Circle().fill(.green).frame(width: 8, height: 8)
                    .accessibilityLabel(Text("Connection is secure"))
            }
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
        // Пустые источники не показываем; как только появится содержимое — чип встанет на своё место.
        let actions = model.homeLayout.visibleQuickActions.filter(isAvailable)
        if !actions.isEmpty {
            SourceChips(actions: actions,
                        emphasizeFirst: model.player?.current == nil && actions.first == .flow,
                        busy: chipBusy,
                        isEnabled: isEnabled,
                        run: run)
        }
    }

    private func isAvailable(_ action: HomeQuickAction) -> Bool {
        switch action {
        case .favoriteSongs: !activeFavoriteSongs.isEmpty
        case .downloads: !model.downloadedSongs.isEmpty
        case .randomAlbum: randomAlbum != nil
        case .flow, .newest, .shuffleAll: true
        }
    }

    private func isEnabled(_ action: HomeQuickAction) -> Bool {
        switch action {
        case .flow, .newest: !model.offline
        default: true
        }
    }

    private func run(_ action: HomeQuickAction) {
        switch action {
        case .flow: startStation(.myFlow, from: action)
        case .newest: startStation(.newest, from: action)
        case .shuffleAll: shuffleAll()
        case .downloads: playDownloads()
        case .favoriteSongs: playFavorites()
        case .randomAlbum: openRandomAlbum()
        }
    }

    private func startStation(_ station: HomeStation, from chip: HomeQuickAction) {
        chipBusy = chip
        Task {
            let started = await HomeStarter.start(station, app: model, favorites: activeFavoriteSongs,
                                                  prepared: home.stationLists[station.id])
            chipBusy = nil
            if started { stationStarted(station) }
        }
    }

    private func stationStarted(_ station: HomeStation) {
        home.consume(station)
        prepareStations()
    }

    private func prepareStations() {
        guard !model.offline, model.homeLayout.visible.contains(where: { $0 == .stations || $0 == .quickActions }) else { return }
        home.prepareStations(stations, client: model.client, folder: model.selectedLibraryID, favorites: activeFavoriteSongs) { [model] id in
            _ = await model.coverImage(id, pixels: 500)              // тот же размер, что у CoverArtView на 150 pt
        }
    }

    /// Загруженное вперемешку; пока ничего не загружено — открываем экран загрузок.
    private func playDownloads() {
        let songs = model.downloadedSongs
        guard !songs.isEmpty else { path.append(HomeDestination.downloads); return }
        model.player?.play(Array(songs.shuffled().prefix(200)))
    }

    private var randomAlbum: Album? {
        if model.offline { return model.downloadedAlbums.randomElement() }
        return home.albums[.random]?.randomElement()
    }

    private var activeFavoriteSongs: [Song] {
        guard let player = model.player else { return home.favoriteSongs }
        return home.favoriteSongs.filter(player.isFavorite)
    }

    /// Мой поток, похожее на текущий трек, любимые жанры и новое в медиатеке.
    private var stations: [HomeStation] {
        var list: [HomeStation] = [.myFlow]
        if let player = model.player, !player.isRadio, let current = player.current { list.append(.similar(current)) }
        list += HomeMix.topGenres(home.favoriteSongs + home.quickPicks).map(HomeStation.genre)
        list.append(.newest)
        return list
    }

    private static let placeholderSongs = (0..<6).map { Song(id: "placeholder-\($0)", title: "Song title here", artist: "Artist name") }

    private func songs(for item: HomeItem) -> [Song] {
        switch item {
        case .quickPicks: home.quickPicks
        case .recentSongs: model.history.log.recent(limit: 50)
        case .newSongs: home.newSongs
        case .rediscover:
            model.history.log.notRecently(from: activeFavoriteSongs, olderThan: 30 * 86_400, now: .now, limit: 50)
        default: []
        }
    }

    private func subtitle(for item: HomeItem) -> LocalizedStringKey? {
        switch item {
        case .newSongs: "From recently added albums"
        case .rediscover: "Favorites you haven't played in a month"
        default: nil
        }
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
        } else if item == .quickPicks {
            if !home.quickPicks.isEmpty {
                TrackPagesSection(title: item.title, subtitle: "Similar to what you play", songs: home.quickPicks) {
                    path.append(HomeDestination.tracks(item))
                }
            } else if !home.loadedOnce {
                // Держим место под блок, пока он грузится: остальное не сдвигается, когда треки приходят.
                TrackPagesSection(title: item.title, subtitle: "Similar to what you play", songs: Self.placeholderSongs)
                    .redacted(reason: .placeholder)
                    .allowsHitTesting(false)
            }
        } else if item == .recentSongs || item == .newSongs || item == .rediscover {
            let songs = songs(for: item)
            if !songs.isEmpty {
                TrackPagesSection(title: item.title, subtitle: subtitle(for: item), songs: Array(songs.prefix(12))) {
                    path.append(HomeDestination.tracks(item))
                }
            }
        } else if item == .stations {
            StationShelf(stations: stations, favorites: activeFavoriteSongs, prepared: home.stationLists,
                         onStarted: stationStarted)
        } else if item == .playlists {
            if !home.playlists.isEmpty { PlaylistShelf(playlists: home.playlists) }
        } else if item == .favoriteSongs {
            if !activeFavoriteSongs.isEmpty { SongRowsSection(title: item.title, songs: Array(activeFavoriteSongs.prefix(6))) }
        } else if item == .favoriteArtists {
            if !home.favoriteArtists.isEmpty { ArtistShelf(artists: home.favoriteArtists) }
        } else if item == .recent {
            if let list = home.albums[.recent], !list.isEmpty {
                AlbumPagesSection(title: item.title, albums: Array(list.prefix(model.homeLayout.recentlyPlayedLimit)))
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

    private func reload(force: Bool = false) async {
        guard !model.offline else { return }
        await home.load(client: model.client, library: model.selectedLibraryID, items: model.homeLayout.visible,
                        seed: model.player?.isRadio == true ? nil : model.player?.current,
                        key: model.contentKey, force: force)
        prepareStations()
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

/// Текущая сессия: крупная карточка в зоне большого пальца на системном стекле (Liquid Glass).
/// Нажатие на карточку открывает очередь, круглая кнопка — пауза и продолжение, полоса под названием — сколько прошло.
private struct ResumeQueueCard: View {
    let player: Player
    let openQueue: () -> Void
    @Environment(AppModel.self) private var model

    var body: some View {
        if let song = player.current {
            HStack(spacing: 0) {
                Button(action: openQueue) {
                    HStack(spacing: 14) {
                        CoverArtView(id: song.coverArt, size: 96, radius: 14)
                            .shadow(color: .black.opacity(0.25), radius: 8, y: 4)
                        VStack(alignment: .leading, spacing: 3) {
                            Text(player.isPlaying ? String(localized: "Now Playing") : String(localized: "Continue"))
                                .font(.caption.weight(.bold))
                                .foregroundStyle(.tint)
                                .textCase(.uppercase)
                            Text(song.title).font(.title3.weight(.semibold)).lineLimit(2)
                            Text(song.displayArtist ?? "").font(.subheadline).foregroundStyle(.secondary).lineLimit(1)
                            if !player.upcoming.isEmpty {
                                Text("\(player.upcoming.count) songs").font(.caption).foregroundStyle(.secondary).lineLimit(1)
                            }
                            // Сколько прошло — системной полосой; темнее — то, что уже загружено.
                            // Время плеера не наблюдаемое, поэтому полоса перерисовывается сама, как на экране плеера.
                            if player.duration > 0 {
                                TimelineView(.periodic(from: .now, by: player.isPlaying ? 1 : 30)) { _ in
                                    let played = min(max(player.progress / player.duration, 0), 1)
                                    ProgressView(value: played)
                                        .overlay { BufferedTrack(played: played, loaded: model.bufferedFraction(for: song), thickness: 4) }
                                }
                                .padding(.top, 6)
                            }
                        }
                        // Колонка во всю ширину: полоса всегда кончается в одном месте, кнопка — по центру за ней.
                        .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityHint("Queue")

                // Та же кнопка, что на экране плеера: символ без подложки, по центру между полосой и краем карточки.
                Button { player.togglePlayPause() } label: {
                    Image(systemName: player.isPlaying ? "pause.fill" : "play.fill")
                        .font(.system(size: 46))
                        .contentTransition(.symbolEffect(.replace))
                        .frame(width: 76)
                        .frame(maxHeight: .infinity)
                        .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(player.isPlaying ? String(localized: "Pause") : String(localized: "Play"))
            }
            .padding([.leading, .vertical], 14)
            .fixedSize(horizontal: false, vertical: true)
            .glassEffect(.regular, in: .rect(cornerRadius: 26))
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
