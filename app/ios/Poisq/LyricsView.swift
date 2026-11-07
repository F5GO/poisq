import SwiftUI
import PoisqCore
import PoisqPlayback

/// Загрузка и кеш текстов для плеера. Общая для текста на обложке и полноэкранного режима.
@MainActor @Observable
final class LyricsModel {
    private(set) var lyrics: Lyrics?
    private(set) var isLoading = false
    private var cache: [String: Lyrics] = [:]
    private var missing: Set<String> = []

    /// `force` — запросить заново (кнопка «Обновить текст»).
    func load(song: Song?, client: SubsonicClient?, force: Bool = false) async {
        guard let song, let client else { lyrics = nil; return }
        if force { cache[song.id] = nil; missing.remove(song.id) }
        if let hit = cache[song.id] { lyrics = hit; return }
        if missing.contains(song.id) { lyrics = nil; return }
        isLoading = true
        lyrics = nil
        let result = await client.lyrics(for: song)
        isLoading = false
        // Оформление строк — только для текста с таймингами; обычный текст показываем как есть. Считаем один раз, а не при каждой перерисовке.
        let soft = result.map { $0.isSynced ? Hyphenator.soften($0) : $0 }
        if let soft { cache[song.id] = soft } else { missing.insert(song.id) }
        lyrics = soft
    }
}

/// Прокручиваемый текст. С таймингами подсвечивает текущую строку (остальные приглушены и, если включено, размыты)
/// и прокручивается сам; нажатие на строку перематывает.
struct LyricsPane: View {
    let player: Player
    let model: LyricsModel
    var fontSize: CGFloat = 26
    var blurInactive: Bool
    var onDark = false

