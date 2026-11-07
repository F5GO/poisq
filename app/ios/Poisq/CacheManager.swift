import Foundation
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
        let ext = quality.fileExtension ?? (song.suffix?.isEmpty == false ? song.suffix! : "audio")
        return root.appending(path: serverID.uuidString, directoryHint: .isDirectory).appending(path: "\(song.id)-\(quality.tag).\(ext)")
    }

    /// Файл из кеша, если он целиком скачан. Обращение освежает «возраст» файла, чтобы он не уходил в очистку первым.
    func url(for song: Song, serverID: UUID, quality: StreamQuality) -> URL? {
        let url = file(song, serverID, quality)
        guard FileManager.default.fileExists(atPath: url.path) else { return nil }
        try? FileManager.default.setAttributes([.modificationDate: Date()], ofItemAtPath: url.path)
        return url
    }

    /// Подкачивает треки в кеш (по одному за раз; уже скачанные пропускаются).
    func prefetch(_ songs: [Song], client: SubsonicClient, serverID: UUID, quality: StreamQuality, isDownloaded: (Song) -> Bool) {
        for song in songs where !isDownloaded(song) {
            let dest = file(song, serverID, quality)
            let key = dest.path
            guard !FileManager.default.fileExists(atPath: key), !inflight.contains(key),
                  let remote = try? client.fileURL(for: song, quality: quality) else { continue }
            inflight.insert(key)
            let request = client.request(for: remote)
            Task { [session] in
                defer { Task { @MainActor in self.inflight.remove(key) } }
                guard let (tmp, response) = try? await session.download(for: request),
                      (response as? HTTPURLResponse)?.statusCode == 200 else { return }
                try? FileManager.default.createDirectory(at: dest.deletingLastPathComponent(), withIntermediateDirectories: true)
                try? FileManager.default.removeItem(at: dest)
                if (try? FileManager.default.moveItem(at: tmp, to: dest)) != nil {
                    await MainActor.run { self.trim() }
                }
            }
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
