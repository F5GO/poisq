import SwiftUI
import PoisqCore
import PoisqPlayback

/// Переход на выпуск: нужен и канал, чтобы подписать трек в плеере.
struct EpisodeRoute: Hashable {
    let episode: PodcastEpisode
    let channelTitle: String?
    let channelID: String?
}

/// Каналы подкастов сервера. Раздел виден, только если сервер поддерживает подкасты (`Capabilities.hasPodcasts`).
struct PodcastsView: View {
    @Environment(AppModel.self) private var model
    @State private var channels: [PodcastChannel] = []
    @State private var loaded = false
    @State private var adding = false
    @State private var newURL = ""
    @State private var failed = false
    @State private var pendingDelete: PodcastChannel?

    var body: some View {
        List {
            if !channels.isEmpty {
                Section {
                    NavigationLink(value: PodcastSection.newest) {
                        Label("New Episodes", systemImage: "sparkles").foregroundStyle(.primary)
                    }
                }
            }
            Section {
                ForEach(channels) { channel in
                    NavigationLink(value: channel) {
                        MediaRow(coverID: channel.coverArt, title: channel.displayTitle,
                                 subtitle: channel.episodes.isEmpty ? channel.description : String(localized: "\(channel.episodes.count) episodes"))
                    }
                    .swipeActions { Button("Delete", systemImage: "trash", role: .destructive) { pendingDelete = channel } }
                }
            }
        }
        .listStyle(.plain)
        .overlay {
            if !loaded { ProgressView() }
            else if channels.isEmpty {
                ContentUnavailableView {
                    Label("No Podcasts", systemImage: "mic")
                } description: {
                    Text("Add a podcast feed address to follow it on your server.")
                } actions: {
                    Button("Add Podcast") { adding = true }
                }
            }
        }
        .navigationTitle("Podcasts")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                Menu {
                    Button("Add Podcast", systemImage: "plus") { adding = true }
                    Button("Check for New Episodes", systemImage: "arrow.clockwise") { Task { try? await model.client?.refreshPodcasts(); await load() } }
                } label: { Label("More", systemImage: "ellipsis") }
            }
        }
        .alert("Add Podcast", isPresented: $adding) {
            TextField("Feed address", text: $newURL).textInputAutocapitalization(.never).keyboardType(.URL).autocorrectionDisabled()
            Button("Cancel", role: .cancel) { newURL = "" }
            Button("Add") { Task { await add() } }
        } message: { Text("The server downloads episodes itself.") }
        .alert("Couldn't complete the action", isPresented: $failed) { Button("OK", role: .cancel) {} }
        .confirmationDialog("Delete this podcast from the server?", isPresented: Binding(get: { pendingDelete != nil }, set: { if !$0 { pendingDelete = nil } }),
                            titleVisibility: .visible) {
            Button("Delete", role: .destructive) { if let c = pendingDelete { Task { await remove(c) } } }
        }
        .navigationDestination(for: PodcastChannel.self) { PodcastChannelView(channel: $0) }
        .navigationDestination(for: EpisodeRoute.self) { EpisodeView(route: $0) }
        .navigationDestination(for: PodcastSection.self) { _ in NewestEpisodesView() }
        .refreshable { await load() }
        .task(id: model.contentKey) { await load() }
    }

    private func load() async {
        channels = (try? await model.client?.podcasts()) ?? channels
        loaded = true
    }

    private func add() async {
        let url = newURL.trimmingCharacters(in: .whitespacesAndNewlines)
        newURL = ""
        guard !url.isEmpty else { return }
        do { try await model.client?.createPodcastChannel(url: url); await load() } catch { failed = true }
    }

    private func remove(_ c: PodcastChannel) async {
        do { try await model.client?.deletePodcastChannel(id: c.id); channels.removeAll { $0.id == c.id } } catch { failed = true }
    }
}

enum PodcastSection: Hashable { case newest }

/// Строка выпуска: точка «не прослушано», дата, длительность, остаток.
struct EpisodeRow: View {
    let episode: PodcastEpisode
    let channelTitle: String?
    @Environment(AppModel.self) private var model
    private let store = PodcastStore.shared

    var body: some View {
        let listened = store.isListened(episode)
        let position = store.position(of: episode)
        HStack(spacing: 12) {
            Circle().fill(listened || !episode.isDownloaded ? .clear : Color.accentColor).frame(width: 8, height: 8)
            VStack(alignment: .leading, spacing: 3) {
                if let date = episode.published {
                    Text(date, format: .dateTime.day().month(.abbreviated).year()).font(.caption).foregroundStyle(.secondary)
                }
                Text(episode.title).lineLimit(2).foregroundStyle(listened ? .secondary : .primary)
                HStack(spacing: 6) {
                    if episode.isDownloading { ProgressView().controlSize(.mini); Text("Downloading on server…") }
                    else if !episode.isDownloaded { Image(systemName: "icloud.and.arrow.down"); Text("Not downloaded") }
                    else if let d = episode.duration {
                        if position > 5, !listened { Text("\(formatEpisodeDuration(max(d - Int(position), 0))) left") }
                        else { Text(formatEpisodeDuration(d)) }
                    }
                }
                .font(.subheadline).foregroundStyle(.secondary)
            }
            Spacer(minLength: 0)
        }
        .contentShape(.rect)
    }
}

/// Последние выпуски всех каналов.
struct NewestEpisodesView: View {
    @Environment(AppModel.self) private var model
    @State private var episodes: [PodcastEpisode] = []
    @State private var loaded = false

    var body: some View {
        List(episodes) { e in
            NavigationLink(value: EpisodeRoute(episode: e, channelTitle: nil, channelID: e.channelId)) {
                EpisodeRow(episode: e, channelTitle: nil)
            }
        }
        .listStyle(.plain)
        .overlay {
            if !loaded { ProgressView() }
            else if episodes.isEmpty { ContentUnavailableView("No New Episodes", systemImage: "sparkles") }
        }
        .navigationTitle("New Episodes")
        .navigationBarTitleDisplayMode(.inline)
        .task { episodes = (try? await model.client?.newestPodcasts()) ?? []; loaded = true }
    }
}
