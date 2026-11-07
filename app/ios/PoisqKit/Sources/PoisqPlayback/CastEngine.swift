import Foundation
import PoisqCore

/// Движок-пульт: звук играет на колонке Music Assistant через прокси poisq (`poisqCast*`), телефон только управляет.
/// Очередью по-прежнему ведает Player: движок получает текущую и следующую песню, как обычный движок для gapless.
/// Песню узнаём по `id` в ссылке на стрим, поэтому Player в этом режиме всегда отдаёт ссылки сервера, а не файлы с устройства.
@MainActor
public final class CastEngine: PlaybackEngine {
    private let client: SubsonicClient
    public let zone: String
    public private(set) var state: EngineState = .idle
    public private(set) var currentURL: URL?

    public var onTrackStarted: ((URL) -> Void)?
    public var onStateChanged: ((EngineState) -> Void)?
    public var onError: ((Error) -> Void)?

    /// Зеркало очереди колонки: индексы совпадают с индексами очереди MA.
    private var items: [URL] = []
    private var index = -1
    private var basePosition = 0.0
    private var baseTime = Date()
    private var chain: Task<Void, Never> = Task {}     // команды идут строго по очереди: запуск → add → seek
    private var pending = 0                            // пока команды в пути, ответы опроса устарели
    private var poll: Task<Void, Never>?
    private let pollInterval: Duration

    public init(client: SubsonicClient, zone: String, pollInterval: Duration = .seconds(1)) {
        self.client = client
        self.zone = zone
        self.pollInterval = pollInterval
    }

    public var progress: Double { state == .playing ? basePosition + Date().timeIntervalSince(baseTime) : basePosition }
    public var duration: Double { 0 }                 // длину Player берёт из метаданных песни
    // shortcut: громкость колонки — ползунком в окне колонок; эквалайзер, ReplayGain, повтор трека и затухание таймера сна на колонке не работают.
    public var volume: Float = 1
    public var requestHeaders: [String: String] = [:]
    public func setEqualizer(_ config: EQConfig) {}
    public func setReplayGain(_ db: Float) {}
    public func setRepeatOne(_ on: Bool) {}

    static func songID(_ url: URL) -> String? {
        URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems?.first { $0.name == "id" }?.value
    }

    /// Запуск, ещё не отправленный на колонку. Пока он ждёт своей очереди, новые play/enqueue правят его, а не копят команды:
    /// Player на каждое «вперёд» запускает песню и заново ставит весь хвост очереди — это одна команда, а не по запросу на песню.
    private struct Start { var urls: [URL]; var seek: Int? }
    private var start: Start?
    private var adds: [URL]?                             // так же склеиваем добавления в конец
    private var expected: Int?                           // после запуска MA не сразу сообщает новый индекс: до тех пор опрос устарел

    public func play(_ url: URL) {
        guard Self.songID(url) != nil else { onError?(SubsonicError.invalidURL); return }
        basePosition = 0; baseTime = Date()
        update(.loading)
        adds = nil                                     // очередь всё равно заменится
        if start == nil { send(failPlayback: true) { [weak self] in try await self?.sendStart() } }
        start = Start(urls: [url])
        startPolling()
    }

    public func enqueue(_ url: URL) {
        guard Self.songID(url) != nil else { return }
        if start != nil { start?.urls.append(url); return }
        if adds == nil { adds = []; send { [weak self] in try await self?.sendAdds() } }
        adds?.append(url)
    }

    public func dequeue(_ url: URL) {
        if let i = start?.urls.lastIndex(of: url), i > 0 { start?.urls.remove(at: i); return }
        if start != nil { return }                     // песня из старой очереди, её и так заменит запуск
        if let i = adds?.lastIndex(of: url) { adds?.remove(at: i); return }
        guard let i = items.lastIndex(of: url), i > index else { return }
        items.remove(at: i)
        send { [client, zone] in try await client.castControl(zone: zone, action: "remove", position: i) }
    }

