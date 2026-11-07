import Foundation
import Observation

/// Полоса эквалайзера: пик или полка, частота, усиление, добротность.
public struct EQBand: Codable, Hashable, Sendable {
    public enum Kind: String, Codable, Sendable { case peak, lowShelf, highShelf }
    public var kind: Kind
    public var frequency: Float
    public var gain: Float
    public var q: Float

    public init(kind: Kind = .peak, frequency: Float, gain: Float = 0, q: Float = 1.2) {
        self.kind = kind; self.frequency = frequency; self.gain = gain; self.q = q
    }
}

/// То, что получает движок: включено ли, полосы и общий предусилитель (дБ).
public struct EQConfig: Sendable, Equatable {
    public var enabled: Bool
    public var bands: [EQBand]
    public var preampDb: Float
    public init(enabled: Bool, bands: [EQBand], preampDb: Float) { self.enabled = enabled; self.bands = bands; self.preampDb = preampDb }
}

/// Пресет эквалайзера. Встроенные названия — английские ключи локализации.
public struct EQPreset: Identifiable, Codable, Hashable, Sendable {
    public enum Category: String, Codable, Sendable, CaseIterable { case basic, genre, situation, headphone, custom }
    public var id: String
    public var name: String
    public var category: Category
    public var bands: [EQBand]
    public var preamp: Float
    /// Автоматически снижать общий уровень на величину самого большого усиления, чтобы не было клиппинга.
    public var headroom: Bool
    public var builtIn: Bool

    public static let graphicFrequencies: [Float] = [32, 64, 125, 250, 500, 1000, 2000, 4000, 8000, 16000]

    public static func graphic(_ id: String, _ name: String, _ category: Category, _ gains: [Float]) -> EQPreset {
        EQPreset(id: id, name: name, category: category,
                 bands: zip(graphicFrequencies, gains).map { EQBand(frequency: $0, gain: $1) },
                 preamp: 0, headroom: true, builtIn: true)
    }

    /// Десять полос с нулевым усилением.
    public static var flatBands: [EQBand] { graphicFrequencies.map { EQBand(frequency: $0) } }

    /// Встроенные пресеты: собственные настройки, подобранные на слух под типичные жанры и ситуации.
    public static let builtIn: [EQPreset] = [
        .graphic("flat", "Flat", .basic, [0, 0, 0, 0, 0, 0, 0, 0, 0, 0]),
        .graphic("bass-boost", "Bass Boost", .basic, [6, 5, 4, 2, 0, 0, 0, 0, 0, 0]),
        .graphic("bass-reducer", "Bass Reducer", .basic, [-6, -5, -4, -2, 0, 0, 0, 0, 0, 0]),
        .graphic("treble-boost", "Treble Boost", .basic, [0, 0, 0, 0, 0, 1, 2, 4, 5, 6]),
        .graphic("treble-reducer", "Treble Reducer", .basic, [0, 0, 0, 0, 0, -1, -2, -4, -5, -6]),
        .graphic("vocal-boost", "Vocal Boost", .basic, [-2, -2, -1, 1, 3, 4, 3, 1, 0, -1]),
        .graphic("loudness", "Loudness", .basic, [5, 4, 1, 0, -1, -1, 0, 2, 4, 4]),
        .graphic("v-shape", "V-Shape", .basic, [5, 4, 2, 0, -2, -2, 0, 2, 4, 5]),

        .graphic("rock", "Rock", .genre, [4, 3, 1, -1, -2, -1, 1, 3, 4, 4]),
        .graphic("metal", "Metal", .genre, [5, 4, 0, -2, -3, 0, 3, 4, 4, 3]),
        .graphic("pop", "Pop", .genre, [-1, 1, 3, 4, 3, 1, -1, -1, 1, 2]),
        .graphic("hip-hop", "Hip-Hop", .genre, [5, 4, 2, 3, -1, -1, 1, -1, 2, 3]),
        .graphic("electronic", "Electronic", .genre, [4, 4, 1, 0, -2, 1, 0, 1, 4, 5]),
        .graphic("dance", "Dance", .genre, [5, 5, 2, 0, 0, -2, -1, 0, 3, 3]),
        .graphic("jazz", "Jazz", .genre, [3, 2, 1, 2, -2, -2, 0, 1, 2, 3]),
        .graphic("classical", "Classical", .genre, [3, 2, 1, 0, 0, 0, -1, -1, 2, 3]),
        .graphic("acoustic", "Acoustic", .genre, [4, 3, 2, 1, 2, 2, 3, 3, 2, 1]),
        .graphic("rnb", "R&B", .genre, [3, 6, 5, 1, -2, -1, 2, 2, 3, 3]),
        .graphic("latin", "Latin", .genre, [3, 2, 0, 0, -1, -1, -1, 0, 2, 3]),
        .graphic("piano", "Piano", .genre, [2, 1, 0, 2, 3, 1, 2, 3, 3, 2]),

        .graphic("late-night", "Late Night", .situation, [2, 3, 2, 0, -1, -2, -2, -1, 0, -1]),
        .graphic("small-speakers", "Small Speakers", .situation, [6, 5, 4, 2, 1, 0, -1, -1, -1, -1]),
        .graphic("spoken-word", "Spoken Word", .situation, [-3, -2, 0, 1, 3, 4, 4, 3, 2, 0]),
        .graphic("deep", "Deep", .situation, [4, 3, 3, 2, 0, -1, -3, -4, -5, -6]),
        .graphic("lounge", "Lounge", .situation, [-3, -1, 1, 2, 3, 2, 0, -1, 2, 1]),
        .graphic("warm", "Warm", .situation, [3, 3, 2, 1, 0, 0, -1, -2, -3, -4]),
        .graphic("bright", "Bright", .situation, [-3, -2, -1, 0, 0, 1, 2, 3, 4, 5]),
    ]

