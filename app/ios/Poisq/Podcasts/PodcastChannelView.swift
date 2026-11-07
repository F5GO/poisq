import SwiftUI
import PoisqCore
import PoisqPlayback

/// Страница канала: обложка, описание и выпуски (новые сверху).
struct PodcastChannelView: View {
    let channel: PodcastChannel
    @Environment(AppModel.self) private var model
    @State private var episodes: [PodcastEpisode] = []
    @State private var loaded = false
    @State private var expanded = false

    var body: some View {
        List {
            VStack(spacing: 12) {
                CoverArtView(id: channel.coverArt, size: 200, radius: 16)
                Text(channel.displayTitle).font(.title2.bold()).multilineTextAlignment(.center)
                if let d = channel.description, !d.isEmpty {
                    Text(d).font(.subheadline).foregroundStyle(.secondary).lineLimit(expanded ? nil : 3)
                        .onTapGesture { withAnimation { expanded.toggle() } }
                }
                if let error = channel.errorMessage, channel.status == "error" {
                    Label(error, systemImage: "exclamationmark.triangle").font(.footnote).foregroundStyle(.red)
                }
            }
            .frame(maxWidth: .infinity)
            .listRowSeparator(.hidden)
            Section {
                ForEach(episodes) { e in
                    NavigationLink(value: EpisodeRoute(episode: e, channelTitle: channel.displayTitle, channelID: channel.id)) {
                        EpisodeRow(episode: e, channelTitle: channel.displayTitle)
                    }
                }
            }
        }
        .listStyle(.plain)
        .overlay { if !loaded && episodes.isEmpty { ProgressView() } }
        .navigationTitle(channel.displayTitle)
        .navigationBarTitleDisplayMode(.inline)
        .refreshable { await load() }
        .task { episodes = channel.episodes; await load() }
    }

    private func load() async {
        if let fresh = try? await model.client?.podcastEpisodes(channelID: channel.id) {
            episodes = fresh.sorted { ($0.published ?? .distantPast) > ($1.published ?? .distantPast) }
        }
        loaded = true
    }
}
