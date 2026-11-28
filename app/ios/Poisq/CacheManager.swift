import Foundation
import CryptoKit
import PoisqCore

/// Кеш прослушивания: текущий и ближайшие треки докачиваются в файлы, чтобы повтор, перемотка и обрыв сети не мешали.
/// Хранится в Caches (система может очистить сама), общий размер ограничен: старейшие файлы удаляются первыми.
@MainActor @Observable
final class CacheManager {
    private(set) var sizeBytes: Int64 = 0
    private var inflight: Set<String> = []
    /// Доля скачанного у файлов, которые качаются сейчас (ключ — путь файла кеша); для полосы загрузки в плеере.
    private(set) var progress: [String: Double] = [:]
    /// Ответы «файл готов / файла нет» по пути: полоса плеера спрашивает каждую секунду, диск трогаем один раз.
    /// Сбрасывается при записи и очистке кеша.
    @ObservationIgnored private var presence: [String: Bool] = [:]
    private let root: URL
    private let session: URLSession = {
        let c = URLSessionConfiguration.default
        c.timeoutIntervalForRequest = 30
        return URLSession(configuration: c)
    }()
    private static let limitKey = "cacheLimitMB"

    /// Предел размера в мегабайтах (по умолчанию 1 ГБ).
    var limitMB: Int {
        get { UserDefaults.standard.object(forKey: Self.limitKey) as? Int ?? 1024 }
        set { UserDefaults.standard.set(newValue, forKey: Self.limitKey); trim() }
    }