    /// Пресет по жанру трека (для «Автоматически по жанру»). nil — жанр не опознан.
    public static func presetID(forGenre genre: String?) -> String? {
        guard let g = genre?.lowercased(), !g.isEmpty else { return nil }
        let table: [(keys: [String], id: String)] = [
            (["metal", "hardcore", "punk"], "metal"), (["rock", "grunge", "alternative", "indie"], "rock"),
            (["hip hop", "hip-hop", "rap", "trap"], "hip-hop"), (["r&b", "rnb", "soul", "funk"], "rnb"),
            (["dance", "house", "techno", "trance", "edm", "disco"], "dance"), (["electro", "synth", "ambient", "idm"], "electronic"),
            (["jazz", "blues", "swing"], "jazz"), (["classic", "opera", "orchestra", "baroque"], "classical"),
            (["piano"], "piano"), (["acoustic", "folk", "country", "singer"], "acoustic"),
            (["latin", "reggae", "salsa", "bossa"], "latin"), (["lounge", "chill", "downtempo"], "lounge"),
            (["pop"], "pop"), (["podcast", "spoken", "audiobook", "speech"], "spoken-word"),
        ]
        return table.first { $0.keys.contains { g.contains($0) } }?.id
    }

    /// Разбор профиля коррекции наушников в формате AutoEq (`ParametricEQ.txt`): «Preamp: -6 dB», «Filter 1: ON PK Fc 105 Hz Gain -2.3 dB Q 0.70».
    public static func parseAutoEq(_ text: String, name: String) -> EQPreset? {
        var bands: [EQBand] = []
        var preamp: Float = 0
        for raw in text.split(whereSeparator: \.isNewline) {
            let line = String(raw)
            if line.lowercased().hasPrefix("preamp") {
                if let v = Self.numbers(in: line).first { preamp = v }
                continue
            }
            guard line.lowercased().hasPrefix("filter"), line.contains(" ON ") else { continue }
            let tokens = line.split(separator: " ").map(String.init)
            func value(after key: String) -> Float? {
                guard let i = tokens.firstIndex(of: key), i + 1 < tokens.count else { return nil }
                return Float(tokens[i + 1])
            }
            let kind: EQBand.Kind
            if tokens.contains("PK") { kind = .peak }
            else if tokens.contains(where: { ["LSC", "LS", "LSQ"].contains($0) }) { kind = .lowShelf }
            else if tokens.contains(where: { ["HSC", "HS", "HSQ"].contains($0) }) { kind = .highShelf }
            else { continue }
            guard let fc = value(after: "Fc"), let gain = value(after: "Gain") else { continue }
            bands.append(EQBand(kind: kind, frequency: fc, gain: gain, q: value(after: "Q") ?? 0.7))
        }
        guard !bands.isEmpty else { return nil }
        return EQPreset(id: "headphone-" + UUID().uuidString.prefix(8), name: name, category: .headphone,
                        bands: Array(bands.prefix(16)), preamp: preamp, headroom: false, builtIn: false)
    }

    private static func numbers(in s: String) -> [Float] {
        s.split(whereSeparator: { !"-0123456789.".contains($0) }).compactMap { Float($0) }
    }
}

