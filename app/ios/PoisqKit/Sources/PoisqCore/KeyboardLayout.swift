import Foundation

/// Исправление текста, набранного не в той раскладке («Тшкмфтф» → «Nirvana», «rbyj» → «кино»).
/// Нужен клиенту, когда сервер не умеет этого сам; на сервере poisq то же самое делает прокси.
public enum KeyboardLayout {
    private static let latin = Array("qwertyuiop[]asdfghjkl;'zxcvbnm,.")
    private static let cyrillic = Array("йцукенгшщзхъфывапролджэячсмитьбю")
    private static let toCyrillic: [Character: Character] = Dictionary(uniqueKeysWithValues: zip(latin, cyrillic))
    private static let toLatin: [Character: Character] = Dictionary(uniqueKeysWithValues: zip(cyrillic, latin))

    /// Преобразованная строка или nil, если менять нечего (нет букв или строка смешанная).
    public static func swapped(_ text: String) -> String? {
        let letters = text.filter { $0.isLetter }
        guard !letters.isEmpty else { return nil }
        let latinCount = letters.filter { $0.isASCII }.count
        let toRu = latinCount == letters.count
        let toEn = latinCount == 0
        guard toRu || toEn else { return nil }
        let table = toRu ? toCyrillic : toLatin
        let result = String(text.map { ch -> Character in
            let lower = Character(ch.lowercased())
            guard let mapped = table[lower] else { return ch }
            return ch.isUppercase ? Character(mapped.uppercased()) : mapped
        })
        return result == text ? nil : result
    }
}
