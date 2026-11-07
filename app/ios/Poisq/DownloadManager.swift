import Foundation
import UIKit
import PoisqCore

struct DownloadedTrack: Codable, Identifiable, Hashable {
    let song: Song
    let serverID: UUID
    /// Путь относительно папки загрузок (папка приложения может переехать при обновлении).
    let relativePath: String
    let bytes: Int64
    /// Качество, в котором трек скачан (kbps; 0 — оригинал). nil у загрузок из прошлых версий — это оригинал.
    var qualityKbps: Int? = nil
    var id: String { Self.key(serverID, song.id) }
    static func key(_ server: UUID, _ songID: String) -> String { "\(server.uuidString)|\(songID)" }
}

/// Загрузки на устройство. Скачивание идёт фоновой сессией: продолжается, когда приложение свернуто.
@MainActor @Observable
final class DownloadManager: NSObject {
    private(set) var tracks: [String: DownloadedTrack] = [:]
    /// Прогресс активных загрузок 0…1 по ключу трека.
    private(set) var active: [String: Double] = [:]
    /// Ключи загрузок, которые закончились ошибкой: показываем «повторить».
    private(set) var failedKeys: Set<String> = []
    /// Разрешить скачивание по мобильной сети. Иначе загрузка ждёт Wi-Fi.
    var allowsCellular = false
    /// Предел занятого места под загрузки, в байтах; nil — без ограничений.
    var limitBytes: Int64?

    private let root: URL
    private let indexURL: URL
    private let pending = PendingStore()
    private var session: URLSession!
    /// Система передаёт это при пробуждении приложения в фоне; вызываем, когда все события обработаны.
    nonisolated(unsafe) static var backgroundCompletion: (() -> Void)?

    override init() {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        root = base.appending(path: "Downloads", directoryHint: .isDirectory)
        indexURL = base.appending(path: "downloads-index.json")
        super.init()
        try? FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        var values = URLResourceValues(); values.isExcludedFromBackup = true      // большие файлы не нужны в iCloud-копии
        var rootCopy = root
        try? rootCopy.setResourceValues(values)
        loadIndex()
        let config = URLSessionConfiguration.background(withIdentifier: "app.poisq.ios.downloads")
        config.sessionSendsLaunchEvents = true
        config.isDiscretionary = false
        session = URLSession(configuration: config, delegate: self, delegateQueue: nil)
        session.getAllTasks { tasks in
            // Задачи, пережившие перезапуск приложения, снова показываем как активные.
            Task { @MainActor in for t in tasks { if let k = t.taskDescription { self.active[k] = self.active[k] ?? 0 } } }
        }
    }

    // MARK: Запросы состояния

    func localURL(for song: Song, serverID: UUID) -> URL? {
        guard let t = tracks[DownloadedTrack.key(serverID, song.id)] else { return nil }
        let url = root.appending(path: t.relativePath)
        return FileManager.default.fileExists(atPath: url.path) ? url : nil
    }

    func hasFailed(_ song: Song, serverID: UUID) -> Bool { failedKeys.contains(DownloadedTrack.key(serverID, song.id)) }

    /// Идущие сейчас загрузки с прогрессом (для списка в «Загружено»).
    func activeDownloads(serverID: UUID) -> [(song: Song, progress: Double)] {
        active.compactMap { key, p in
            guard key.hasPrefix(serverID.uuidString + "|"), let item = pending.get(key) else { return nil }
            return (item.song, p)
        }.sorted { $0.song.title < $1.song.title }
    }

    /// Размер будущего файла: для оригинала — из метаданных, для перекодированного — по битрейту и длительности.
    static func estimatedSize(of song: Song, quality: StreamQuality) -> Int64 {
        guard let rate = quality.maxBitRate, let duration = song.duration else { return Int64(song.size ?? 0) }
        return Int64(duration) * Int64(rate) * 125          // кбит/с → байт/с = ×125
    }

    /// В каком качестве скачан трек.
    func quality(of song: Song, serverID: UUID) -> StreamQuality {
        tracks[DownloadedTrack.key(serverID, song.id)].flatMap { $0.qualityKbps }.flatMap(StreamQuality.init(rawValue:)) ?? .original
    }

