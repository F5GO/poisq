import Foundation
import CryptoKit
import PoisqCore

/// Кеш прослушивания: текущий и ближайшие треки докачиваются в файлы, чтобы повтор, перемотка и обрыв сети не мешали.
/// Хранится в Caches (система может очистить сама), общий размер ограничен: старейшие файлы удаляются первыми.
@MainActor @Observable
final class CacheManager {
    private(set) var sizeBytes: Int64 = 0
    private var inflight: Set<String> = []
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

    /// Подкачивает треки в кеш (уже скачанные пропускаются).
    /// Защищённый профиль качает так же, как офлайн-загрузки: билет из канала, шифротекст по нему, расшифровка в файл кеша.
    func prefetch(_ songs: [Song], client: SubsonicClient, serverID: UUID, quality: StreamQuality, isDownloaded: (Song) -> Bool) {
        for song in songs where !isDownloaded(song) {
            let dest = file(song, serverID, quality)
            let key = dest.path
            guard !FileManager.default.fileExists(atPath: key), !inflight.contains(key) else { continue }
            if let channel = client.channel {
                inflight.insert(key)
                Task { [session] in
                    defer { Task { @MainActor in self.inflight.remove(key) } }
                    guard let ticket = try? await channel.downloadTicket(songID: song.id),
                          let (tmp, response) = try? await session.download(for: URLRequest(url: ticket.url), delegate: RefuseRedirects.shared)
                    else { return }
                    defer { try? FileManager.default.removeItem(at: tmp) }
                    guard (response as? HTTPURLResponse)?.statusCode == 200 else { return }
                    let ok = await Task.detached(priority: .utility) {
                        Self.store(dest) { try SecureBlob.decrypt(from: tmp, to: $0, key: ticket.key, size: ticket.size, chunkSize: ticket.chunkSize) }
                    }.value
                    if ok { self.trim() }
                }
                continue
            }
            guard let remote = try? client.fileURL(for: song, quality: quality) else { continue }
            inflight.insert(key)
            let request = client.request(for: remote)
            Task { [session] in
                defer { Task { @MainActor in self.inflight.remove(key) } }
                guard let (tmp, response) = try? await session.download(for: request),
                      let http = response as? HTTPURLResponse, http.statusCode == 200 else { return }
                let ct = (http.value(forHTTPHeaderField: "Content-Type") ?? "").lowercased()
                if ct.contains("json") || ct.contains("xml") || ct.contains("html") {
                    try? FileManager.default.removeItem(at: tmp)
                    return
                }
                if Self.store(dest, { try FileManager.default.moveItem(at: tmp, to: $0) }) { self.trim() }
            }
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
            await MainActor.run { self.sizeBytes = total }
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

/// Шифротекст по билету берётся только с адреса билета: перенаправление считается ошибкой, как в загрузках.
private final class RefuseRedirects: NSObject, URLSessionTaskDelegate {
    static let shared = RefuseRedirects()
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest) async -> URLRequest? { nil }
}
