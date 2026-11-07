import Foundation

public enum ServerURL {
    /// Превращает ввод пользователя в список адресов-кандидатов.
    /// Без схемы пробуем https, затем http (домашние серверы часто без TLS).
    public static func candidates(from input: String) -> [URL] {
        let text = input.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return [] }
        let raw: [String] = text.contains("://") ? [text] : ["https://" + text, "http://" + text]
        return raw.compactMap { s in
            var s = s
            while s.hasSuffix("/") { s.removeLast() }
            guard let u = URL(string: s), let host = u.host(), !host.isEmpty,
                  u.scheme == "http" || u.scheme == "https" else { return nil }
            return u
        }
    }
}