    func isDownloaded(_ song: Song, serverID: UUID) -> Bool { tracks[DownloadedTrack.key(serverID, song.id)] != nil }

    func progress(for song: Song, serverID: UUID) -> Double? { active[DownloadedTrack.key(serverID, song.id)] }

    func songs(serverID: UUID) -> [Song] {
        tracks.values.filter { $0.serverID == serverID }.map(\.song)
            .sorted { ($0.album ?? "", $0.track ?? 0) < ($1.album ?? "", $1.track ?? 0) }
    }

    func totalBytes(serverID: UUID? = nil) -> Int64 {
        tracks.values.filter { serverID == nil || $0.serverID == serverID }.reduce(0) { $0 + $1.bytes }
    }

    // MARK: Действия

    /// Ставит треки в очередь. Возвращает, сколько треков не поместилось в лимит и осталось нескачанными.
    @discardableResult
    func download(_ songs: [Song], client: SubsonicClient, serverID: UUID, quality: StreamQuality) -> Int {
        // Занято = уже скачанное + то, что скачивается сейчас (по размеру из метаданных сервера).
        var used = totalBytes() + active.keys.reduce(Int64(0)) { $0 + (pending.get($1).map { Self.estimatedSize(of: $0.song, quality: $0.quality) } ?? 0) }
        var skipped = 0
        for song in songs {
            let key = DownloadedTrack.key(serverID, song.id)
            guard tracks[key] == nil, active[key] == nil, let url = try? client.fileURL(for: song, quality: quality) else { continue }
            let size = Self.estimatedSize(of: song, quality: quality)
            if let limit = limitBytes, used + size > limit { skipped += 1; continue }
            used += size
            pending.set(key, song, quality: quality)
            var request = client.request(for: url)
            request.allowsCellularAccess = allowsCellular
            failedKeys.remove(key)
            let task = session.downloadTask(with: request)
            task.taskDescription = key
            active[key] = 0
            task.resume()
        }
        return skipped
    }

    func remove(_ songs: [Song], serverID: UUID) {
        for song in songs {
            let key = DownloadedTrack.key(serverID, song.id)
            if let t = tracks.removeValue(forKey: key) { try? FileManager.default.removeItem(at: root.appending(path: t.relativePath)) }
            if active[key] != nil {
                session.getAllTasks { tasks in tasks.first { $0.taskDescription == key }?.cancel() }
                active[key] = nil; pending.remove(key)
            }
        }
        saveIndex()
    }

    /// Удаляет все загрузки (всего приложения или одного сервера) и отменяет активные.
    func removeAll(serverID: UUID? = nil) {
        let prefix = serverID.map { $0.uuidString + "|" }
        for t in tracks.values where prefix == nil || t.id.hasPrefix(prefix!) {
            try? FileManager.default.removeItem(at: root.appending(path: t.relativePath))
            tracks[t.id] = nil
        }
        for key in active.keys where prefix == nil || key.hasPrefix(prefix!) { active[key] = nil; pending.remove(key) }
        failedKeys = failedKeys.filter { prefix != nil && !$0.hasPrefix(prefix!) }
        session.getAllTasks { tasks in tasks.filter { t in prefix == nil || (t.taskDescription ?? "").hasPrefix(prefix!) }.forEach { $0.cancel() } }
        if let serverID { try? FileManager.default.removeItem(at: root.appending(path: serverID.uuidString)) }
        saveIndex()
    }

    // MARK: Хранение

    private func loadIndex() {
        guard let data = try? Data(contentsOf: indexURL), let list = try? JSONDecoder().decode([DownloadedTrack].self, from: data) else { return }
        tracks = Dictionary(uniqueKeysWithValues: list.map { ($0.id, $0) })
    }

    private func saveIndex() {
        try? JSONEncoder().encode(Array(tracks.values)).write(to: indexURL, options: .atomic)
    }

    fileprivate func finished(key: String, track: DownloadedTrack) {
        tracks[key] = track; active[key] = nil; pending.remove(key); saveIndex()
    }
    fileprivate func failed(key: String) { active[key] = nil; pending.remove(key); failedKeys.insert(key) }
    fileprivate func updated(key: String, progress: Double) { if active[key] != nil { active[key] = progress } }
    fileprivate var rootURL: URL { root }
}