    init() {
        root = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0].appending(path: "StreamCache", directoryHint: .isDirectory)
        try? FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        refreshSize()
    }

    /// Файлы разного качества лежат отдельно: смена качества не подсовывает старые версии.
    private func file(_ song: Song, _ serverID: UUID, _ quality: StreamQuality) -> URL {
        let rawExt = quality.fileExtension ?? (song.suffix ?? "")
        let ext = String(rawExt.filter { $0.isASCII && ($0.isLetter || $0.isNumber) })
        let safeExt = ext.isEmpty ? "audio" : ext
        let safeID = Self.safeID(song.id)
        return root.appending(path: serverID.uuidString, directoryHint: .isDirectory).appending(path: "\(safeID)-\(quality.tag).\(safeExt)")
    }

    private static func safeID(_ raw: String) -> String {
        let clean = String(raw.map { ($0.isASCII && ($0.isLetter || $0.isNumber || $0 == "_" || $0 == "-")) ? $0 : "_" })
        if clean == raw, !clean.isEmpty, !clean.hasPrefix("."), clean.count <= 80 {
            return clean
        }
        let digest = SHA256.hash(data: Data(raw.utf8)).prefix(8).map { String(format: "%02x", $0) }.joined()
        return "\(clean.prefix(40))_\(digest)"
    }

    /// Файл из кеша, если он целиком скачан. Обращение освежает «возраст» файла, чтобы он не уходил в очистку первым.
    func url(for song: Song, serverID: UUID, quality: StreamQuality) -> URL? {
        let url = file(song, serverID, quality)
        guard FileManager.default.fileExists(atPath: url.path) else { return nil }
        try? FileManager.default.setAttributes([.modificationDate: Date()], ofItemAtPath: url.path)
        return url
    }

    /// Сколько трека уже лежит в кеше: 1 — файл готов, nil — не качается и не скачан. Не трогает «возраст» файла.
    func fraction(for song: Song, serverID: UUID, quality: StreamQuality) -> Double? {
        let path = file(song, serverID, quality).path
        if let p = progress[path] { return p }
        if let known = presence[path] { return known ? 1 : nil }
        let exists = FileManager.default.fileExists(atPath: path)
        presence[path] = exists
        return exists ? 1 : nil
    }

    /// Обновляет долю скачанного не чаще, чем на 2 %: плееру хватает, а лишних перерисовок нет.
    private func report(_ key: String, _ value: Double) {
        guard inflight.contains(key) else { return }
        if let old = progress[key], value < 1, value - old < 0.02 { return }
        progress[key] = min(max(value, 0), 1)
    }

    private func finish(_ key: String) {
        inflight.remove(key)
        presence[key] = nil          // файл мог появиться — следующий запрос проверит диск
        progress[key] = nil
    }

    /// Очередь подкачки: качается один файл за раз, чтобы не отнимать канал у играющего потока
    /// и не держать радио под несколькими загрузками сразу. Новый список заменяет ещё не начатые файлы.
    private var queued: [(key: String, run: () async -> Void)] = []
    private var running = false

    /// Подкачивает треки в кеш по порядку (уже скачанные пропускаются).
    /// Защищённый профиль качает так же, как офлайн-загрузки: билет из канала, шифротекст по нему, расшифровка в файл кеша.
    func prefetch(_ songs: [Song], client: SubsonicClient, serverID: UUID, quality: StreamQuality, isDownloaded: (Song) -> Bool) {
        queued = songs.compactMap { song -> (key: String, run: () async -> Void)? in
            guard !isDownloaded(song) else { return nil }
            let dest = file(song, serverID, quality)
            let key = dest.path
            guard !FileManager.default.fileExists(atPath: key), !inflight.contains(key) else { return nil }
            if let channel = client.channel {
                return (key, { [session] in
                    guard let ticket = try? await channel.downloadTicket(songID: song.id) else { return }
                    let watcher = DownloadWatcher(refuseRedirects: true) { value in Task { @MainActor in self.report(key, value) } }
                    guard let (tmp, response) = try? await session.download(for: URLRequest(url: ticket.url), delegate: watcher)
                    else { return }
                    defer { try? FileManager.default.removeItem(at: tmp) }
                    guard (response as? HTTPURLResponse)?.statusCode == 200 else { return }
                    let ok = await Task.detached(priority: .utility) {
                        Self.store(dest) { try SecureBlob.decrypt(from: tmp, to: $0, key: ticket.key, size: ticket.size, chunkSize: ticket.chunkSize) }
                    }.value
                    if ok { self.trim() }
                })
            }
            guard let remote = try? client.fileURL(for: song, quality: quality) else { return nil }
            let request = client.request(for: remote)
            return (key, { [session] in
                let watcher = DownloadWatcher(refuseRedirects: false) { value in Task { @MainActor in self.report(key, value) } }
                guard let (tmp, response) = try? await session.download(for: request, delegate: watcher),
                      let http = response as? HTTPURLResponse, http.statusCode == 200 else { return }
                let ct = (http.value(forHTTPHeaderField: "Content-Type") ?? "").lowercased()
                if ct.contains("json") || ct.contains("xml") || ct.contains("html") {
                    try? FileManager.default.removeItem(at: tmp)
                    return
                }
                if Self.store(dest, { try FileManager.default.moveItem(at: tmp, to: $0) }) { self.trim() }
            })
        }
        pump()
    }

    private func pump() {
        guard !running, !queued.isEmpty else { return }
        let job = queued.removeFirst()
        running = true
        inflight.insert(job.key)
        progress[job.key] = 0
        Task {
            await job.run()
            finish(job.key)
            running = false
            pump()
        }
    }

    /// Пишет файл кеша через временное имя: наполовину записанный файл никогда не выглядит готовым для `url(for:)`.
    nonisolated private static func store(_ dest: URL, _ write: (URL) throws -> Void) -> Bool {
        let partial = dest.appendingPathExtension("partial")
        do {
            try FileManager.default.createDirectory(at: dest.deletingLastPathComponent(), withIntermediateDirectories: true)
            try? FileManager.default.removeItem(at: partial)
            try write(partial)
            try? FileManager.default.removeItem(at: dest)
            try FileManager.default.moveItem(at: partial, to: dest)
            return true
        } catch {
            try? FileManager.default.removeItem(at: partial)
            return false
        }
    }

    func clear(serverID: UUID? = nil) {
        presence = [:]
        let target = serverID.map { root.appending(path: $0.uuidString) } ?? root
        try? FileManager.default.removeItem(at: target)
        try? FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        refreshSize()
    }

    /// Удаляет самые давние файлы, пока кеш не уложится в предел.
    func trim() {
        let limit = Int64(limitMB) * 1_048_576
        let root = self.root
        Task.detached(priority: .utility) {
            let entries = Self.allFiles(in: root)
            var total = entries.reduce(Int64(0)) { $0 + $1.size }
            for e in entries.sorted(by: { $0.date < $1.date }) where total > limit {
                try? FileManager.default.removeItem(at: e.url)
                total -= e.size
            }
            await MainActor.run { self.sizeBytes = total; self.presence = [:] }
        }
    }

    func refreshSize() {
        let root = self.root
        Task.detached(priority: .utility) {
            let total = Self.allFiles(in: root).reduce(Int64(0)) { $0 + $1.size }
            await MainActor.run { self.sizeBytes = total }
        }
    }

    private struct Entry { let url: URL; let size: Int64; let date: Date }

    nonisolated private static func allFiles(in root: URL) -> [Entry] {
        guard let e = FileManager.default.enumerator(at: root, includingPropertiesForKeys: [.fileSizeKey, .contentModificationDateKey, .isRegularFileKey]) else { return [] }
        return e.compactMap { item -> Entry? in
            guard let url = item as? URL,
                  let v = try? url.resourceValues(forKeys: [.fileSizeKey, .contentModificationDateKey, .isRegularFileKey]),
                  v.isRegularFile == true else { return nil }
            return Entry(url: url, size: Int64(v.fileSize ?? 0), date: v.contentModificationDate ?? .distantPast)
        }
    }
}

/// Следит за загрузкой одного файла: доля скачанного (по прогрессу задачи) и, для шифротекста по билету,
/// отказ от перенаправлений — шифротекст берётся только с адреса билета, как в загрузках.
private final class DownloadWatcher: NSObject, URLSessionTaskDelegate, @unchecked Sendable {
    private let refuseRedirects: Bool
    private let onProgress: @Sendable (Double) -> Void
    private var observation: NSKeyValueObservation?

    init(refuseRedirects: Bool, onProgress: @escaping @Sendable (Double) -> Void) {
        self.refuseRedirects = refuseRedirects
        self.onProgress = onProgress
    }

    deinit { observation?.invalidate() }

    func urlSession(_ session: URLSession, didCreateTask task: URLSessionTask) {
        task.priority = URLSessionTask.lowPriority     // подкачка уступает запросам интерфейса и потока
        let callback = onProgress
        observation = task.progress.observe(\.fractionCompleted) { progress, _ in callback(progress.fractionCompleted) }
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest) async -> URLRequest? {
        refuseRedirects ? nil : request
    }
}
