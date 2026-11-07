import Foundation
import Observation
import PoisqCore
#if os(iOS)
import UIKit
#endif

public enum PlayerState: Sendable, Equatable { case idle, loading, active }
public enum RepeatMode: Int, Sendable, Equatable, Codable { case off, all, one }
/// Откуда играет трек: стрим с сервера, кеш (докачано при прослушивании) или загрузка на устройство.
public enum TrackSource: Sendable, Equatable { case streaming, cached, downloaded }

/// Состояние плеера для сохранения: что играет, откуда и с какого места.
public struct PlayerSnapshot: Codable, Sendable {
    public var songs: [Song]              // songs[0] — играющий трек
    public var position: Double           // секунды
    public var shuffleOn: Bool
    public var repeatMode: RepeatMode
    public var endless: Bool
    public var savedAt: Date
    public init(songs: [Song], position: Double, shuffleOn: Bool, repeatMode: RepeatMode, endless: Bool, savedAt: Date = Date()) {
        self.songs = songs; self.position = position; self.shuffleOn = shuffleOn
        self.repeatMode = repeatMode; self.endless = endless; self.savedAt = savedAt
    }
}

/// Очередь, shuffle и повтор поверх любого PlaybackEngine. Работает только со стандартным Subsonic.
@MainActor @Observable
public final class Player {
    public private(set) var state: PlayerState = .idle
    public private(set) var current: Song?
    public private(set) var isPlaying = false
    /// queue[0] — играющий трек, дальше — очередь.
    public private(set) var queue: [Song] = []
    public private(set) var shuffleOn = false
    public private(set) var repeatMode: RepeatMode = .off
    /// Идентификаторы избранных треков, с которыми мы работали в этой сессии.
    public private(set) var favoriteIDs: Set<String> = []
    /// Качество стрима. Смена перестраивает очередь: текущий трек доиграет как есть, следующие пойдут в новом качестве.
    public var streamQuality: StreamQuality = .original {
        didSet { if oldValue != streamQuality, state == .active, !endless { replaceUpcoming(with: upcoming) } else if oldValue != streamQuality, endless, state == .active { Task { await rebuildUpcoming() } } }
    }
    /// Ограничение shuffle одной медиатекой; nil — все.
    public var libraryID: String? {
        didSet { if oldValue != libraryID, endless, state == .active { Task { await rebuildUpcoming() } } }
    }

    /// Локальный файл трека, если он загружен на устройство. Тогда играем его, а не стрим.
    public var localURLProvider: ((Song) -> URL?)?
    /// Файл из кеша прослушивания, если трек уже докачан.
    public var cachedURLProvider: ((Song) -> URL?)?
    /// Вызывается при смене трека: текущий и ближайшие — их приложение может подкачать в кеш.
    public var onTrackChange: ((_ current: Song, _ upcoming: [Song]) -> Void)?
    /// Трек начал играть: отправить серверу «сейчас играет».
    public var onNowPlaying: ((Song) -> Void)?
    /// Трек прослушан достаточно (половина или 4 минуты): засчитать прослушивание. Второй параметр — когда трек начался.
    public var onListened: ((Song, Date) -> Void)?
    /// Что-то изменилось в очереди, треке или паузе: пора сохранить сессию.
    public var onSessionChange: (() -> Void)?
    /// Песни для режима «только загруженные»: nil — обычный режим (случайные треки берём у сервера).
    public var offlineSongsProvider: (() -> [Song]?)?

