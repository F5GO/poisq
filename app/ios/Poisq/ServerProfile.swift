import Foundation

/// Сохранённый сервер. Пароль и значения заголовков здесь не хранятся — они в Keychain под ключом `id`.
struct ServerProfile: Identifiable, Codable, Hashable {
    var id = UUID()
    var name: String
    var url: URL
    var username: String
    /// Выбранная медиатека на этом сервере; nil — все.
    var libraryID: String?
    /// Собственные имена медиатек (только в приложении): id медиатеки → имя.
    var aliases: [String: String] = [:]
    /// Запасные адреса того же сервера (домашний и внешний, VPN): пробуем по порядку, когда основной не отвечает.
    var alternateURLs: [URL] = []
    /// Имена своих HTTP-заголовков (значения — в Keychain); нужны, чтобы показать список без чтения секретов.
    var headerNames: [String] = []

    init(id: UUID = UUID(), name: String, url: URL, username: String, libraryID: String? = nil,
         aliases: [String: String] = [:], alternateURLs: [URL] = [], headerNames: [String] = []) {
        self.id = id; self.name = name; self.url = url; self.username = username; self.libraryID = libraryID
        self.aliases = aliases; self.alternateURLs = alternateURLs; self.headerNames = headerNames
    }

    // Старые сохранения не знают новых полей — читаем их как необязательные.
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decodeIfPresent(UUID.self, forKey: .id) ?? UUID()
        name = try c.decode(String.self, forKey: .name)
        url = try c.decode(URL.self, forKey: .url)
        username = try c.decode(String.self, forKey: .username)
        libraryID = try c.decodeIfPresent(String.self, forKey: .libraryID)
        aliases = try c.decodeIfPresent([String: String].self, forKey: .aliases) ?? [:]
        alternateURLs = try c.decodeIfPresent([URL].self, forKey: .alternateURLs) ?? []
        headerNames = try c.decodeIfPresent([String].self, forKey: .headerNames) ?? []
    }
}

/// Свои HTTP-заголовки сервера. Значения часто секретные (токены Cloudflare Access), поэтому только Keychain.
enum ServerHeaders {
    static func load(_ id: UUID) -> [String: String] {
        guard let json = Keychain.load(account: id.uuidString + ".headers"), let data = json.data(using: .utf8),
              let dict = try? JSONDecoder().decode([String: String].self, from: data) else { return [:] }
        return dict
    }
    static func save(_ headers: [String: String], for id: UUID) {
        if headers.isEmpty { Keychain.delete(account: id.uuidString + ".headers"); return }
        guard let data = try? JSONEncoder().encode(headers), let json = String(data: data, encoding: .utf8) else { return }
        Keychain.save(json, account: id.uuidString + ".headers")
    }
}
