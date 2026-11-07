import SwiftUI
import PoisqCore
import PoisqPlayback

enum SignInError: LocalizedError {
    case emptyFields, badAddress, badCredentials, unreachable, notSubsonic

    var errorDescription: String? {
        switch self {
        case .emptyFields: String(localized: "Fill in the address, username and password")
        case .badAddress: String(localized: "Check the server address")
        case .badCredentials: String(localized: "Wrong username or password")
        case .unreachable: String(localized: "Couldn't connect to the server")
        case .notSubsonic: String(localized: "This doesn't look like a Subsonic server")
        }
    }
}

/// Корневое состояние: серверы, активная сессия, медиатеки, плеер.
@MainActor @Observable
final class AppModel {
    private(set) var servers: [ServerProfile] = []
    private(set) var activeID: UUID?
    private(set) var player: Player?
    private(set) var capabilities = Capabilities.baseline
    /// Колонка, на которой играет музыка (трансляция poisq). nil — играет телефон.
    private(set) var castZone: SubsonicClient.CastZone?
    /// Колонки, разрешённые в poisq Studio.
    private(set) var castZones: [SubsonicClient.CastZone] = []
    private(set) var libraries: [MusicFolder] = []
    private(set) var client: SubsonicClient?
    let homeLayout = HomeLayout()
    let downloads = DownloadManager()
    let network = NetworkMonitor()
    let cache = CacheManager()
    let equalizer = Equalizer()
    /// Сервер отвечает. Сеть может быть, а домашний сервер — недоступен (VPN, выключен).
    private(set) var serverReachable = true
    /// Короткая плашка «снова онлайн» после возвращения связи.
    private(set) var showBackOnline = false
    /// Автоматически переключаться на загрузки, когда нет сети или сервера.
    var autoOffline: Bool = UserDefaults.standard.object(forKey: "autoOffline") as? Bool ?? true {
        didSet { UserDefaults.standard.set(autoOffline, forKey: "autoOffline"); applyOfflineState() }
    }
    /// Кешировать ближайшие треки и в мобильной сети.
    var cacheOnCellular: Bool = UserDefaults.standard.bool(forKey: "cacheOnCellular") {
        didSet { UserDefaults.standard.set(cacheOnCellular, forKey: "cacheOnCellular") }
    }
    /// Скачивать на устройство и в мобильной сети.
    var downloadOnCellular: Bool = UserDefaults.standard.bool(forKey: "downloadOnCellular") {
        didSet { UserDefaults.standard.set(downloadOnCellular, forKey: "downloadOnCellular"); downloads.allowsCellular = downloadOnCellular }
    }
    /// Предел места под загрузки в гигабайтах; 0 — без ограничений.
    var downloadLimitGB: Int = UserDefaults.standard.integer(forKey: "downloadLimitGB") {
        didSet { UserDefaults.standard.set(downloadLimitGB, forKey: "downloadLimitGB"); applyDownloadLimit() }
    }
    /// Показывает окно «лимит загрузок исчерпан».
    var downloadLimitReached = false
    /// Качество воспроизведения. Кеш подкачивает треки в том же качестве.
    /// Выравнивание громкости: 0 — выкл., 1 — по треку, 2 — по альбому.
    var replayGainMode: Int = UserDefaults.standard.integer(forKey: "replayGainMode") {
        didSet { UserDefaults.standard.set(replayGainMode, forKey: "replayGainMode"); player?.replayGainMode = Player.ReplayGainMode(rawValue: replayGainMode) ?? .off }
    }
    var playbackQuality: StreamQuality = StreamQuality(rawValue: UserDefaults.standard.object(forKey: "playbackQuality") as? Int ?? 0) ?? .original {
        didSet { UserDefaults.standard.set(playbackQuality.rawValue, forKey: "playbackQuality"); player?.streamQuality = playbackQuality }
    }
    /// Качество новых загрузок (уже скачанное остаётся как есть).
    var downloadQuality: StreamQuality = StreamQuality(rawValue: UserDefaults.standard.object(forKey: "downloadQuality") as? Int ?? 0) ?? .original {
        didSet { UserDefaults.standard.set(downloadQuality.rawValue, forKey: "downloadQuality") }
    }
    /// Отправлять на сервер «сейчас играет» и прослушанные песни (скробблинг).
    var scrobbleEnabled: Bool = UserDefaults.standard.object(forKey: "scrobbleEnabled") as? Bool ?? true {
        didSet { UserDefaults.standard.set(scrobbleEnabled, forKey: "scrobbleEnabled") }
    }
    /// Хранить очередь на сервере, чтобы продолжить с другого устройства.
    var syncQueueEnabled: Bool = UserDefaults.standard.object(forKey: "syncQueueEnabled") as? Bool ?? true {
        didSet { UserDefaults.standard.set(syncQueueEnabled, forKey: "syncQueueEnabled") }
    }
    private let scrobbles = ScrobbleQueue()
    private var sessionSaveTask: Task<Void, Never>?
    private var sessionTickTask: Task<Void, Never>?
    private var lastServerSave = Date.distantPast
    private var pingClient: SubsonicClient?
    private var reachabilityTask: Task<Void, Never>?
    private var lastOffline = false
    /// Режим «только загруженные»: музыка берётся с устройства, сеть не нужна.
    var downloadedOnly: Bool = UserDefaults.standard.bool(forKey: "downloadedOnly") {
        didSet { UserDefaults.standard.set(downloadedOnly, forKey: "downloadedOnly"); applyOfflineState() }
    }
    /// Работаем без сервера: вручную выбрано «Загруженные» либо нет связи (и включено автопереключение).
    var offline: Bool { downloadedOnly || (autoOffline && (!network.isConnected || !serverReachable)) }
    /// Офлайн включился сам, а не по выбору пользователя.
    var isAutoOffline: Bool { offline && !downloadedOnly }
    /// Альбом, который нужно открыть на главной (например, из меню плеера).
    var pendingAlbum: Album?
    var pendingArtist: Artist?
    var pendingPlaylist: Playlist?
    /// Песни, которые сейчас добавляют в плейлист (показывает окно выбора плейлиста).
    var playlistTarget: PlaylistTarget?
    /// Растёт, когда нужно открыть полный экран плеера (нажатие на виджет или Live Activity).
    var openPlayerRequest = 0
    /// Что показать в системном окне «Поделиться».
    var shareItems: ShareItems?
    /// Закреплённое на главной.
    let pins = PinStore()
    /// Растёт после изменения плейлистов — списки перезагружаются.
    var playlistsVersion = 0
    private let covers = CoverCache()

