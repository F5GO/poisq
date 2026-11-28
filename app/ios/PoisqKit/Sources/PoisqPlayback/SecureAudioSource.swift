import Foundation
import os
import AudioToolbox
import AudioStreaming
import PoisqCore

/// Источник аудио защищённого профиля: читает исходные байты трека кусками через шифрованный канал.
///
/// Батарея тратится в основном на радио, поэтому кусок крупный, а чтение идёт пачками с упреждением
/// до `readAhead`; дальше загрузка ждёт, пока плеер не разберёт буфер, и радио простаивает.
/// Первый кусок после старта и перемотки маленький — для быстрого начала звука.
/// Состояние меняется на `underlyingQueue` (очередь источников AudioPlayer); флаг паузы — атомарный,
/// потому что плеер вызывает `suspend()` с главного потока (пауза, перемотка).
///
/// Поведение повторяет сетевой источник AudioStreaming, которого ждёт плеер:
/// - перемотка (`seek`) снимает паузу: после неё плеер не вызывает `resume()`;
/// - первый кусок после старта или перемотки тоже снимает паузу: плеер просит перемотку сразу после старта,
///   откладывает её до разбора заголовка и без данных так и остался бы ждать;
/// - данные отдаются по одному срезу за проход очереди: пауза и перемотка плеера встают между срезами,
///   а не ждут всего буфера (иначе при остановленном выводе очередь блокировалась навсегда).
final class SecureAudioSource: NSObject, CoreAudioStreamSource, @unchecked Sendable {
    static let firstChunk = 256 << 10
    static let chunk = 4 << 20
    static let readAhead = 8 << 20
    static let slice = 64 << 10
    static let retryDelays: [Duration] = [.milliseconds(500), .seconds(1), .seconds(2)]

    weak var delegate: AudioStreamSourceDelegate?
    let underlyingQueue: DispatchQueue
    let audioFileHint: AudioFileTypeID

    private(set) var position = 0
    private(set) var length = 0

    private let url: URL
    private let fetcher: SecureMediaFetching
    private var generation = 0
    private let suspendedFlag = OSAllocatedUnfairLock(initialState: false)
    private var suspended: Bool {
        get { suspendedFlag.withLock { $0 } }
        set { suspendedFlag.withLock { $0 = newValue } }
    }
    private var deliveryScheduled = false
    private var firstChunkPending = true
    private var pending: [Data] = []
    private var pendingBytes = 0
    private var finishedFetching = false
    private var endReported = false
    private var task: Task<Void, Never>?
    private var roomWaiter: CheckedContinuation<Void, Never>?

    init(url: URL, fetcher: SecureMediaFetching, queue: DispatchQueue) {
        self.url = url
        self.fetcher = fetcher
        underlyingQueue = queue
        let suffix = URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems?.first { $0.name == "suffix" }?.value
        audioFileHint = Self.fileType(suffix: suffix)
    }

    deinit { task?.cancel() }

    func seek(at offset: Int) {
        reset()
        position = max(0, offset)
        if length > 0, position >= length {
            finishedFetching = true
            scheduleDelivery()
            return
        }
        startFetching(from: position, generation: generation)
    }

    func close() { reset() }

    func suspend() { suspended = true }

    func resume() {
        suspended = false
        underlyingQueue.async { self.scheduleDelivery() }
    }

    private func reset() {
        generation += 1
        task?.cancel()
        task = nil
        roomWaiter?.resume()
        roomWaiter = nil
        pending = []
        pendingBytes = 0
        finishedFetching = false
        endReported = false
        suspended = false
        firstChunkPending = true
    }