    private var engine: PlaybackEngine
    /// Звук играет на колонке (трансляция poisq): локальные и кешированные файлы не используются, нужны ссылки сервера.
    public private(set) var isCasting = false
    /// Прямые ссылки на поток для радио (id песни → URL). Радио играет не через `stream` сервера.
    private var streamOverrides: [String: URL] = [:]
    /// Подкасты и другая речь: у них своя скорость, они не засчитываются как прослушивание музыки и за ними не идёт автоподборка.
    public private(set) var spokenIDs: Set<String> = []
    public static let radioPrefix = "radio:"
    public var isRadio: Bool { current?.id.hasPrefix(Self.radioPrefix) == true }
    /// Помечает песни как речь (подкасты): после перезапуска приложения скорость возвращается.
    public func markSpoken(_ ids: [String]) { spokenIDs.formUnion(ids) }
    public var isSpoken: Bool { current.map { spokenIDs.contains($0.id) } == true }
    /// Название текущего эфира из ICY-метаданных потока.
    public private(set) var streamTitle: String?
    /// Скорость речи 0.75–2×. Применяется только к подкастам, музыка идёт как обычно.
    public var speechRate: Float = {
        let v = UserDefaults.standard.float(forKey: "speechRate"); return v == 0 ? 1 : v
    }() {
        didSet { UserDefaults.standard.set(speechRate, forKey: "speechRate"); applyRate() }
    }
    private func applyRate() { engine.setRate(isSpoken ? speechRate : 1) }
    /// Эквалайзер приложения: подбирает пресет по жанру каждого нового трека.
    public var equalizer: Equalizer?
    private let client: SubsonicClient
    private var urlToSong: [URL: Song] = [:]
    /// Ссылка, под которой трек стоит в очереди движка (нужна, чтобы убрать его, даже если качество с тех пор сменилось).
    private var queuedURL: [String: URL] = [:]
    private var enqueuedIDs: Set<String> = []
    private var history: [Song] = []
    /// Исходный порядок списка (альбом, плейлист) — к нему возвращаемся при выключении shuffle.
    private var original: [Song] = []
    /// Бесконечный shuffle по всей медиатеке (кнопка Play на главной).
    private var endless = false
    /// «Продолжать автоматически»: когда очередь кончается, дальше идут похожие треки.
    public var autoplay: Bool = UserDefaults.standard.bool(forKey: "autoplay") {
        didSet {
            UserDefaults.standard.set(autoplay, forKey: "autoplay")
            if autoplay { if state == .active { Task { await refillMix() } } } else { dropAuto() }
        }
    }
    /// Треки, которые добавили мы, а не пользователь — в очереди они идут отдельным блоком.
    public private(set) var autoIDs: Set<String> = []
    public var autoUpcoming: [Song] { upcoming.filter { autoIDs.contains($0.id) } }
    public var manualUpcoming: [Song] { upcoming.filter { !autoIDs.contains($0.id) } }
    private var refilling = false
    private var suppressIdle = false
    private var playingURL: URL?
    /// Не nil — очередь восстановлена после перезапуска, движок ещё не запущен; значение — позиция в секундах.
    private var restoredPosition: Double?
    private var pendingSeek: Double?
    @ObservationIgnored private var listenTask: Task<Void, Never>?
    private let batchSize = 50
    private let refillThreshold = 10
    #if os(iOS)
    @ObservationIgnored private var bridge: NowPlayingBridge?
    /// Обложка для экрана блокировки.
    public var artworkProvider: ((Song) async -> UIImage?)? {
        didSet { bridge?.artworkProvider = artworkProvider }
    }
    #endif

    public init(client: SubsonicClient, engine: PlaybackEngine) {
        self.client = client
        self.engine = engine
        wire(engine)
        #if os(iOS)
        bridge = NowPlayingBridge(player: self)
        #endif
    }

    private func wire(_ engine: PlaybackEngine) {
        engine.onTrackStarted = { [weak self] url in self?.trackStarted(url) }
        engine.onStateChanged = { [weak self] s in self?.engineStateChanged(s) }
        engine.onError = { [weak self] _ in self?.playbackFailed() }
        engine.setMetadataHandler { [weak self] title in self?.streamTitle = title }
    }

    /// Меняет движок (телефон ↔ колонка). Очередь и место сохраняются, воспроизведение остаётся на паузе.
    public func useEngine(_ new: PlaybackEngine, casting: Bool) {
        let saved = snapshot()
        stop()
        engine = new
        isCasting = casting
        wire(new)
        if let saved { restore(saved) }
    }

    public var isActive: Bool { state == .active }
    public var isLoading: Bool { state == .loading }
    public var progress: Double { restoredPosition ?? engine.progress }
    public var duration: Double { engine.duration > 0 ? engine.duration : Double(current?.duration ?? 0) }
    public var hasNext: Bool { queue.count > 1 }
    /// Треки после текущего.
    public var upcoming: [Song] { Array(queue.dropFirst()) }
    /// Для играющего трека — откуда он играет на самом деле; для остальных — где он доступен.
    public func source(of song: Song) -> TrackSource {
        if song == current, let url = playingURL {
            guard url.isFileURL else { return .streaming }
            return localURLProvider?(song) == url ? .downloaded : .cached
        }
        if localURLProvider?(song) != nil { return .downloaded }
        return cachedURLProvider?(song) != nil ? .cached : .streaming
    }
    // MARK: ReplayGain