    private let defaults = UserDefaults.standard
    private enum Key { static let servers = "servers", active = "activeServer" }

    var isSignedIn: Bool { player != nil }
    var activeServer: ServerProfile? { servers.first { $0.id == activeID } }
    var serverName: String { activeServer?.name ?? "poisq" }
    var selectedLibraryID: String? { activeServer?.libraryID }

    init() {
        // Без этого iOS может не считать приложение источником «Сейчас играет» на экране блокировки (так делает и Amperfy).
        UIApplication.shared.beginReceivingRemoteControlEvents()
        downloads.allowsCellular = downloadOnCellular
        applyDownloadLimit()
        network.onChange = { [weak self] in
            guard let self else { return }
            Task { await self.checkServer() }
        }
        loadServers()
        migrateLegacySession()
        if let id = defaults.string(forKey: Key.active).flatMap(UUID.init(uuidString:)), servers.contains(where: { $0.id == id }) {
            activeID = id
        } else {
            activeID = servers.first?.id
        }
        if let s = activeServer { connect(s) }
    }

    // MARK: Серверы

    func signIn(name: String, address: String, username: String, password: String,
                headers: [String: String] = [:], alternates: [String] = []) async throws {
        let user = username.trimmingCharacters(in: .whitespaces)
        guard !address.trimmingCharacters(in: .whitespaces).isEmpty, !user.isEmpty, !password.isEmpty else { throw SignInError.emptyFields }
        let url = try await verify(address: address, user: user, password: password, headers: headers)
        let title = name.trimmingCharacters(in: .whitespaces)
        let alt = Self.parseAlternates(alternates)
        var profile: ServerProfile
        if let i = servers.firstIndex(where: { $0.url == url && $0.username == user }) {
            profile = servers[i]                       // тот же сервер и пользователь — обновляем
            if !title.isEmpty { profile.name = title }
            profile.alternateURLs = alt; profile.headerNames = headers.keys.sorted()
            servers[i] = profile
        } else {
            profile = ServerProfile(name: title.isEmpty ? (url.host() ?? "poisq") : title, url: url, username: user,
                                    alternateURLs: alt, headerNames: headers.keys.sorted())
            servers.append(profile)
        }
        Keychain.save(password, account: profile.id.uuidString)
        ServerHeaders.save(headers, for: profile.id)
        saveServers()
        switchServer(to: profile.id)
    }

