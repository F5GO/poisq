import SwiftUI
import WidgetKit
import ActivityKit
import CoreSpotlight
import PoisqCore
import PoisqPlayback

/// Системная интеграция: виджеты, Live Activity, Spotlight, переход по ссылкам.
/// Следит за `AppModel` и ничего не знает о прослушиваниях: в Spotlight попадает только каталог.
@MainActor
final class SystemIntegration {
    static let shared = SystemIntegration()

    private var model: AppModel { AppModel.shared }
    private var started = false
    private var refreshTask: Task<Void, Never>?
    private var catalogTask: Task<Void, Never>?
    private var catalogKey: String?          // для какого сервера/медиатеки каталог уже обновлён
    private var lastCatalogAt = Date.distantPast
    private var activity: Activity<PlaybackActivityAttributes>?
    private var lastTrack: SharedTrack?
    private var wasSignedIn = false
    private var lastSpotlightSignature: String?

    func start() {
        guard !started else { return }
        started = true
        PlaybackCommands.handler = { [weak self] command in self?.perform(command) }
        track()
        scheduleRefresh()
    }

    // MARK: Команды (виджеты, Live Activity, Siri)

    func perform(_ command: PlaybackCommand) {
        guard let player = model.player else { return }
        switch command {
        case .toggle: player.togglePlayPause()
        case .pause: player.pause()
        case .next: player.next()
        case .previous: player.previous()
        }
    }

    // MARK: Наблюдение

    private func track() {
        withObservationTracking {
            let m = model
            _ = m.activeID; _ = m.contentKey; _ = m.playlistsVersion; _ = m.offline
            _ = m.player; _ = m.player?.current; _ = m.player?.isPlaying; _ = m.player?.state
        } onChange: { [weak self] in
            Task { @MainActor in self?.scheduleRefresh(); self?.track() }
        }
    }

    private func scheduleRefresh() {
        refreshTask?.cancel()
        refreshTask = Task { [weak self] in
            try? await Task.sleep(for: .milliseconds(250))       // смена трека и состояния приходит пачкой
            guard !Task.isCancelled else { return }
            await self?.refresh()
        }
    }

    private func refresh() async {
        guard model.isSignedIn else { signedOut(); return }
        wasSignedIn = true
        await publishNowPlaying()
        refreshCatalogIfNeeded()
    }

    // MARK: Сейчас играет: виджет и Live Activity

    private func publishNowPlaying() async {
        guard let player = model.player, let song = player.current, player.isActive || player.isLoading else {
            if lastTrack != nil { lastTrack = nil; SharedStore.write(SharedTrack?.none, "nowplaying.json"); WidgetCenter.shared.reloadTimelines(ofKind: "NowPlaying") }
            await endActivity()
            return
        }
        let file = await saveCover(song.coverArt)
        let t = SharedTrack(title: song.title, artist: song.artist ?? "", albumID: song.albumId, albumName: song.album,
                            isPlaying: player.isPlaying, coverFile: file)
        guard t != lastTrack else { return }
        lastTrack = t
        SharedStore.write(t, "nowplaying.json")
        WidgetCenter.shared.reloadTimelines(ofKind: "NowPlaying")
        await updateActivity(t)
    }

    private func updateActivity(_ t: SharedTrack) async {
        guard UserDefaults.standard.object(forKey: "liveActivityEnabled") as? Bool ?? true else { await endActivity(); return }
        let state = PlaybackActivityAttributes.ContentState(title: t.title, artist: t.artist, isPlaying: t.isPlaying, coverFile: t.coverFile)
        let content = ActivityContent(state: state, staleDate: nil)
        if await Self.updateRunningActivity(content) {
        } else if ActivityAuthorizationInfo().areActivitiesEnabled {
            // Запрос разрешён только на переднем плане; из фона (кнопка виджета) просто пропускаем.
            activity = try? Activity.request(attributes: PlaybackActivityAttributes(), content: content)
        }
    }

    private func endActivity() async {
        activity = nil
        await Self.endAllActivities()
    }

    // Activity не Sendable: работаем с ней вне главного актора, не передавая через границу изоляции.
    private nonisolated static func updateRunningActivity(_ content: ActivityContent<PlaybackActivityAttributes.ContentState>) async -> Bool {
        guard let a = Activity<PlaybackActivityAttributes>.activities.first(where: { $0.activityState == .active }) else { return false }
        await a.update(content)
        return true
    }

    private nonisolated static func endAllActivities() async {
        for a in Activity<PlaybackActivityAttributes>.activities { await a.end(nil, dismissalPolicy: .immediate) }
    }

    // MARK: Обложки в общем кэше

    private static func coverFileName(_ id: String) -> String {
        "c_" + id.map { $0.isLetter || $0.isNumber || $0 == "-" || $0 == "_" ? String($0) : "_" }.joined() + ".jpg"
    }