    public enum ReplayGainMode: Int, Sendable, CaseIterable { case off, track, album }
    /// Выравнивание громкости треков по данным сервера. Без данных — без изменений.
    public var replayGainMode: ReplayGainMode = .off { didSet { applyReplayGain() } }
    private func applyReplayGain() {
        guard replayGainMode != .off, let rg = current?.replayGain, let db = rg.gainDb(album: replayGainMode == .album) else {
            engine.setReplayGain(0); return
        }
        engine.setReplayGain(Float(db))
    }

    public func isFavorite(_ song: Song) -> Bool { favoriteIDs.contains(song.id) }
    /// Оценки, выставленные в этой сессии (поверх того, что пришло с сервером).
    public private(set) var ratingOverrides: [String: Int] = [:]
    public func rating(of song: Song) -> Int { ratingOverrides[song.id] ?? song.userRating ?? 0 }
    public func setRating(_ rating: Int, for song: Song) async {
        let old = self.rating(of: song)
        ratingOverrides[song.id] = rating                       // сразу, не дожидаясь сети
        do { try await client.setRating(rating, songID: song.id) }
        catch { ratingOverrides[song.id] = old }
    }

    // MARK: Запуск

    /// Кнопка Play на главной: shuffle по всей медиатеке, бесконечный.
    public func shuffleLibrary() async {
        guard state == .idle else { return }
        state = .loading
        endless = true; shuffleOn = true; original = []
        do {
            let songs = try await randomSongs(count: batchSize)
            guard !songs.isEmpty else { state = .idle; return }
            history = []
            rebuild(songs)
        } catch {
            state = .idle
        }
    }

    /// Играет список с позиции `index`. `shuffled` — сразу вперемешку (исходный порядок сохраняется для выключения shuffle).
    /// Откуда брать продолжение, когда очередь подходит к концу (например, список песен на главной).
    /// Вернул пусто — источник кончился: идёт автоподборка (если включена) или случайные песни, музыка не останавливается.
    public var continuation: (@MainActor () async -> [Song])?
    private var continuing = false

    /// Играет список и подхватывает продолжение из `continuation`.
    public func play(_ songs: [Song], startAt index: Int = 0, continuation: @escaping @MainActor () async -> [Song]) {
        play(songs, startAt: index)
        self.continuation = continuation
    }

    private func refillContinuation() async {
        guard let provider = continuation, !continuing else { return }
        continuing = true
        defer { continuing = false }
        var more = await provider()
        let taken = Set(queue.map(\.id))
        more = more.filter { !taken.contains($0.id) }
        if more.isEmpty {
            if autoplay { continuation = nil; await refillMix(); return }       // источник кончился — дальше похожие
            continuation = { [weak self] in (try? await self?.randomSongs(count: 40)) ?? [] }   // или случайные: играем дальше
            more = (await continuation?() ?? []).filter { !taken.contains($0.id) }
        }
        for s in more {
            if restoredPosition != nil { queue.append(s) } else { try? enqueue(s) }
        }
        onSessionChange?()
    }

    public func play(_ songs: [Song], startAt index: Int = 0, shuffled: Bool = false) {
        guard songs.indices.contains(index) else { return }
        continuation = nil
        endless = false
        autoIDs = []
        original = songs
        shuffleOn = shuffled
        state = .loading
        history = []
        let list: [Song]
        if shuffled {
            list = [songs[index]] + (songs.enumerated().filter { $0.offset != index }.map(\.element)).shuffled()
        } else {
            list = Array(songs[index...])
        }
        rebuild(list)
    }

    // MARK: Подкасты и радио

    /// Играет выпуски подкаста. `position` — секунды, с которых продолжить первый выпуск.
    public func playSpoken(_ songs: [Song], startAt index: Int = 0, position: Double = 0) {
        spokenIDs.formUnion(songs.map(\.id))
        play(songs, startAt: index)
        if position > 1 { pendingSeek = position }
    }

