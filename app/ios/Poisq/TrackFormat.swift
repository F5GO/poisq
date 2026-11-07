import Foundation
import PoisqCore

/// Подписи про формат и качество трека для плеера и экрана информации.
enum TrackFormat {
    /// «FLAC», «MP3», «AAC»…
    static func name(_ s: Song) -> String? {
        if let suffix = s.suffix, !suffix.isEmpty { return suffix.uppercased() }
        if let type = s.contentType, let sub = type.split(separator: "/").last { return sub.uppercased() }
        return nil
    }

    /// «24-bit/96 kHz» — только если сервер сообщил оба значения.
    static func resolution(_ s: Song) -> String? {
        guard let rate = s.samplingRate else { return nil }
        let khz = Double(rate) / 1000
        let khzText = khz == khz.rounded() ? String(Int(khz)) : String(format: "%.1f", khz)
        return s.bitDepth.map { "\($0)-bit/\(khzText) kHz" } ?? "\(khzText) kHz"
    }

    static func bitrate(_ s: Song) -> String? { s.bitRate.map { "\($0) kbps" } }

    /// Одна строка под полосой прогресса: «FLAC · 24-bit/96 kHz · 1411 kbps».
    static func summary(_ s: Song) -> String {
        [name(s), resolution(s), bitrate(s)].compactMap { $0 }.joined(separator: " · ")
    }

    /// С учётом качества: перекодированный трек показываем как «MP3 · до 320 kbps» (разрядность и частота оригинала уже не про него).
    static func summary(_ s: Song, quality: StreamQuality) -> String {
        guard let rate = quality.maxBitRate, let format = quality.format else { return summary(s) }
        let effective = min(rate, s.bitRate ?? rate)            // перекодировать выше исходного битрейта сервер не станет
        return "\(format.uppercased()) · \(effective) kbps"
    }

    static func size(_ s: Song) -> String? {
        s.size.map { ByteCountFormatStyle(style: .file).format(Int64($0)) }
    }
}
