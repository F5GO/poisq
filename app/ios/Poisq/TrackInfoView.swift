import SwiftUI
import PoisqCore
import PoisqPlayback

/// Техническая информация о треке.
struct TrackInfoView: View {
    let song: Song
    let source: TrackSource
    @Environment(\.dismiss) private var dismiss
    @Environment(AppModel.self) private var model

    var body: some View {
        NavigationStack {
            List {
                Section {
                    row("Title", song.title)
                    row("Artist", song.displayArtist)
                    row("Album", song.album)
                    row("Year", song.year.map(String.init))
                    row("Genre", song.genre)
                    row("Track", song.track.map(String.init))
                    row("Duration", song.duration.map { "\($0 / 60):" + String(format: "%02d", $0 % 60) })
                    if let player = model.player {
                        LabeledContent("Rating") {
                            let r = player.rating(of: song)
                            HStack(spacing: 4) {
                                ForEach(1...5, id: \.self) { n in
                                    Button { Task { await player.setRating(r == n ? 0 : n, for: song) } } label: {
                                        Image(systemName: n <= r ? "star.fill" : "star").foregroundStyle(n <= r ? Color.yellow : .secondary)
                                    }
                                    .buttonStyle(.plain)
                                }
                            }
                            .sensoryFeedback(.selection, trigger: r)
                        }
                    }
                }
                Section("Quality") {
                    row("Format", TrackFormat.name(song))
                    row("Bitrate", TrackFormat.bitrate(song))
                    row("Resolution", TrackFormat.resolution(song))
                    row("Channels", song.channelCount.map(String.init))
                    row("Size", TrackFormat.size(song))
                    row("Source", { switch source { case .downloaded: String(localized: "Downloaded"); case .cached: String(localized: "Cached"); case .streaming: String(localized: "Online") } }())
                }
                if let path = song.path {
                    Section("File") { Text(path).font(.footnote).foregroundStyle(.secondary).textSelection(.enabled) }
                }
            }
            .navigationTitle("Track Info")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done", systemImage: "checkmark") { dismiss() } } }
        }
        .presentationDetents([.medium, .large])
    }

    /// Строка скрывается, если сервер не сообщил значение.
    @ViewBuilder private func row(_ title: LocalizedStringKey, _ value: String?) -> some View {
        if let value, !value.isEmpty { LabeledContent(title, value: value) }
    }
}