    var body: some View {
        Group {
            if let lyrics = model.lyrics { scroll(lyrics) }
            else if model.isLoading { ProgressView().tint(onDark ? .white : nil) }
            else {
                VStack(spacing: 8) {
                    Image(systemName: "quote.bubble").font(.title)
                    Text("No lyrics").font(.headline)
                }
                .foregroundStyle(onDark ? Color.white.opacity(0.7) : .secondary)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    /// Если текст начинается позже этого момента, до него показываем «ожидание» (три точки); раньше — первая строка сразу по центру.
    private static let introThresholdMs = 1500
    private static let introID = -1

    private func scroll(_ lyrics: Lyrics) -> some View {
        GeometryReader { geo in
        TimelineView(.periodic(from: .now, by: 0.3)) { _ in
            let synced = lyrics.isSynced
            let (active, intro) = position(lyrics, ms: Int(player.progress * 1000))
            let target: Int? = intro ? Self.introID : active.map { lyrics.lines[$0].id }
            ScrollViewReader { proxy in
                ScrollView(showsIndicators: false) {
                    // Как в Яндекс Музыке: текст по центру, текущая строка крупнее и ярче, соседние мельче и тусклее.
                    LazyVStack(spacing: fontSize * 0.55) {
                        if synced { introDots(visible: intro) }
                        ForEach(Array(lyrics.lines.enumerated()), id: \.element.id) { index, line in
                            let isCurrent = !synced || index == active
                            Text(line.text.isEmpty ? " " : line.text)
                                .font(.system(size: fontSize, weight: .bold))
                                .multilineTextAlignment(.center)
                                .frame(maxWidth: .infinity)
                                .scaleEffect(isCurrent ? 1 : 0.65)
                                .opacity(isCurrent ? 1 : 0.4)
                                .blur(radius: blurRadius(index: index, active: intro ? -1 : active))
                                .animation(.smooth, value: active)
                                .id(line.id)
                                .contentShape(.rect)
                                .onTapGesture { if let t = line.startMs { player.seek(to: Double(t) / 1000) } }
                        }
                    }
                    // Запас сверху и снизу в половину высоты: иначе первую (и последнюю) строку нельзя прокрутить к центру.
                    .padding(.vertical, geo.size.height / 2)
                    // Для караоке — более узкая колонка, чтобы строки переносились по 2–3 слова, как в Яндекс Музыке.
                    .padding(.horizontal, synced ? fontSize * 0.9 : 0)
                }
                .task {
                    // Сразу после появления список ещё не разложен — прокрутка к строке не сработает. Пробуем, пока он не готов, без анимации.
                    guard let target else { return }
                    for delay in [30, 120, 300] {
                        try? await Task.sleep(for: .milliseconds(delay))
                        proxy.scrollTo(target, anchor: .center)
                    }
                }
                .onChange(of: target) { _, new in
                    if let new { withAnimation(.smooth) { proxy.scrollTo(new, anchor: .center) } }
                }
            }
        }
        }
        // Строки плавно уходят в прозрачность у верхнего и нижнего края.
        .mask(LinearGradient(stops: [.init(color: .clear, location: 0), .init(color: .black, location: 0.1),
                                     .init(color: .black, location: 0.9), .init(color: .clear, location: 1)],
                             startPoint: .top, endPoint: .bottom))
    }

    /// Текущая строка и признак «ждём начала текста».
    private func position(_ lyrics: Lyrics, ms: Int) -> (active: Int?, intro: Bool) {
        guard lyrics.isSynced else { return (nil, false) }
        if let i = lyrics.lines.lastIndex(where: { ($0.startMs ?? Int.max) <= ms }) { return (i, false) }
        let first = lyrics.lines.compactMap(\.startMs).min() ?? 0
        return first > Self.introThresholdMs ? (nil, true) : (0, false)      // текст почти сразу — первая строка уже активна
    }

    /// Три точки ожидания: пульсируют по очереди; когда текст начался, плавно схлопываются.
    private func introDots(visible: Bool) -> some View {
        TimelineView(.animation) { timeline in
            let t = timeline.date.timeIntervalSinceReferenceDate
            HStack(spacing: fontSize * 0.5) {
                ForEach(0..<3, id: \.self) { i in
                    let wave = (sin(t * 3.2 - Double(i) * 0.9) + 1) / 2
                    Circle()
                        .frame(width: fontSize * 0.55, height: fontSize * 0.55)
                        .scaleEffect(0.7 + 0.3 * wave)
                        .opacity(0.4 + 0.6 * wave)
                }
            }
            .foregroundStyle(onDark ? Color.white : Color.primary)
            .frame(maxWidth: .infinity)
        }
        .frame(height: visible ? fontSize * 1.3 : 0)
        .opacity(visible ? 1 : 0)
        .clipped()
        .animation(.smooth, value: visible)
        .id(Self.introID)
        .accessibilityHidden(!visible)
    }

    /// Чем дальше строка от текущей, тем сильнее размытие (до 5 пунктов).
    private func blurRadius(index: Int, active: Int?) -> CGFloat {
        guard blurInactive, let active, index != active else { return 0 }
        return min(CGFloat(abs(index - active)) * 1.5, 5)
    }
}

/// Текст на весь экран. В меню: обновить текст и размытие строк.
struct LyricsFullScreen: View {
    let player: Player
    let model: LyricsModel
    let onRefresh: () -> Void
    @Binding var blurInactive: Bool
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            LyricsPane(player: player, model: model, fontSize: 40, blurInactive: blurInactive)
                .padding(.horizontal, 24)
                .background { PlayerBackdrop(coverID: player.current?.coverArt) }
                .toolbar {
                    ToolbarItem(placement: .topBarLeading) {
                        Button("Collapse", systemImage: "arrow.down.right.and.arrow.up.left") { dismiss() }
                    }
                    ToolbarItem(placement: .principal) {
                        VStack(spacing: 0) {
                            Text(player.current?.title ?? "").font(.headline).lineLimit(1)
                            Text(player.current?.artist ?? "").font(.caption).foregroundStyle(.secondary).lineLimit(1)
                        }
                    }
                    ToolbarItem(placement: .topBarTrailing) {
                        Menu {
                            Button("Refresh Lyrics", systemImage: "arrow.clockwise", action: onRefresh)
                            Toggle("Blur Other Lines", systemImage: "drop", isOn: $blurInactive)
                        } label: { Image(systemName: "ellipsis") }
                        .accessibilityLabel("Lyrics Options")
                    }
                }
                .navigationBarTitleDisplayMode(.inline)
        }
    }
}