    /// Проверяет доступность и логин/пароль, возвращает рабочий адрес.
    /// Без схемы в адресе пробуем https, затем http.
    private func verify(address: String, user: String, password: String, headers: [String: String] = [:]) async throws -> URL {
        let candidates = ServerURL.candidates(from: address)
        guard !candidates.isEmpty else { throw SignInError.badAddress }
        var lastError: SignInError = .unreachable
        for url in candidates {
            do {
                try await SubsonicClient(baseURL: url, username: user, password: password, headers: headers).ping()
                return url
            } catch let e as SubsonicError {
                switch e {
                case .server(let code, _) where code == 40 || code == 41: throw SignInError.badCredentials
                case .http(401), .http(403): throw SignInError.badCredentials
                default: lastError = .notSubsonic
                }
            } catch is URLError {
                lastError = .unreachable      // пробуем следующий вариант (http после https)
            } catch {
                lastError = .notSubsonic
            }
        }
        throw lastError
    }

    /// Запасные адреса из текстовых полей: пустые и повторы убираем, схему подбираем как для основного.
    static func parseAlternates(_ raw: [String]) -> [URL] {
        var out: [URL] = []
        for r in raw {
            guard let u = ServerURL.candidates(from: r).first, !out.contains(u) else { continue }
            out.append(u)
        }
        return out
    }

    /// Изменение имени, адреса, логина и пароля. Пустой пароль — оставить прежний.
    func updateServer(_ id: UUID, name: String, address: String, username: String, password: String,
                      alternates: [String] = [], headers: [String: String]? = nil) async throws {
        guard let i = servers.firstIndex(where: { $0.id == id }) else { return }
        let user = username.trimmingCharacters(in: .whitespaces)
        guard !user.isEmpty, !address.trimmingCharacters(in: .whitespaces).isEmpty,
              let effectivePassword = password.isEmpty ? Keychain.load(account: id.uuidString) : password
        else { throw SignInError.emptyFields }

        var p = servers[i]
        let alt = Self.parseAlternates(alternates)
        let newHeaders = headers ?? ServerHeaders.load(id)
        let connectionChanged = password.isEmpty == false || user != p.username || ServerURL.candidates(from: address).first != p.url
            || newHeaders != ServerHeaders.load(id) || alt != p.alternateURLs
        if connectionChanged {
            let url = try await verify(address: address, user: user, password: effectivePassword, headers: newHeaders)
            if url != p.url { p.libraryID = nil; p.aliases = [:] }      // другой сервер — старый выбор не подходит
            p.url = url; p.username = user
            Keychain.save(effectivePassword, account: id.uuidString)
            ServerHeaders.save(newHeaders, for: id)
            p.alternateURLs = alt; p.headerNames = newHeaders.keys.sorted()
        }
        let title = name.trimmingCharacters(in: .whitespaces)
        if !title.isEmpty { p.name = title }
        guard let j = servers.firstIndex(where: { $0.id == id }) else { return }   // список мог измениться за время запроса
        servers[j] = p
        saveServers()
        if connectionChanged, id == activeID {
            player?.stop()
            connect(p)
        }
    }

    func switchServer(to id: UUID) {
        guard let profile = servers.first(where: { $0.id == id }) else { return }
        if id == activeID, player != nil { return }
        saveSession()
        player?.stop()
        activeID = id
        defaults.set(id.uuidString, forKey: Key.active)
        connect(profile)
    }

