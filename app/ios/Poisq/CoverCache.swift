import UIKit
import CryptoKit

/// Кеш обложек: память + диск. Ключ не зависит от токена в URL, поэтому кеш переживает перезапуск.
actor CoverCache {
    private let memory = NSCache<NSString, UIImage>()
    private var inflight: [String: Task<UIImage?, Never>] = [:]
    private let dir: URL = {
        let d = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0].appending(path: "covers", directoryHint: .isDirectory)
        try? FileManager.default.createDirectory(at: d, withIntermediateDirectories: true)
        return d
    }()

    /// Свои HTTP-заголовки активного сервера.
    private var headers: [String: String] = [:]
    func setHeaders(_ h: [String: String]) { headers = h }

    init() { memory.countLimit = 200 }

    func image(key: String, url: @Sendable @escaping () throws -> URL) async -> UIImage? {
        if let hit = memory.object(forKey: key as NSString) { return hit }
        if let task = inflight[key] { return await task.value }
        let file = dir.appending(path: Self.fileName(key))
        let headers = self.headers
        let task = Task<UIImage?, Never> {
            if let data = try? Data(contentsOf: file), let img = UIImage(data: data) { return img }
            guard let u = try? url(), let (data, resp) = try? await URLSession.shared.data(for: { var r = URLRequest(url: u); headers.forEach { r.setValue($1, forHTTPHeaderField: $0) }; return r }()),
                  (resp as? HTTPURLResponse)?.statusCode == 200, let img = UIImage(data: data) else { return nil }
            try? data.write(to: file, options: .atomic)
            return img
        }
        inflight[key] = task
        let img = await task.value
        inflight[key] = nil
        if let img { memory.setObject(img, forKey: key as NSString) }
        return img
    }

    func clear() {
        memory.removeAllObjects()
        try? FileManager.default.removeItem(at: dir)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
    }

    private static func fileName(_ key: String) -> String {
        SHA256.hash(data: Data(key.utf8)).map { String(format: "%02x", $0) }.joined()
    }
}
