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
    private var chain: Task<Void, Never> = Task {}     // команды идут строго по очереди: play → add → seek
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

    public func play(_ url: URL) {
        guard let id = Self.songID(url) else { onError?(SubsonicError.invalidURL); return }
        items = [url]; index = -1; currentURL = nil
        basePosition = 0; baseTime = Date()
        update(.loading)
        send(failPlayback: true) { [client, zone] in try await client.castPlay(zone: zone, ids: [id]) }
        startPolling()
    }

    public func enqueue(_ url: URL) {
        guard let id = Self.songID(url) else { return }
        items.append(url)
        send { [client, zone] in try await client.castControl(zone: zone, action: "add", ids: [id]) }
    }

    public func dequeue(_ url: URL) {
        guard let i = items.lastIndex(of: url), i > index else { return }
        items.remove(at: i)
        send { [client, zone] in try await client.castControl(zone: zone, action: "remove", position: i) }
    }

    public func skipToNext() {
        guard items.indices.contains(index + 1) else { return }
        send { [client, zone] in try await client.castControl(zone: zone, action: "next") }
    }

    public func seek(to seconds: Double) {
        basePosition = seconds; baseTime = Date()
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
        }
    }

    private func startPolling() {
        guard poll == nil else { return }
        poll = Task { [weak self, pollInterval] in
            while !Task.isCancelled {
                guard let self else { return }
                if self.pending == 0, let s = try? await self.client.castStatus(zone: self.zone), self.pending == 0 {
                    self.apply(s)
                }
                try? await Task.sleep(for: pollInterval)
            }
        }
    }

    /// Состояние колонки → зеркало и события для Player.
    func apply(_ s: SubsonicClient.CastStatus) {
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