    /// Включает интернет-радио: поток без перемотки, без очереди и без автоподборки.
    public func playRadio(id: String, name: String, streamURL: URL) {
        let song = Song(id: Self.radioPrefix + id, title: name)
        streamOverrides[song.id] = streamURL
        play([song])
        continuation = nil
    }

    // MARK: Таймер сна

    /// Когда сработает таймер сна (nil — не стоит). При «до конца трека» значение обновляется под текущий трек.
    public private(set) var sleepEndsAt: Date?
    public private(set) var sleepEndOfTrack = false
    @ObservationIgnored private var sleepTask: Task<Void, Never>?

    /// Через сколько минут остановить музыку; nil — выключить таймер.
    public func setSleepTimer(minutes: Int?) {
        sleepEndOfTrack = false
        guard let minutes else { cancelSleep(); return }
        startSleep(after: TimeInterval(minutes) * 60)
    }

    /// Остановить, когда доиграет текущий трек (и каждый следующий, пока включено).
    public func setSleepAtEndOfTrack() {
        sleepEndOfTrack = true
        scheduleSleep(afterTrack: true)
    }

    private func scheduleSleep(afterTrack: Bool) {
        let remaining = max(duration - progress, 1)
        startSleep(after: remaining)
    }

    private func startSleep(after seconds: TimeInterval) {
        sleepTask?.cancel()
        engine.volume = 1
        sleepEndsAt = Date().addingTimeInterval(seconds)
        sleepTask = Task { [weak self] in
            let fade = min(8, seconds)
            try? await Task.sleep(for: .seconds(max(seconds - fade, 0)))
            guard let self, !Task.isCancelled else { return }
            // Плавное затухание перед остановкой.
            let steps = 40
            for i in 1...steps {
                if Task.isCancelled { return }
                self.engine.volume = 1 - Float(i) / Float(steps)
                try? await Task.sleep(for: .seconds(fade / Double(steps)))
            }
            guard !Task.isCancelled else { return }
            self.pause()
            self.engine.volume = 1
            self.sleepEndsAt = nil
            self.sleepEndOfTrack = false
        }
    }

    private func cancelSleep() {
        sleepTask?.cancel(); sleepTask = nil
        sleepEndsAt = nil
        engine.volume = 1
    }

    // MARK: Управление

    public func resume() {
        if restoredPosition != nil { startRestored(); return }
        engine.resume()
    }
    public func pause() { engine.pause() }
    public func togglePlayPause() { isPlaying ? engine.pause() : resume() }

    public func seek(to seconds: Double) {
        guard !isRadio else { return }                                           // эфир не перематывается
        if restoredPosition != nil { restoredPosition = seconds; return }       // движок ещё не запущен: запоминаем место
        // Перемотка в самый конец (или чуть за него из-за неточной длительности) заставляет движок закончить трек
        // и перейти к следующему из своей очереди. Не даём промотать ближе чем за секунду до конца.
        var target = max(seconds, 0)
        let length = engine.duration
        if length > 2 { target = min(target, length - 1) }
        engine.seek(to: target)
        #if os(iOS)
        bridge?.update()
        #endif
    }

    /// Следующий трек. Очередь — наша, поэтому не просим движок «пропустить»: его внутренняя очередь может расходиться с нашей.
    /// Пересобираем со следующего трека, а интерфейс переключается сразу, не дожидаясь загрузки.
    public func next() {
        guard hasNext else { return }
        if let current { history.append(current) }
        rebuild(Array(queue.dropFirst()), immediate: true)
    }

    /// Предыдущий трек (для соседней обложки в плеере).
    public var previousSong: Song? { history.last }

    /// Всегда на предыдущий трек (свайп); если его нет — в начало текущего.
    public func playPrevious() {
        guard let current, restoredPosition == nil, let prev = history.popLast() else { seek(to: 0); return }
        rebuild([prev, current] + queue.dropFirst(), immediate: true)
    }

    /// Больше трёх секунд от начала — в начало трека, иначе предыдущий трек.
    public func previous() {
        guard let current else { return }
        if restoredPosition != nil { restoredPosition = 0; return }
        guard progress <= 3, let prev = history.popLast() else { seek(to: 0); return }
        rebuild([prev, current] + queue.dropFirst(), immediate: true)
    }