    /// Если новая очередь — хвост той, что уже на колонке (вперёд, назад, переход по очереди), просто переходим к песне:
    /// MA не пересобирает очередь и не перечитывает файлы. Иначе заменяем очередь целиком.
    private func sendStart() async throws {
        guard let s = start else { return }
        start = nil
        let ids = s.urls.compactMap(Self.songID)
        let k = items.count - ids.count
        index = -1; currentURL = nil                   // опрос сообщит о запуске, как о смене трека
        do {
            if k >= 0, items[k...].map(Self.songID) == ids.map(Optional.some) {
                items.replaceSubrange(k..., with: s.urls)  // ссылки новые (свежая подпись), песни те же
                expected = k
                try await client.castControl(zone: zone, action: "index", position: k)
                if let seek = s.seek { try await client.castControl(zone: zone, action: "seek", position: seek) }
            } else {
                items = s.urls
                expected = 0
                try await client.castPlay(zone: zone, ids: ids, offset: s.seek ?? 0)
            }
        } catch { expected = nil; throw error }
    }

    private func sendAdds() async throws {
        guard let urls = adds, !urls.isEmpty else { adds = nil; return }
        adds = nil
        items += urls
        try await client.castControl(zone: zone, action: "add", ids: urls.compactMap(Self.songID))
    }

    public func skipToNext() {
        guard items.indices.contains(index + 1) else { return }
        send { [client, zone] in try await client.castControl(zone: zone, action: "next") }
    }

    public func seek(to seconds: Double) {
        basePosition = seconds; baseTime = Date()
        if start != nil { start?.seek = Int(seconds); return }
        send { [client, zone] in try await client.castControl(zone: zone, action: "seek", position: Int(seconds)) }
    }

    public func pause() {
        basePosition = progress; baseTime = Date()
        update(.paused)
        send { [client, zone] in try await client.castControl(zone: zone, action: "pause") }
    }

    public func resume() {
        baseTime = Date()
        update(.playing)
        send { [client, zone] in try await client.castControl(zone: zone, action: "play") }
    }

    public func stop() {
        poll?.cancel(); poll = nil
        items = []; index = -1; currentURL = nil; basePosition = 0
        start = nil; adds = nil; expected = nil
        update(.idle)
        send { [client, zone] in
            try await client.castControl(zone: zone, action: "stop")
            try await client.castControl(zone: zone, action: "clear")
        }
    }

    /// Команды выполняются по одной. Ошибка запуска песни — для Player это сбой воспроизведения; остальные не ломают очередь.
    private func send(failPlayback: Bool = false, _ body: @escaping @Sendable () async throws -> Void) {
        pending += 1
        let previous = chain
        chain = Task { [weak self] in
            await previous.value
            do { try await body() } catch { if failPlayback { self?.onError?(error) } }
            self?.pending -= 1
            if self?.pending == 0 { await self?.refresh() }   // команды отработали — сразу узнаём состояние, не ждём такта опроса
        }
    }

    private func startPolling() {
        guard poll == nil else { return }
        poll = Task { [weak self, pollInterval] in
            while !Task.isCancelled {
                guard let self else { return }
                await self.refresh()
                try? await Task.sleep(for: pollInterval)
            }
        }
    }

    private func refresh() async {
        guard pending == 0, let s = try? await client.castStatus(zone: zone), pending == 0 else { return }
        apply(s)
    }

    /// Состояние колонки → зеркало и события для Player.
    func apply(_ s: SubsonicClient.CastStatus) {
        if let e = expected { guard s.index == e else { return }; expected = nil }
        basePosition = s.position; baseTime = Date()
        if s.index != index, items.indices.contains(s.index) {
            index = s.index
            currentURL = items[s.index]
            onTrackStarted?(items[s.index])
        }
        switch s.state {
        case "playing": update(.playing)
        case "paused": update(.paused)
        default: if index >= 0 { update(.idle) }      // очередь доиграла
        }
    }

    private func update(_ new: EngineState) {
        guard new != state else { return }
        state = new
        onStateChanged?(new)
    }
}