    private func startFetching(from offset: Int, generation gen: Int) {
        task = Task.detached(priority: .userInitiated) { [weak self, url, fetcher] in
            var cursor = offset
            var size = Self.firstChunk
            while !Task.isCancelled {
                guard let self else { return }
                await self.waitForRoom(generation: gen)
                guard !Task.isCancelled else { return }
                let chunk: SecureMediaChunk
                do {
                    chunk = try await Self.fetchWithRetry(fetcher, url: url, offset: cursor, length: size)
                } catch {
                    guard !Task.isCancelled else { return }
                    self.underlyingQueue.async {
                        guard gen == self.generation else { return }
                        self.delegate?.errorOccurred(source: self, error: error)
                    }
                    return
                }
                cursor += chunk.data.count
                let end = chunk.pastEnd || chunk.data.isEmpty || chunk.total.map { cursor >= $0 } ?? (chunk.data.count < size)
                self.underlyingQueue.async { self.received(chunk, end: end, generation: gen) }
                if end { return }
                size = Self.chunk
            }
        }
    }

    private static func fetchWithRetry(_ fetcher: SecureMediaFetching, url: URL, offset: Int, length: Int) async throws -> SecureMediaChunk {
        var attempt = 0
        while true {
            do {
                return try await fetcher.mediaChunk(for: url, offset: offset, length: length)
            } catch {
                // Отказ сервера в доступе или в чтении с места повтором не исправить.
                if let f = error as? SecureChannel.Failure, [.forbidden, .rangeUnsupported, .revoked, .notSignedIn].contains(f) { throw error }
                guard attempt < retryDelays.count, !Task.isCancelled else { throw error }
                try? await Task.sleep(for: retryDelays[attempt])
                attempt += 1
            }
        }
    }

    /// Ждёт, пока в буфере упреждения появится место; возвращается сразу после перемотки или закрытия.
    private func waitForRoom(generation gen: Int) async {
        await withCheckedContinuation { (cont: CheckedContinuation<Void, Never>) in
            underlyingQueue.async {
                if gen != self.generation || self.pendingBytes < Self.readAhead {
                    cont.resume()
                } else {
                    self.roomWaiter?.resume()
                    self.roomWaiter = cont
                }
            }
        }
    }

    private func received(_ chunk: SecureMediaChunk, end: Bool, generation gen: Int) {
        guard gen == generation else { return }
        if firstChunkPending {
            firstChunkPending = false
            suspended = false            // как ответ сервера у сетевого источника
        }
        if let total = chunk.total { length = total }
        if !chunk.data.isEmpty {
            pending.append(chunk.data)
            pendingBytes += chunk.data.count
        }
        if end { finishedFetching = true }
        scheduleDelivery()
    }

    /// Ставит отдачу следующего среза в очередь, если она ещё не стоит.
    private func scheduleDelivery() {
        guard !deliveryScheduled else { return }
        deliveryScheduled = true
        underlyingQueue.async {
            self.deliveryScheduled = false
            self.deliver()
        }
    }

    /// Отдаёт плееру один срез и уступает очередь; следующий срез — отдельным проходом.
    private func deliver() {
        if !suspended, !pending.isEmpty {
            let head = pending[0]
            let n = min(Self.slice, head.count)
            let slice = head.prefix(n)
            if n == head.count {
                pending.removeFirst()
            } else {
                pending[0] = head.dropFirst(n)
            }
            pendingBytes -= n
            position += n
            delegate?.dataAvailable(source: self, data: Data(slice))
        }
        if pendingBytes < Self.readAhead, let waiter = roomWaiter {
            roomWaiter = nil
            waiter.resume()
        }
        if !pending.isEmpty {
            if !suspended { scheduleDelivery() }
        } else if finishedFetching, !endReported, !suspended {
            endReported = true
            delegate?.endOfFileOccurred(source: self)
        }
    }

    static func fileType(suffix: String?) -> AudioFileTypeID {
        switch suffix?.lowercased() {
        case "mp3": kAudioFileMP3Type
        case "flac": kAudioFileFLACType
        case "m4a", "m4b", "mp4", "alac": kAudioFileM4AType
        case "aac": kAudioFileAAC_ADTSType
        case "wav": kAudioFileWAVEType
        case "aif", "aiff": kAudioFileAIFFType
        case "caf": kAudioFileCAFType
        default: 0
        }
    }
}