    /// Перейти к треку очереди (`index` — позиция в `upcoming`).
    public func jump(toUpcoming index: Int) {
        let target = index + 1
        guard queue.indices.contains(target), let current else { return }
        history.append(current)
        history += queue[1..<target]
        rebuild(Array(queue[target...]), immediate: true)
    }

    // MARK: Правка очереди

    /// «Играть следом»: трек встаёт сразу после текущего. Если он уже в очереди, переносится.
    public func playNext(_ songs: [Song]) {
        insert(songs, atUpcoming: 0)
    }

    /// «В конец очереди».
    public func addToQueue(_ songs: [Song]) {
        insert(songs, atUpcoming: nil)
    }

    private func insert(_ songs: [Song], atUpcoming index: Int?) {
        guard state == .active, current != nil else { play(songs); return }       // ничего не играет — просто запускаем
        let ids = Set(songs.map(\.id))
        var rest = upcoming.filter { !ids.contains($0.id) }
        let add = songs.filter { $0.id != current?.id }
        let tail = rest.firstIndex { autoIDs.contains($0.id) } ?? rest.count      // свои треки всегда перед автоподборкой
        rest.insert(contentsOf: add, at: min(index ?? tail, rest.count))
        autoIDs.subtract(add.map(\.id))
        replaceUpcoming(with: rest)
        original += add.filter { s in !original.contains { $0.id == s.id } }
        onSessionChange?()
    }

    public func removeUpcoming(at offsets: IndexSet) {
        var rest = upcoming
        rest = rest.enumerated().filter { !offsets.contains($0.offset) }.map(\.element)
        replaceUpcoming(with: rest)
        onSessionChange?()
    }

    public func moveUpcoming(from source: IndexSet, to destination: Int) {
        var rest = upcoming
        let moving = source.sorted().map { rest[$0] }
        let before = source.filter { $0 < destination }.count
        rest = rest.enumerated().filter { !source.contains($0.offset) }.map(\.element)
        rest.insert(contentsOf: moving, at: destination - before)
        replaceUpcoming(with: rest)
        onSessionChange?()
    }

    // MARK: Сессия

    /// Текущее состояние для сохранения (nil — ничего не играет). В снимок попадает не больше 100 треков.
    public func snapshot() -> PlayerSnapshot? {
        guard state == .active, current != nil, !isRadio else { return nil }
        return PlayerSnapshot(songs: Array(queue.prefix(100)), position: progress, shuffleOn: shuffleOn,
                              repeatMode: repeatMode, endless: endless)
    }

    /// Восстанавливает очередь на паузе: движок запустится при первом нажатии Play, с сохранённого места.
    public func restore(_ snapshot: PlayerSnapshot) {
        guard state == .idle, let first = snapshot.songs.first else { return }
        history = []
        original = snapshot.songs
        endless = snapshot.endless
        shuffleOn = snapshot.shuffleOn
        repeatMode = snapshot.repeatMode
        queue = snapshot.songs
        current = first
        restoredPosition = snapshot.position
        if autoplay, snapshot.songs.count <= refillThreshold + 1 { Task { await refillMix() } }
        isPlaying = false
        state = .active
        if first.starred != nil { favoriteIDs.insert(first.id) }
    }

    private func startRestored() {
        let position = restoredPosition ?? 0
        restoredPosition = nil
        pendingSeek = position > 1 ? position : nil
        rebuild(queue)
    }

