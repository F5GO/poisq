import Foundation

public enum ServerURL {
    /// Превращает ввод пользователя в список адресов-кандидатов.
    /// Без схемы — только https: молча переходить на http нельзя, токен и заголовки уйдут открытым текстом.
    /// Сервер без TLS пользователь указывает явно через http://.
    public static func candidates(from input: String) -> [URL] {
        let text = input.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return [] }
        let raw: [String] = text.contains("://") ? [text] : ["https://" + text]
        return raw.compactMap { s in
            var s = s
            while s.hasSuffix("/") { s.removeLast() }
            guard let u = URL(string: s), let host = u.host(), !host.isEmpty else { return nil }
            let scheme = u.scheme?.lowercased()
            guard scheme == "http" || scheme == "https" else { return nil }
            return u
        }
    }
}
