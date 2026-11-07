import Foundation
import NaturalLanguage
import PoisqCore

/// Мягкие переносы для длинных слов в текстах песен.
/// Правила переноса строк:
/// 1. Короткие слова (до 3 букв: «в», «на», «по», «не», «to», «a») склеиваются со следующим неразрывным пробелом,
///    чтобы они не повисали в конце строки.
/// 2. Два последних слова строки склеиваются, чтобы на последней строке не оставалось одного слова.
/// 3. Внутри слов — мягкие переносы: только слова от 10 букв, по системному словарю переносов для языка строки (русский, английский…),
/// и оставляем не меньше трёх букв до и после переноса, чтобы не оставалось «о-» и «ся» на отдельной строке.
/// Знак переноса (U+00AD) виден, только если слово не помещается в строке.
enum Hyphenator {
    private static let minWord = 10
    private static let minPart = 3
    private static let softHyphen: Character = "\u{00AD}"

    static func soften(_ lyrics: Lyrics) -> Lyrics {
        Lyrics(lines: lyrics.lines.map { Lyrics.Line(id: $0.id, startMs: $0.startMs, text: soften($0.text)) })
    }

    static func soften(_ text: String) -> String {
        bindWords(hyphenated(text))
    }

    /// Неразрывные пробелы после коротких слов и между двумя последними словами.
    private static func bindWords(_ text: String) -> String {
        let words = text.split(separator: " ", omittingEmptySubsequences: true).map(String.init)
        guard words.count > 1 else { return text }
        var out = words[0]
        for i in 1..<words.count {
            let prevShort = words[i - 1].filter(\.isLetter).count <= 3
            let isLastPair = i == words.count - 1
            out += (prevShort || isLastPair) ? "\u{00A0}" : " "
            out += words[i]
        }
        return out
    }

    private static func hyphenated(_ text: String) -> String {
        guard text.count >= minWord else { return text }
        let recognizer = NLLanguageRecognizer()
        recognizer.processString(text)
        let code = recognizer.dominantLanguage?.rawValue ?? "en"
        let locale = Locale(identifier: code) as CFLocale
        guard CFStringIsHyphenationAvailableForLocale(locale) else { return text }

        var result = ""
        var word = ""
        func flush() {
            result += hyphenate(word, locale: locale)
            word = ""
        }
        for ch in text {
            if ch.isLetter { word.append(ch) } else { flush(); result.append(ch) }
        }
        flush()
        return result
    }

    private static func hyphenate(_ word: String, locale: CFLocale) -> String {
        let chars = Array(word)
        guard chars.count >= minWord else { return word }
        let cf = word as CFString
        let range = CFRange(location: 0, length: CFStringGetLength(cf))
        var points: [Int] = []
        var from = range.length
        while from > 0 {
            let loc = CFStringGetHyphenationLocationBeforeIndex(cf, from, range, 0, locale, nil)
            if loc == kCFNotFound || loc <= 0 { break }
            if loc >= minPart, range.length - loc >= minPart, (points.last.map { $0 - loc >= minPart } ?? true) {
                points.append(loc)
            }
            from = loc
        }
        var out = chars
        for p in points where p < out.count { out.insert(softHyphen, at: p) }      // points убывают, индексы не сбиваются
        return String(out)
    }
}