    /// Следит за временем реального прослушивания и вызывает `onListened`, когда набралась половина трека (не больше 4 минут).
    private func startListening(_ song: Song) {
        listenTask?.cancel()
        onNowPlaying?(song)
        let started = Date()
        let duration = Double(song.duration ?? 0)
        guard duration == 0 || duration >= 30 else { return }          // совсем короткие треки не засчитываем
        let threshold = duration > 0 ? min(duration / 2, 240) : 60
        listenTask = Task { [weak self] in
            var listened = 0.0
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(1))
                guard let self, !Task.isCancelled else { return }
                if self.isPlaying { listened += 1 }
                if listened >= threshold { self.onListened?(song, started); return }
            }
        }
    }

    /// Связь пропала или вернулась. Без сети оставляем в очереди только то, что можно проиграть с устройства.
    public func offlineChanged(_ offline: Bool) {
        guard offline, state == .active else { return }
        if endless {
            Task { if let songs = try? await randomSongs(count: batchSize) { replaceUpcoming(with: songs) } }
        } else {
            replaceUpcoming(with: upcoming.filter { localURLProvider?($0) != nil || cachedURLProvider?($0) != nil })
        }
    }

    /// Трек не загрузился (нет сети, файл недоступен): переходим к следующему, чтобы музыка не вставала.
    private func playbackFailed() {
        if hasNext { next() } else { isPlaying = false }
    }

    public func toggleShuffle() {
        shuffleOn.toggle()
        if endless {
            if shuffleOn, queue.count <= refillThreshold { Task { await refill() } }
            return
        }
        guard let current else { return }
        let next: [Song]
        if shuffleOn {
            next = original.filter { $0.id != current.id }.shuffled()
        } else if let i = original.firstIndex(where: { $0.id == current.id }) {
            next = Array(original[(i + 1)...])                       // возвращаем исходный порядок после текущего
        } else {
            next = original.filter { $0.id != current.id }
        }
        replaceUpcoming(with: next)
    }

    public func cycleRepeat() {
        repeatMode = switch repeatMode { case .off: .all; case .all: .one; case .one: .off }
        applyRepeat()
    }

    public func toggleFavorite(_ song: Song) async {
        let wanted = !isFavorite(song)
        if wanted { favoriteIDs.insert(song.id) } else { favoriteIDs.remove(song.id) }     // сразу, не дожидаясь сети
        do { try await client.setStarred(wanted, songID: song.id) }
        catch { if wanted { favoriteIDs.remove(song.id) } else { favoriteIDs.insert(song.id) } }
    }

    /// Полная остановка и сброс (выход из аккаунта).
    public func stop() {
        engine.stop()
        queue = []; urlToSong = [:]; enqueuedIDs = []; current = nil; history = []; original = []
        endless = false; shuffleOn = false
        restoredPosition = nil; pendingSeek = nil; listenTask?.cancel()
        isPlaying = false
        state = .idle
        #if os(iOS)
        bridge?.update()
        #endif
    }

    // MARK: Внутреннее

    /// Пересобирает очередь с первого трека списка. История не трогается.
    /// `immediate` — интерфейс сразу показывает первый трек (кнопки «вперёд», «назад», переход по очереди), не дожидаясь его загрузки.
    private func rebuild(_ songs: [Song], immediate: Bool = false) {
        restoredPosition = nil
        suppressIdle = true
        // Намеренно без engine.stop(): AudioStreaming очищает очередь и текущий трек после stop() асинхронно, на своей очереди,
        // и этот запоздавший сброс стирал только что поставленные треки (плеер оставался пустым). play(url:) сам заменяет
        // всё, что играло и стояло в очереди.
        queue = []; urlToSong = [:]; queuedURL = [:]; enqueuedIDs = []; current = nil; playingURL = nil
        autoIDs.formIntersection(songs.map(\.id))
        #if os(iOS)
        bridge?.activateSession()
        #endif
        applyRepeat()
        try? start(songs)
        if immediate, let first = queue.first {
            current = first
            state = .active
            isPlaying = true
            if first.starred != nil { favoriteIDs.insert(first.id) }
            #if os(iOS)
            bridge?.update()
            bridge?.loadArtwork(for: first)
            #endif
        }
    }

    private func playbackURL(for song: Song) throws -> URL {
        if let direct = streamOverrides[song.id] { return direct }
        if isCasting { return try client.streamURL(for: song) }
        if let local = localURLProvider?(song) { return local }
        if let cached = cachedURLProvider?(song) { return cached }
        return try client.streamURL(for: song, quality: streamQuality)
    }

    /// Случайные песни: из загруженных в офлайн-режиме, иначе у сервера.
    private func randomSongs(count: Int) async throws -> [Song] {
        if let offline = offlineSongsProvider?() { return Array(offline.shuffled().prefix(count)) }
        return try await client.randomSongs(count: count, musicFolderId: libraryID)
    }

    private func start(_ songs: [Song]) throws {
        let first = songs[0]
        try register(first)
        engine.play(try playbackURL(for: first))
        for s in songs.dropFirst() { try? enqueue(s) }
    }

    private func register(_ song: Song) throws {
        let url = try playbackURL(for: song)
        urlToSong[url] = song
        queuedURL[song.id] = url
        enqueuedIDs.insert(song.id)
        queue.append(song)
    }

    private func enqueue(_ song: Song) throws {
        guard !enqueuedIDs.contains(song.id) else { return }
        try register(song)
        engine.enqueue(try playbackURL(for: song))
    }

    /// Заменяет всё, что после текущего трека.
    private func replaceUpcoming(with songs: [Song]) {
        if restoredPosition != nil {                          // восстановленная сессия: движок ещё не запущен
            queue = (current.map { [$0] } ?? []) + songs
            return
        }
        for song in queue.dropFirst() {
            if let url = queuedURL[song.id] { engine.dequeue(url); urlToSong[url] = nil; queuedURL[song.id] = nil }
            enqueuedIDs.remove(song.id)
        }
        queue = current.map { [$0] } ?? []
        autoIDs.formIntersection(songs.map(\.id))
        for s in songs { try? enqueue(s) }
    }

    /// Повтор одного трека делает движок; повтор списка — мы (см. extendForRepeat).
    private func applyRepeat() {
        let single = repeatMode == .one || (repeatMode == .all && !endless && original.count == 1)
        engine.setRepeatOne(single)
    }

    /// Повтор списка: когда остался последний трек, ставим в очередь следующий круг.
    private func extendForRepeat() {
        guard repeatMode == .all, !endless, !autoplay, !isRadio, original.count > 1, queue.count <= 1 else { return }
        let cycle = shuffleOn ? original.shuffled() : original
        for s in cycle { enqueuedIDs.remove(s.id) }
        for s in cycle { try? enqueue(s) }
    }

    private func trackStarted(_ url: URL) {
        guard let song = urlToSong[url] else { return }
        suppressIdle = false
        if let old = current, old != song { history.append(old) }
        current = song
        playingURL = url
        state = .active
        if song.starred != nil { favoriteIDs.insert(song.id) }
        if let i = queue.firstIndex(of: song) { queue.removeFirst(i) }
        extendForRepeat()
        onTrackChange?(song, upcoming)
        equalizer?.trackChanged(genre: song.genre)
        applyReplayGain()
        if sleepEndOfTrack { scheduleSleep(afterTrack: true) }
        if let p = pendingSeek { pendingSeek = nil; engine.seek(to: p) }       // докрутка до места после восстановления
        streamTitle = nil
        applyRate()
        if !spokenIDs.contains(song.id), !song.id.hasPrefix(Self.radioPrefix) { startListening(song) } else { listenTask?.cancel() }
        onSessionChange?()
        if endless, shuffleOn, queue.count <= refillThreshold { Task { await refill() } }
        if song.id.hasPrefix(Self.radioPrefix) { continuation = nil }
        if continuation != nil, queue.count <= refillThreshold { Task { await refillContinuation() } }
        else if autoplay, !endless, queue.count <= refillThreshold { Task { await refillMix() } }
        #if os(iOS)
        bridge?.update()
        bridge?.loadArtwork(for: song)
        #endif
    }

    private func engineStateChanged(_ s: EngineState) {
        // Пока грузится следующий трек, кнопка остаётся «Пауза», если музыка играла.
        isPlaying = (s == .playing) || (s == .loading && current != nil && isPlaying)
        switch s {
        case .playing, .paused: state = .active
        case .idle: if current == nil, !suppressIdle { state = .idle }
        case .loading: if current == nil { state = .loading }
        }
        if s == .playing || s == .paused { onSessionChange?() }
        #if os(iOS)
        bridge?.update()
        #endif
    }

    /// Смена медиатеки во время shuffle: текущий трек доигрывает, остальная очередь заменяется.
    private func rebuildUpcoming() async {
        let requested = libraryID
        guard let fresh = try? await randomSongs(count: batchSize),
              requested == libraryID, endless else { return }   // за время запроса выбор мог измениться
        replaceUpcoming(with: fresh)
    }

    private func dropAuto() {
        guard !autoIDs.isEmpty else { return }
        replaceUpcoming(with: manualUpcoming)
    }

    /// Новая подборка вместо текущей автоматической.
    public func refreshMix() {
        guard autoplay, state == .active else { return }
        replaceUpcoming(with: manualUpcoming)
        Task { await refillMix() }
    }

    /// Подборка ищется или ещё не найдена: для индикатора в очереди.
    public private(set) var mixLoading = false

    /// Выполняет запрос, но не ждёт дольше `seconds`: медленный внешний сервис (Last.fm, ListenBrainz) не должен вешать очередь.
    private func timed<T: Sendable>(_ seconds: Double, _ op: @escaping @Sendable () async throws -> T) async -> T? {
        await withTaskGroup(of: T?.self) { group in
            group.addTask { try? await op() }
            group.addTask { try? await Task.sleep(for: .seconds(seconds)); return nil }
            let first = await group.next() ?? nil
            group.cancelAll()
            return first
        }
    }

    /// Бесконечная очередь для любого Subsonic-сервера: похожие на последний трек → тот же жанр → случайные.
    /// Три запроса идут параллельно, медленный «похожий» ждём не дольше 4 секунд. Повторы и недавно сыгранное отбрасываются;
    /// альбомы и исполнители чередуются: два трека одного альбома подряд не ставим.
    private func refillMix() async {
        guard autoplay, !endless, !refilling, !isRadio, !isSpoken, let seed = queue.last ?? current else { return }
        refilling = true
        mixLoading = true
        defer { refilling = false; mixLoading = false }
        let client = self.client, folder = libraryID
        let offline = offlineSongsProvider?() != nil
        async let similar: [Song]? = offline ? nil : timed(4) { try await client.similarSongs(id: seed.id, count: 40) }
        async let sameGenre: [Song]? = (offline || seed.genre == nil) ? nil
            : timed(4) { try await client.randomSongs(count: 40, genre: seed.genre, musicFolderId: folder) }
        async let anyRandom: [Song]? = offline ? nil : timed(4) { try await client.randomSongs(count: 40, musicFolderId: folder) }
        var pool = (await similar) ?? []
        if pool.count < 15 { pool += (await sameGenre) ?? [] }
        if pool.count < 15 { pool += (await anyRandom) ?? [] }
        if pool.count < 15, offline { pool += (try? await randomSongs(count: 40)) ?? [] }
        guard autoplay, !endless else { return }
        let taken = Set(queue.map(\.id)).union(history.suffix(60).map(\.id))
        var seen = taken
        pool = pool.filter { seen.insert($0.id).inserted }
        let ordered = Self.spread(pool, after: queue.last, maxPerAlbum: 2, limit: 30)
        for s in ordered {
            if restoredPosition != nil { queue.append(s) } else { try? enqueue(s) }     // восстановленная сессия: движок ещё не запущен
            autoIDs.insert(s.id)
        }
        onSessionChange?()
    }

    /// Порядок без соседей из одного альбома или от одного исполнителя; не больше `maxPerAlbum` треков с альбома.
    nonisolated static func spread(_ pool: [Song], after last: Song?, maxPerAlbum: Int, limit: Int) -> [Song] {
        var rest = pool
        var out: [Song] = []
        var perAlbum: [String: Int] = [:]
        var prev = last
        while out.count < limit, !rest.isEmpty {
            let eligible = rest.indices.filter { i in
                guard let a = rest[i].albumId else { return true }
                return perAlbum[a, default: 0] < maxPerAlbum
            }
            func sameAlbum(_ i: Int) -> Bool { rest[i].albumId != nil && rest[i].albumId == prev?.albumId }
            func sameArtist(_ i: Int) -> Bool { rest[i].artist != nil && rest[i].artist == prev?.artist }
            // лучше всего другой альбом и другой исполнитель, иначе хотя бы другой альбом
            guard let i = eligible.first(where: { !sameAlbum($0) && !sameArtist($0) }) ?? eligible.first(where: { !sameAlbum($0) })
            else { break }                                    // остались только соседи по альбому — лучше недобрать, чем повторить подряд
            let s = rest.remove(at: i)
            out.append(s)
            if let a = s.albumId { perAlbum[a, default: 0] += 1 }
            prev = s
        }
        return out
    }

    private func refill() async {
        guard !refilling else { return }
        refilling = true
        defer { refilling = false }
        guard let more = try? await randomSongs(count: batchSize) else { return }
        for s in more { try? enqueue(s) }
    }
}
