import Foundation

/// Качество звука при воспроизведении и загрузке. Сервер перекодирует «на лету» (параметры format и maxBitRate стандарта Subsonic).
public enum StreamQuality: Int, CaseIterable, Sendable, Codable, Identifiable {
    case original = 0
    case kbps320 = 320
    case kbps192 = 192
    case kbps128 = 128

    public var id: Int { rawValue }
    /// Максимальный битрейт в кбит/с; nil — без перекодирования.
    public var maxBitRate: Int? { self == .original ? nil : rawValue }
    /// Формат, в который сервер перекодирует. MP3 понимают все плееры и серверы.
    public var format: String? { self == .original ? nil : "mp3" }
    /// Метка для имён файлов: файлы разного качества не смешиваются.
    public var tag: String { self == .original ? "orig" : "\(rawValue)" }
    /// Расширение файла на устройстве; для оригинала берётся расширение трека.
    public var fileExtension: String? { format }
}
