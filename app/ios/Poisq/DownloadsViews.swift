import SwiftUI
import PoisqCore
import PoisqPlayback

/// Кнопка загрузки для набора треков (альбом, плейлист): скачать, показать прогресс, удалить.
struct DownloadButton: View {
    let songs: [Song]
    @Environment(AppModel.self) private var model
    @State private var confirmRemove = false

    private var state: State {
        guard !songs.isEmpty, let sid = model.activeID else { return .none }
        let done = songs.filter { model.downloads.isDownloaded($0, serverID: sid) }.count
        if done == songs.count { return .done }
        if songs.contains(where: { model.downloads.hasFailed($0, serverID: sid) }) && songs.allSatisfy({ model.downloads.progress(for: $0, serverID: sid) == nil }) { return .failed }
        let running = songs.compactMap { model.downloads.progress(for: $0, serverID: sid) }
        if !running.isEmpty {
            let total = (Double(done) + running.reduce(0, +)) / Double(songs.count)
            return .running(total)
        }
        return .none
    }
    private enum State: Equatable { case none, running(Double), done, failed }

    var body: some View {
        Button(action: tapped) {
            switch state {
            case .none: Image(systemName: "arrow.down.circle")
            case .done: Image(systemName: "arrow.down.circle.fill").foregroundStyle(.tint)
            case .failed: Image(systemName: "exclamationmark.arrow.circlepath").foregroundStyle(.red)
            case .running(let p):
                ZStack {
                    Circle().stroke(.secondary.opacity(0.3), lineWidth: 2.5)
                    Circle().trim(from: 0, to: max(p, 0.05)).stroke(.tint, style: .init(lineWidth: 2.5, lineCap: .round)).rotationEffect(.degrees(-90))
                    Image(systemName: "stop.fill").font(.system(size: 8))
                }
                .frame(width: 20, height: 20)
            }
        }
        .disabled(songs.isEmpty)
        .contentTransition(.symbolEffect(.replace))
        .accessibilityLabel(state == .done ? "Remove Download" : "Download")
        .confirmationDialog("Remove this download?", isPresented: $confirmRemove, titleVisibility: .visible) {
            Button("Remove Download", role: .destructive) { model.removeDownloads(songs) }
        }
        .sensoryFeedback(.success, trigger: state == .done)
    }

    private func tapped() {
        switch state {
        case .none, .failed: model.download(songs)                // при ошибке нажатие повторяет загрузку
        case .running: model.removeDownloads(songs)          // нажатие во время загрузки отменяет её
        case .done: confirmRemove = true
        }
    }
}

/// Загруженное на устройство: по альбомам; нажатие запускает, свайп удаляет.
struct DownloadedView: View {
    @Environment(AppModel.self) private var model

    private var groups: [(album: String, songs: [Song])] {
        Dictionary(grouping: model.downloadedSongs) { $0.album ?? "" }
            .map { ($0.key, $0.value.sorted { ($0.track ?? 0) < ($1.track ?? 0) }) }
            .sorted { $0.album.localizedCaseInsensitiveCompare($1.album) == .orderedAscending }
    }

    var body: some View {
        List {
            let running = model.activeID.map { model.downloads.activeDownloads(serverID: $0) } ?? []
            if !running.isEmpty {
                Section("Downloading") {
                    ForEach(running, id: \.song.id) { item in
                        HStack(spacing: 12) {
                            CoverArtView(id: item.song.coverArt, size: 40, radius: 6)
                            VStack(alignment: .leading, spacing: 4) {
                                Text(item.song.title).lineLimit(1)
                                ProgressView(value: item.progress)
                            }
                            Button("Cancel", systemImage: "xmark.circle.fill") { model.removeDownloads([item.song]) }
                                .labelStyle(.iconOnly).buttonStyle(.borderless).foregroundStyle(.secondary)
                        }
                    }
                }
            }
            ForEach(groups, id: \.album) { group in
                Section(group.album) {
                    ForEach(Array(group.songs.enumerated()), id: \.element.id) { index, song in
                        Button { model.player?.play(group.songs, startAt: index) } label: {
                            MediaRow(coverID: song.coverArt, title: song.title, subtitle: song.artist)
                        }
                        .buttonStyle(.plain)
                        .swipeActions { Button("Remove", role: .destructive) { model.removeDownloads([song]) } }
                    }
                }
            }
            if !model.downloadedSongs.isEmpty {
                Section { } footer: { Text("Storage: \(storage)") }
            }
        }
        .listStyle(.plain)
        .overlay {
            if model.downloadedSongs.isEmpty {
                ContentUnavailableView("No Downloads", systemImage: "arrow.down.circle", description: Text("Songs you download will appear here."))
            }
        }
        .navigationTitle("Downloaded")
        .navigationBarTitleDisplayMode(.inline)
    }

    private var storage: String {
        ByteCountFormatter.string(fromByteCount: model.downloads.totalBytes(serverID: model.activeID), countStyle: .file)
    }
}