extension DownloadManager: URLSessionDownloadDelegate {
    nonisolated func urlSession(_ session: URLSession, downloadTask: URLSessionDownloadTask, didWriteData _: Int64,
                                totalBytesWritten: Int64, totalBytesExpectedToWrite: Int64) {
        guard let key = downloadTask.taskDescription, totalBytesExpectedToWrite > 0 else { return }
        let p = Double(totalBytesWritten) / Double(totalBytesExpectedToWrite)
        Task { @MainActor in self.updated(key: key, progress: p) }
    }

    /// Файл надо переместить прямо здесь: после выхода из метода система его удаляет.
    nonisolated func urlSession(_ session: URLSession, downloadTask: URLSessionDownloadTask, didFinishDownloadingTo location: URL) {
        guard let key = downloadTask.taskDescription, let item = pending.get(key),
              let http = downloadTask.response as? HTTPURLResponse, http.statusCode == 200,
              let serverID = UUID(uuidString: String(key.split(separator: "|").first ?? "")) else {
            if let key = downloadTask.taskDescription { Task { @MainActor in self.failed(key: key) } }
            return
        }
        let song = item.song
        let ext = item.quality.fileExtension ?? (song.suffix?.isEmpty == false ? song.suffix! : "audio")
        let rel = "\(serverID.uuidString)/\(song.id).\(ext)"
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0].appending(path: "Downloads", directoryHint: .isDirectory)
        let dest = base.appending(path: rel)
        do {
            try FileManager.default.createDirectory(at: dest.deletingLastPathComponent(), withIntermediateDirectories: true)
            try? FileManager.default.removeItem(at: dest)
            try FileManager.default.moveItem(at: location, to: dest)
            let bytes = (try? FileManager.default.attributesOfItem(atPath: dest.path)[.size] as? Int64) ?? 0
            let track = DownloadedTrack(song: song, serverID: serverID, relativePath: rel, bytes: bytes, qualityKbps: item.quality.rawValue)
            Task { @MainActor in self.finished(key: key, track: track) }
        } catch {
            Task { @MainActor in self.failed(key: key) }
        }
    }

    nonisolated func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
        if error != nil, let key = task.taskDescription { Task { @MainActor in self.failed(key: key) } }
    }

    nonisolated func urlSessionDidFinishEvents(forBackgroundURLSession session: URLSession) {
        Task { @MainActor in
            DownloadManager.backgroundCompletion?()
            DownloadManager.backgroundCompletion = nil
        }
    }
}

/// Метаданные загружаемых треков: нужны делегату, который может сработать после перезапуска приложения.
final class PendingStore: @unchecked Sendable {
    struct Item: Codable { let song: Song; let qualityKbps: Int
        var quality: StreamQuality { StreamQuality(rawValue: qualityKbps) ?? .original }
    }
    private let lock = NSLock()
    private var items: [String: Item] = [:]
    private let url = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0].appending(path: "downloads-pending.json")

    init() {
        if let data = try? Data(contentsOf: url), let dict = try? JSONDecoder().decode([String: Item].self, from: data) { items = dict }
    }
    func set(_ key: String, _ song: Song, quality: StreamQuality) { lock.withLock { items[key] = Item(song: song, qualityKbps: quality.rawValue); persist() } }
    func get(_ key: String) -> (song: Song, quality: StreamQuality)? { lock.withLock { items[key].map { ($0.song, $0.quality) } } }
    func remove(_ key: String) { lock.withLock { items[key] = nil; persist() } }
    private func persist() { try? JSONEncoder().encode(items).write(to: url, options: .atomic) }
}

/// Нужен, чтобы система могла разбудить приложение и дозавершить фоновые загрузки.
final class AppDelegate: NSObject, UIApplicationDelegate {
    func application(_ application: UIApplication, handleEventsForBackgroundURLSession identifier: String, completionHandler: @escaping () -> Void) {
        DownloadManager.backgroundCompletion = completionHandler
    }
}
