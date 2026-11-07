import SwiftUI
import PoisqCore
import PoisqPlayback

/// Страница выпуска: описание, воспроизведение с запомненного места, загрузка и удаление на сервере.
struct EpisodeView: View {
    let route: EpisodeRoute
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var episode: PodcastEpisode
    @State private var failed = false
    private let store = PodcastStore.shared

    init(route: EpisodeRoute) { self.route = route; _episode = State(initialValue: route.episode) }

    var body: some View {
        let listened = store.isListened(episode)
        let position = store.position(of: episode)
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                HStack(alignment: .top, spacing: 16) {
                    CoverArtView(id: episode.coverArt, size: 110, radius: 14)
                    VStack(alignment: .leading, spacing: 4) {
                        if let c = route.channelTitle { Text(c).font(.subheadline).foregroundStyle(.secondary) }
                        Text(episode.title).font(.title3.bold())
                        HStack(spacing: 6) {
                            if let date = episode.published { Text(date, format: .dateTime.day().month(.abbreviated).year()) }
                            if let d = episode.duration { Text("· " + formatEpisodeDuration(d)) }
                        }
                        .font(.subheadline).foregroundStyle(.secondary)
                    }
                }
                if episode.isDownloaded {
                    Button { if let player = model.player { store.play(episode, channelTitle: route.channelTitle, player: player) } } label: {
                        Label(position > 5 && !listened ? "Resume" : "Play", systemImage: "play.fill").frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.glassProminent).controlSize(.large)
                } else if episode.isDownloading {
                    Label("Downloading on server…", systemImage: "arrow.down.circle").foregroundStyle(.secondary)
                } else {
                    Button { Task { await serverDownload() } } label: {
                        Label("Download to Server", systemImage: "icloud.and.arrow.down").frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.glassProminent).controlSize(.large)
                    Text("The server downloads the episode first, then it can be played.").font(.footnote).foregroundStyle(.secondary)
                }
                if let player = model.player, episode.isDownloaded {
                    HStack { Text("Speed"); Spacer(); SpeedMenu(player: player) }
                }
                if let text = episode.description.flatMap(Self.plain), !text.isEmpty {
                    Text(text).font(.body)
                }
            }
            .padding(20)
        }
        .navigationTitle("Episode")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                Menu {
                    if episode.isDownloaded {
                        Button(listened ? "Mark as Unplayed" : "Mark as Played", systemImage: listened ? "circle" : "checkmark.circle") {
                            store.setListened(!listened, episode)
                        }
                        Button("Delete from Server", systemImage: "trash", role: .destructive) { Task { await serverDelete() } }
                    }
                } label: { Label("More", systemImage: "ellipsis") }
            }
        }
        .alert("Couldn't complete the action", isPresented: $failed) { Button("OK", role: .cancel) {} }
        .task { await refresh() }
    }

    private func refresh() async {
        guard let channelID = route.channelID, let list = try? await model.client?.podcastEpisodes(channelID: channelID),
              let fresh = list.first(where: { $0.id == episode.id }) else { return }
        episode = fresh
    }

    private func serverDownload() async {
        do {
            try await model.client?.downloadPodcastEpisode(id: episode.id)
            for _ in 0..<20 {                       // сервер качает сам: ждём, пока выпуск появится
                try? await Task.sleep(for: .seconds(3))
                await refresh()
                if episode.isDownloaded { return }
            }
        } catch { failed = true }
    }

    private func serverDelete() async {
        do { try await model.client?.deletePodcastEpisode(id: episode.id); dismiss() } catch { failed = true }
    }

    /// В описаниях лент бывает HTML.
    private static func plain(_ html: String) -> String {
        var s = html.replacingOccurrences(of: "<br\\s*/?>|</p>", with: "\n", options: .regularExpression)
        s = s.replacingOccurrences(of: "<[^>]+>", with: "", options: .regularExpression)
        for (k, v) in ["&amp;": "&", "&quot;": "\"", "&#39;": "'", "&lt;": "<", "&gt;": ">", "&nbsp;": " "] { s = s.replacingOccurrences(of: k, with: v) }
        return s.trimmingCharacters(in: .whitespacesAndNewlines)
    }
}