/// Эквалайзер приложения: состояние, пресеты, сохранение, связь с движком.
@MainActor @Observable
public final class Equalizer {
    public var isEnabled: Bool { didSet { changed() } }
    /// Выбранный пользователем пресет (или «edited» — полосы правили вручную).
    public private(set) var selectedID: String
    /// Пресет, включённый автоматически по жанру текущего трека.
    public private(set) var autoID: String?
    public private(set) var bands: [EQBand]
    public var preamp: Float { didSet { changed() } }
    public var headroom: Bool { didSet { changed() } }
    public var autoByGenre: Bool { didSet { changed(); if !autoByGenre { autoID = nil; reapplyPreset() } } }
    public private(set) var custom: [EQPreset]

    @ObservationIgnored private weak var engine: PlaybackEngine?
    @ObservationIgnored private let defaults = UserDefaults.standard
    private static let key = "equalizer.v1"

    private struct Stored: Codable {
        var enabled: Bool; var selectedID: String; var bands: [EQBand]; var preamp: Float
        var headroom: Bool; var autoByGenre: Bool; var custom: [EQPreset]
    }

    public init() {
        let s = UserDefaults.standard.data(forKey: Self.key).flatMap { try? JSONDecoder().decode(Stored.self, from: $0) }
        isEnabled = s?.enabled ?? false
        selectedID = s?.selectedID ?? "flat"
        bands = s?.bands ?? EQPreset.flatBands
        preamp = s?.preamp ?? 0
        headroom = s?.headroom ?? true
        autoByGenre = s?.autoByGenre ?? false
        custom = s?.custom ?? []
    }

    public func attach(_ engine: PlaybackEngine) {
        self.engine = engine
        push()
    }

    public var allPresets: [EQPreset] { EQPreset.builtIn + custom }
    public func preset(id: String) -> EQPreset? { allPresets.first { $0.id == id } }
    /// Название того, что сейчас звучит.
    public var activeName: String {
        if let autoID, let p = preset(id: autoID) { return p.name }
        return preset(id: selectedID)?.name ?? "Custom"
    }
    public var activeIsBuiltIn: Bool { (autoID ?? selectedID).isEmpty ? false : (preset(id: autoID ?? selectedID)?.builtIn ?? false) }

    /// Максимальное усиление в дБ — по нему считается запас по уровню.
    public var peakBoost: Float { max(0, bands.map(\.gain).max() ?? 0) }
    public var effectivePreamp: Float { preamp - (headroom ? peakBoost : 0) }

    public func select(_ p: EQPreset) {
        autoID = nil
        selectedID = p.id
        apply(p)
    }

    private func apply(_ p: EQPreset) {
        bands = p.bands
        preamp = p.preamp
        headroom = p.headroom
        changed()
    }

    private func reapplyPreset() {
        if let p = preset(id: selectedID) { apply(p) }
    }

    /// Правка одной полосы вручную: пресет становится «своим» (несохранённым).
    public func setGain(_ gain: Float, at index: Int) {
        guard bands.indices.contains(index) else { return }
        bands[index].gain = (gain * 2).rounded() / 2          // шаг 0,5 дБ
        autoID = nil
        selectedID = "edited"
        changed()
    }

    public func reset() { select(EQPreset.builtIn[0]) }

    @discardableResult
    public func saveCurrent(as name: String) -> EQPreset {
        let p = EQPreset(id: "custom-" + UUID().uuidString.prefix(8), name: name, category: .custom,
                         bands: bands, preamp: preamp, headroom: headroom, builtIn: false)
        custom.append(p)
        selectedID = p.id
        changed()
        return p
    }

    public func delete(_ p: EQPreset) {
        custom.removeAll { $0.id == p.id }
        if selectedID == p.id { reset() } else { changed() }
    }

    public func addImported(_ p: EQPreset) {
        custom.append(p)
        select(p)
    }

    /// Вызывается при смене трека: при включённом «по жанру» подбирает пресет под жанр.
    public func trackChanged(genre: String?) {
        guard isEnabled, autoByGenre else { return }
        let id = EQPreset.presetID(forGenre: genre)
        guard id != autoID else { return }
        autoID = id
        if let id, let p = preset(id: id) { apply(p) } else { reapplyPreset() }
    }

    private func changed() {
        save()
        push()
    }

    private func push() {
        engine?.setEqualizer(EQConfig(enabled: isEnabled, bands: bands, preampDb: effectivePreamp))
    }

    private func save() {
        let s = Stored(enabled: isEnabled, selectedID: selectedID, bands: bands, preamp: preamp,
                       headroom: headroom, autoByGenre: autoByGenre, custom: custom)
        if let d = try? JSONEncoder().encode(s) { defaults.set(d, forKey: Self.key) }
    }
}