    /// Кладёт обложку в общий контейнер; расширение читает её оттуда, сети у него нет.
    private func saveCover(_ id: String?) async -> String? {
        guard let id, let dir = PoisqShared.coversDirectory else { return nil }
        let name = Self.coverFileName(id)
        let url = dir.appending(path: name)
        if FileManager.default.fileExists(atPath: url.path) { return name }
        guard let image = await model.coverImage(id, pixels: 300), let data = image.jpegData(compressionQuality: 0.8) else { return nil }
        try? data.write(to: url, options: .atomic)
        return name
    }

    private func pruneCovers(keeping: Set<String>) {
        guard let dir = PoisqShared.coversDirectory,
              let files = try? FileManager.default.contentsOfDirectory(atPath: dir.path) else { return }
        for f in files where !keeping.contains(f) { try? FileManager.default.removeItem(at: dir.appending(path: f)) }
    }

    // MARK: Каталог: недавние альбомы и Spotlight

    private func refreshCatalogIfNeeded() {
        guard !model.offline, let client = model.client else { return }
        let key = model.contentKey + "|\(model.playlistsVersion)"
        guard key != catalogKey || Date().timeIntervalSince(lastCatalogAt) > 6 * 3600 else { return }
        catalogKey = key
        lastCatalogAt = Date()
        let library = model.selectedLibraryID
        catalogTask?.cancel()
        catalogTask = Task { [weak self] in
            guard let self else { return }
            async let recent = try? client.albumList(type: "newest", size: 12, musicFolderId: library)
            async let playlists = try? client.playlists()
            await self.publishRecent(await recent ?? [])
            await self.indexSpotlight(client: client, library: library, playlists: await playlists ?? [])
        }
    }

    private func publishRecent(_ albums: [Album]) async {
        var out: [SharedAlbum] = []
        for a in albums { out.append(SharedAlbum(id: a.id, name: a.name, artist: a.artist, coverFile: await saveCover(a.coverArt))) }
        guard !Task.isCancelled else { return }
        SharedStore.write(out, "recent-albums.json")
        pruneCovers(keeping: Set(out.compactMap(\.coverFile)).union([lastTrack?.coverFile].compactMap { $0 }))
        WidgetCenter.shared.reloadTimelines(ofKind: "RecentAlbums")
    }

    private func indexSpotlight(client: SubsonicClient, library: String?, playlists: [Playlist]) async {
        let artists = (try? await client.artists(musicFolderId: library)) ?? []
        // По алфавиту, а не по частоте прослушивания: в индекс не попадает ничего о том, что слушает человек.
        var albums: [Album] = []
        for page in 0..<4 {
            guard let chunk = try? await client.albumList(type: "alphabeticalByName", size: 500, offset: page * 500, musicFolderId: library) else { break }
            albums += chunk
            if chunk.count < 500 { break }
        }
        guard !Task.isCancelled else { return }
        let signature = "\(model.activeID?.uuidString ?? "")|\(artists.count)|\(albums.count)|\(playlists.count)|" +
            (albums.prefix(20).map(\.id) + artists.prefix(20).map(\.id) + playlists.map(\.id)).joined(separator: ",")
        guard signature != lastSpotlightSignature else { return }
        let index = CSSearchableIndex.default()
        try? await index.deleteAllSearchableItems()           // содержимое сменилось (сервер, медиатека, правка) — пересобираем
        guard !Task.isCancelled else { return }
        do {
            try await index.indexAppEntities(artists.map(ArtistEntity.init))
            try await index.indexAppEntities(albums.map(AlbumEntity.init))
            try await index.indexAppEntities(playlists.map(PlaylistEntity.init))
            lastSpotlightSignature = signature
        } catch {}
    }

    // MARK: Выход из аккаунта

    private func signedOut() {
        guard wasSignedIn || lastTrack != nil || lastSpotlightSignature != nil else { return }
        wasSignedIn = false
        catalogKey = nil; lastTrack = nil; lastSpotlightSignature = nil
        catalogTask?.cancel()
        SharedStore.write(SharedTrack?.none, "nowplaying.json")
        SharedStore.write([SharedAlbum]?.none, "recent-albums.json")
        pruneCovers(keeping: [])
        WidgetCenter.shared.reloadAllTimelines()
        Task {
            await endActivity()
            try? await CSSearchableIndex.default().deleteAllSearchableItems()
        }
    }

    // MARK: Переход по ссылке

    func open(_ target: DeepLink.Target) {
        switch target {
        case .album(let id, let name):
            // В ссылке только id и имя — полный альбом (обложка, год) берём у сервера; без сети откроем по тому, что есть.
            Task {
                let full = try? await model.client?.album(id: id).album
                model.pendingAlbum = full ?? Album(id: id, name: name)
            }
        case .artist(let id, let name): model.pendingArtist = Artist(id: id, name: name)
        case .playlist(let id, let name): model.pendingPlaylist = Playlist.make(id: id, name: name)
        case .player: model.openPlayerRequest += 1
        }
    }

    func open(_ url: URL) { if let t = DeepLink.parse(url) { open(t) } }
}

extension Playlist {
    /// У `Playlist` нет публичного init; для перехода по ссылке достаточно id и имени.
    static func make(id: String, name: String) -> Playlist? {
        try? JSONDecoder().decode(Playlist.self, from: JSONSerialization.data(withJSONObject: ["id": id, "name": name]))
    }
}