    func removeServer(_ id: UUID) {
        Keychain.delete(account: id.uuidString)
        Task { await covers.clear() }          // обложки удалённого сервера не нужны
        downloads.removeAll(serverID: id)
        cache.clear(serverID: id)
        SessionStore.delete(serverID: id)
        scrobbles.removeAll(serverID: id)
        let wasActive = id == activeID
        servers.removeAll { $0.id == id }
        saveServers()
        guard wasActive else { return }
        player?.stop()
        reachabilityTask?.cancel(); pingClient = nil
        player = nil; client = nil; libraries = []; capabilities = .baseline
        castZone = nil; castZones = []
        activeID = nil
        defaults.removeObject(forKey: Key.active)
        if let next = servers.first { switchServer(to: next.id) }
    }

    func renameServer(_ id: UUID, to name: String) {
        let trimmed = name.trimmingCharacters(in: .whitespaces)
        guard !trimmed.isEmpty, let i = servers.firstIndex(where: { $0.id == id }) else { return }
        servers[i].name = trimmed
        saveServers()
    }

    private func connect(_ profile: ServerProfile) {
        guard let password = Keychain.load(account: profile.id.uuidString) else { return }
        pins.load(serverID: profile.id)
        let headers = ServerHeaders.load(profile.id)
        let client = SubsonicClient(baseURL: profile.url, username: profile.username, password: password, headers: headers)
        let engine = AudioStreamingEngine()
        engine.requestHeaders = headers
        Task { await covers.setHeaders(headers) }
        equalizer.attach(engine)
        let player = Player(client: client, engine: engine)
        player.equalizer = equalizer
        player.libraryID = profile.libraryID
        let sid = profile.id
        player.localURLProvider = { [weak self] song in self?.downloads.localURL(for: song, serverID: sid) }
        player.streamQuality = playbackQuality
        player.replayGainMode = Player.ReplayGainMode(rawValue: replayGainMode) ?? .off
        player.cachedURLProvider = { [weak self] song in
            guard let self else { return nil }
            return self.cache.url(for: song, serverID: sid, quality: self.playbackQuality)
        }
        player.offlineSongsProvider = { [weak self] in
            guard let self, self.offline else { return nil }
            // Без сети играем загруженное и то, что успело попасть в кеш.
            return self.downloads.songs(serverID: sid)
        }
        player.onTrackChange = { [weak self] current, upcoming in
            guard let self, !self.offline, self.cacheOnCellular || !self.network.isExpensive else { return }
            self.cache.prefetch([current] + upcoming.prefix(2), client: client, serverID: sid, quality: self.playbackQuality,
                                isDownloaded: { self.downloads.isDownloaded($0, serverID: sid) })
        }
        player.onNowPlaying = { [weak self] song in
            guard let self, self.scrobbleEnabled, !self.offline else { return }
            Task { try? await client.scrobble(songID: song.id, submission: false) }
        }
        player.onListened = { [weak self] song, started in
            guard let self, self.scrobbleEnabled else { return }
            self.scrobbles.add(serverID: sid, songID: song.id, time: started)
            Task { await self.flushScrobbles() }
        }
        player.onSessionChange = { [weak self] in self?.scheduleSessionSave() }
        sessionTickTask?.cancel()
        sessionTickTask = Task { [weak self] in                       // пока играет — сохраняем место каждые 30 секунд
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(30))
                if self?.player?.isPlaying == true { self?.saveSession() }
            }
        }
        Task { await restoreSession(player: player, client: client, serverID: sid) }
        pingClient = SubsonicClient(baseURL: profile.url, username: profile.username, password: password, headers: headers, session: Self.shortSession)
        startReachability()
        player.artworkProvider = { [weak self] song in
            guard let id = song.coverArt else { return nil }
            return await self?.coverImage(id, pixels: 600)
        }
        self.client = client; self.player = player
        libraries = []; capabilities = .baseline
        castZone = nil; castZones = []
        Task {
            let caps = await client.detectCapabilities()
            guard self.client === client else { return }       // за время запроса сервер могли сменить
            capabilities = caps
            await reloadLibraries()
        }
    }

    /// Обновить список колонок (окно выбора колонок).
    func loadCastZones() async {
        guard let client, capabilities.hasCast else { castZones = []; return }
        if let zones = try? await client.castZones(), self.client === client { castZones = zones }
    }

    /// Перенести воспроизведение на колонку или обратно на телефон (nil). Очередь и место сохраняются, игравшее продолжает играть.
    func cast(to zone: SubsonicClient.CastZone?) {
        guard let player, let client, zone?.id != castZone?.id else { return }
        let wasPlaying = player.isPlaying
        let engine: PlaybackEngine
        if let zone {
            engine = CastEngine(client: client, zone: zone.id)
        } else {
            let local = AudioStreamingEngine()
            local.requestHeaders = activeID.map(ServerHeaders.load) ?? [:]
            engine = local
        }
        equalizer.attach(engine)
        player.useEngine(engine, casting: zone != nil)
        castZone = zone
        if wasPlaying { player.resume() }
    }

    /// Громкость колонки 0…100.
    func setCastVolume(_ volume: Int) {
        guard let client, let zone = castZone else { return }
        castZone?.volume = volume
        if let i = castZones.firstIndex(where: { $0.id == zone.id }) { castZones[i].volume = volume }
        Task { try? await client.castControl(zone: zone.id, action: "volume", volume: volume) }
    }

    /// «Показать альбом» из плеера.
    /// «Поделиться песней»: ссылка с сервера, если он умеет шары; иначе текст «Название — Исполнитель».
    func share(_ song: Song) async {
        let text = [song.title, song.displayArtist].compactMap { $0 }.joined(separator: " — ")
        var items: [Any] = [text]
        if let url = try? await client?.createShare(songID: song.id) { items.append(url) }
        shareItems = ShareItems(items: items)
    }

    func showAlbum(of song: Song) async {
        guard let id = song.albumId, let client, let result = try? await client.album(id: id) else { return }
        pendingAlbum = result.album
    }

    /// «Показать артиста» из плеера и меню.
    func showArtist(of song: Song) {
        guard let id = song.artistId, let name = song.artist else { return }
        pendingArtist = Artist(id: id, name: name)
    }

    // MARK: Связь и офлайн

    private static let shortSession: URLSession = {
        let c = URLSessionConfiguration.ephemeral
        c.timeoutIntervalForRequest = 6
        c.waitsForConnectivity = false
        return URLSession(configuration: c)
    }()

    /// Проверка, отвечает ли сервер. Ошибку сети считаем недоступностью; ответ сервера (даже «ошибка») — доступностью.
    func checkServer() async {
        guard let pingClient else { return }
        guard network.isConnected else { setReachable(false); return }
        do { try await pingClient.ping(); setReachable(true); await returnToPrimaryIfPossible() }
        catch is URLError { setReachable(await failover()) }
        catch { setReachable(true) }
    }

    /// Основной адрес не отвечает — пробуем запасные по порядку. Нашли живой — переключаем клиент на него на лету.
    private func failover() async -> Bool {
        guard let profile = activeServer, !profile.alternateURLs.isEmpty, let client, let pingClient,
              let password = Keychain.load(account: profile.id.uuidString) else { return false }
        let headers = ServerHeaders.load(profile.id)
        for url in [profile.url] + profile.alternateURLs where url != client.baseURL {
            let probe = SubsonicClient(baseURL: url, username: profile.username, password: password, headers: headers, session: Self.shortSession)
            if await Self.answers(probe) {
                client.setBaseURL(url); pingClient.setBaseURL(url)
                return true
            }
        }
        return false
    }

    /// Ответ сервера (даже «ошибка») — это живой адрес; сетевая ошибка — нет.
    private static func answers(_ c: SubsonicClient) async -> Bool {
        do { try await c.ping(); return true } catch is URLError { return false } catch { return true }
    }

    /// Когда работаем на запасном адресе, а основной ожил — возвращаемся на основной.
    private func returnToPrimaryIfPossible() async {
        guard let profile = activeServer, let client, let pingClient, client.baseURL != profile.url,
              let password = Keychain.load(account: profile.id.uuidString) else { return }
        let probe = SubsonicClient(baseURL: profile.url, username: profile.username, password: password,
                                   headers: ServerHeaders.load(profile.id), session: Self.shortSession)
        if await Self.answers(probe) { client.setBaseURL(profile.url); pingClient.setBaseURL(profile.url) }
    }

    private func setReachable(_ value: Bool) {
        serverReachable = value
        applyOfflineState()
        if value { Task { await flushScrobbles() } }          // связь вернулась — досылаем прослушанное офлайн
    }

    /// Раз в минуту, а при пропавшем сервере каждые несколько секунд, проверяем, не вернулся ли он.
    private func startReachability() {
        reachabilityTask?.cancel()
        reachabilityTask = Task { [weak self] in
            while !Task.isCancelled {
                await self?.checkServer()
                let ok = self?.serverReachable ?? true
                try? await Task.sleep(for: .seconds(ok ? 60 : 6))
            }
        }
    }

    /// Реагирует на смену офлайн-состояния: перестраивает очередь и показывает плашку возврата.
    private func applyOfflineState() {
        let now = offline
        guard now != lastOffline else { return }
        let wasAuto = lastOffline && !downloadedOnly
        lastOffline = now
        player?.offlineChanged(now)
        if !now && wasAuto {
            showBackOnline = true
            Task { try? await Task.sleep(for: .seconds(3)); showBackOnline = false }
        }
    }

    // MARK: Скробблинг и сессия

    func flushScrobbles() async {
        guard let client, let sid = activeID, !offline else { return }
        await scrobbles.flush(client: client, serverID: sid)
    }

    /// Сохраняет сессию не чаще раза в пару секунд (смена трека, пауза и перемотка приходят пачками).
    private func scheduleSessionSave() {
        sessionSaveTask?.cancel()
        sessionSaveTask = Task { [weak self] in
            try? await Task.sleep(for: .seconds(2))
            guard !Task.isCancelled else { return }
            self?.saveSession()
        }
    }

    /// Пишет сессию на устройство, а при включённой синхронизации — и на сервер (не чаще раза в 20 секунд).
    func saveSession() {
        guard let player, let sid = activeID, let snapshot = player.snapshot() else { return }
        SessionStore.save(snapshot, serverID: sid)
        guard syncQueueEnabled, !offline, let client, Date().timeIntervalSince(lastServerSave) > 20 else { return }
        lastServerSave = Date()
        let ids = snapshot.songs.map(\.id)
        Task { try? await client.savePlayQueue(ids: ids, current: ids.first, positionMs: Int(snapshot.position * 1000)) }
    }

    /// При запуске поднимает последнюю сессию: из файла на устройстве или, если на сервере она новее (играли на другом устройстве), с сервера.
    private func restoreSession(player: Player, client: SubsonicClient, serverID: UUID) async {
        let local = SessionStore.load(serverID: serverID)
        var chosen = local
        if syncQueueEnabled, !offline, let remote = try? await client.playQueue(),
           let changed = remote.changed, changed > (local?.savedAt ?? .distantPast).addingTimeInterval(10) {
            // Сервер новее: берём его очередь с текущего трека (то, что уже прослушано, не нужно).
            let start = remote.songs.firstIndex { $0.id == remote.current } ?? 0
            chosen = PlayerSnapshot(songs: Array(remote.songs[start...]), position: Double(remote.positionMs) / 1000,
                                    shuffleOn: local?.shuffleOn ?? false, repeatMode: local?.repeatMode ?? .off, endless: false,
                                    savedAt: changed)
        }
        guard let chosen, self.player === player else { return }       // за время запроса сервер могли сменить
        player.restore(chosen)
    }

    // MARK: Обложки

    func coverImage(_ id: String, pixels: Int) async -> UIImage? {
        guard let client, let sid = activeID else { return nil }
        return await covers.image(key: "\(sid.uuidString)_\(id)_\(pixels)") { try client.coverArtURL(id: id, size: pixels) }
    }

    // MARK: Медиатеки

    func reloadLibraries() async {
        guard let client else { return }
        guard let fresh = try? await client.musicFolders(), self.client === client else { return }
        libraries = fresh
        if let id = selectedLibraryID, !fresh.contains(where: { $0.id == id }) { select(nil) }   // пропала с сервера
    }

    /// Своё имя побеждает имя с сервера.
    func name(of folder: MusicFolder) -> String {
        guard let alias = activeServer?.aliases[folder.id], !alias.isEmpty else { return folder.name }
        return alias
    }

    func rename(_ folder: MusicFolder, to newName: String) {
        guard let i = servers.firstIndex(where: { $0.id == activeID }) else { return }
        let trimmed = newName.trimmingCharacters(in: .whitespaces)
        servers[i].aliases[folder.id] = trimmed.isEmpty ? nil : trimmed
        saveServers()
    }

    func select(_ id: String?) {
        guard let i = servers.firstIndex(where: { $0.id == activeID }) else { return }
        servers[i].libraryID = id
        saveServers()
        player?.libraryID = id        // при активном shuffle очередь пересоберётся
    }

    /// Подзаголовок главной. nil, если медиатека одна и выбирать нечего.
    var libraryTitle: String? {
        guard libraries.count > 1 else { return nil }
        guard let id = selectedLibraryID, let f = libraries.first(where: { $0.id == id }) else { return String(localized: "All Libraries") }
        return name(of: f)
    }

    /// Есть ли что переключать по нажатию на заголовок.
    /// Меняется при смене сервера или медиатеки — по нему главная перезагружает данные.
    var contentKey: String { "\(activeID?.uuidString ?? "")|\(selectedLibraryID ?? "")" }

    // MARK: Загрузки

    func download(_ songs: [Song]) {
        guard let client, let id = activeID else { return }
        if downloads.download(songs, client: client, serverID: id, quality: downloadQuality) > 0 { downloadLimitReached = true }
    }

    private func applyDownloadLimit() {
        downloads.limitBytes = downloadLimitGB > 0 ? Int64(downloadLimitGB) * 1_073_741_824 : nil
    }
    func removeDownloads(_ songs: [Song]) { if let id = activeID { downloads.remove(songs, serverID: id) } }
    /// Качество, в котором трек реально играет сейчас (для строки под полосой прогресса).
    func qualityInUse(for song: Song) -> StreamQuality {
        guard let player else { return playbackQuality }
        switch player.source(of: song) {
        case .downloaded: return activeID.map { downloads.quality(of: song, serverID: $0) } ?? .original
        case .cached, .streaming: return playbackQuality
        }
    }

    func isDownloaded(_ song: Song) -> Bool { activeID.map { downloads.isDownloaded(song, serverID: $0) } ?? false }
    /// Загруженные песни текущего сервера.
    var downloadedSongs: [Song] { activeID.map { downloads.songs(serverID: $0) } ?? [] }
    /// Альбомы, собранные из загруженных песен.
    var downloadedAlbums: [Album] {
        var seen = Set<String>(); var out: [Album] = []
        for s in downloadedSongs {
            guard let aid = s.albumId, seen.insert(aid).inserted else { continue }
            out.append(Album(id: aid, name: s.album ?? "", artist: s.artist, coverArt: s.coverArt))
        }
        return out
    }

    var hasSwitcher: Bool { servers.count > 1 || libraries.count > 1 }

    // MARK: Хранение

    private func loadServers() {
        guard let data = defaults.data(forKey: Key.servers), let list = try? JSONDecoder().decode([ServerProfile].self, from: data) else { return }
        servers = list
    }

    private func saveServers() {
        defaults.set(try? JSONEncoder().encode(servers), forKey: Key.servers)
    }

    /// Первые сборки хранили один сервер в отдельных ключах — переносим в список.
    private func migrateLegacySession() {
        guard servers.isEmpty, let s = defaults.string(forKey: "server"), let url = URL(string: s), let user = defaults.string(forKey: "user") else { return }
        let old = "\(user)@\(url.absoluteString)"
        if let password = Keychain.load(account: old) {
            let name = defaults.string(forKey: "serverName") ?? ""
            let p = ServerProfile(name: name.isEmpty ? (url.host() ?? "poisq") : name, url: url, username: user,
                                  libraryID: defaults.string(forKey: "library"))
            Keychain.save(password, account: p.id.uuidString)
            Keychain.delete(account: old)
            servers = [p]
            saveServers()
        }
        for k in ["server", "user", "serverName", "library", "libraryAliases"] { defaults.removeObject(forKey: k) }
    }
}
