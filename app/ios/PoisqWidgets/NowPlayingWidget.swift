import SwiftUI
import WidgetKit
import AppIntents

struct NowPlayingEntry: TimelineEntry {
    let date: Date
    let track: SharedTrack?
}

struct NowPlayingProvider: TimelineProvider {
    func placeholder(in context: Context) -> NowPlayingEntry {
        NowPlayingEntry(date: .now, track: SharedTrack(title: "Song", artist: "Artist", isPlaying: true))
    }
    func getSnapshot(in context: Context, completion: @escaping (NowPlayingEntry) -> Void) {
        completion(NowPlayingEntry(date: .now, track: SharedStore.track))
    }
    func getTimeline(in context: Context, completion: @escaping (Timeline<NowPlayingEntry>) -> Void) {
        // Приложение само перезагружает виджет при смене трека и паузе.
        completion(Timeline(entries: [NowPlayingEntry(date: .now, track: SharedStore.track)], policy: .never))
    }
}

struct NowPlayingWidget: Widget {
    var body: some WidgetConfiguration {
        StaticConfiguration(kind: "NowPlaying", provider: NowPlayingProvider()) { entry in
            NowPlayingView(entry: entry)
                .widgetURL(DeepLink.player)        // нажатие вне кнопок открывает плеер
                .containerBackground(.fill.tertiary, for: .widget)
        }
        .configurationDisplayName("Now Playing")
        .description("The current song with a play/pause button.")
        .supportedFamilies([.systemSmall, .systemMedium, .accessoryRectangular, .accessoryCircular, .accessoryInline])
    }
}

private struct PlayPauseButton: View {
    let isPlaying: Bool
    var body: some View {
        Button(intent: PlayPauseIntent()) {
            Image(systemName: isPlaying ? "pause.fill" : "play.fill")
                .contentTransition(.symbolEffect(.replace))
        }
    }
}

struct NowPlayingView: View {
    let entry: NowPlayingEntry
    @Environment(\.widgetFamily) private var family

    var body: some View {
        switch family {
        case .systemSmall: small
        case .systemMedium: medium
        case .accessoryRectangular: rectangular
        case .accessoryCircular: circular
        case .accessoryInline: inline
        default: small
        }
    }

    private var title: String { entry.track?.title ?? String(localized: "Nothing Playing") }

    private var small: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .top) {
                SharedCover(file: entry.track?.coverFile).frame(width: 56, height: 56)
                    .clipShape(.rect(cornerRadius: 10, style: .continuous))
                Spacer(minLength: 0)
                if entry.track != nil { PlayPauseButton(isPlaying: entry.track?.isPlaying ?? false).buttonStyle(.plain).font(.title2) }
            }
            Spacer(minLength: 0)
            Text(title).font(.subheadline.weight(.semibold)).lineLimit(2)
            if let artist = entry.track?.artist { Text(artist).font(.caption).foregroundStyle(.secondary).lineLimit(1) }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private var medium: some View {
        HStack(spacing: 14) {
            SharedCover(file: entry.track?.coverFile).frame(width: 110, height: 110)
                .clipShape(.rect(cornerRadius: 14, style: .continuous))
            VStack(alignment: .leading, spacing: 4) {
                Text(title).font(.headline).lineLimit(2)
                if let artist = entry.track?.artist { Text(artist).font(.subheadline).foregroundStyle(.secondary).lineLimit(1) }
                Spacer(minLength: 0)
                if entry.track != nil {
                    HStack(spacing: 22) {
                        Button(intent: PreviousTrackIntent()) { Image(systemName: "backward.fill") }
                        PlayPauseButton(isPlaying: entry.track?.isPlaying ?? false).font(.title)
                        Button(intent: NextTrackIntent()) { Image(systemName: "forward.fill") }
                    }
                    .buttonStyle(.plain).font(.title3)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    private var rectangular: some View {
        HStack(spacing: 8) {
            VStack(alignment: .leading, spacing: 0) {
                Text(title).font(.headline).lineLimit(1).widgetAccentable()
                Text(entry.track?.artist ?? "").font(.caption).lineLimit(1)
            }
            Spacer(minLength: 0)
            if entry.track != nil { PlayPauseButton(isPlaying: entry.track?.isPlaying ?? false).buttonStyle(.plain).font(.title3) }
        }
    }

    private var circular: some View {
        // Круглый виджет целиком открывает приложение: кнопка на всю площадь не оставляла бы места для нажатия «открыть».
        ZStack {
            AccessoryWidgetBackground()
            Image(systemName: entry.track?.isPlaying == true ? "waveform" : "music.note").font(.title2)
        }
    }

    private var inline: some View {
        Text([entry.track?.title, entry.track?.artist].compactMap { $0 }.joined(separator: " – ").nonEmpty ?? String(localized: "Nothing Playing"))
    }
}

private extension String { var nonEmpty: String? { isEmpty ? nil : self } }
