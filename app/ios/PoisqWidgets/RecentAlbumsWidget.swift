import SwiftUI
import WidgetKit

struct RecentAlbumsEntry: TimelineEntry {
    let date: Date
    let albums: [SharedAlbum]
}

struct RecentAlbumsProvider: TimelineProvider {
    func placeholder(in context: Context) -> RecentAlbumsEntry {
        RecentAlbumsEntry(date: .now, albums: (1...6).map { SharedAlbum(id: "\($0)", name: "Album", artist: "Artist") })
    }
    func getSnapshot(in context: Context, completion: @escaping (RecentAlbumsEntry) -> Void) {
        completion(RecentAlbumsEntry(date: .now, albums: SharedStore.recentAlbums))
    }
    func getTimeline(in context: Context, completion: @escaping (Timeline<RecentAlbumsEntry>) -> Void) {
        // Кэш обновляет приложение; на случай, если оно давно не запускалось, перечитываем раз в несколько часов.
        completion(Timeline(entries: [RecentAlbumsEntry(date: .now, albums: SharedStore.recentAlbums)],
                            policy: .after(.now.addingTimeInterval(6 * 3600))))
    }
}

struct RecentAlbumsWidget: Widget {
    var body: some WidgetConfiguration {
        StaticConfiguration(kind: "RecentAlbums", provider: RecentAlbumsProvider()) { entry in
            RecentAlbumsView(entry: entry)
                .containerBackground(.fill.tertiary, for: .widget)
        }
        .configurationDisplayName("Recent Albums")
        .description("Recently added albums. Tap one to open it.")
        .supportedFamilies([.systemMedium, .systemLarge])
    }
}

struct RecentAlbumsView: View {
    let entry: RecentAlbumsEntry
    @Environment(\.widgetFamily) private var family

    private var count: Int { family == .systemLarge ? 6 : 4 }
    private var columns: Int { family == .systemLarge ? 3 : 4 }

    var body: some View {
        if entry.albums.isEmpty {
            ContentUnavailableView("No Albums", systemImage: "square.stack", description: Text("Open poisq to load your albums."))
        } else {
            let items = Array(entry.albums.prefix(count))
            Grid(horizontalSpacing: 10, verticalSpacing: 10) {
                ForEach(0..<((items.count + columns - 1) / columns), id: \.self) { row in
                    GridRow {
                        ForEach(items.dropFirst(row * columns).prefix(columns)) { album in
                            Link(destination: DeepLink.album(id: album.id, name: album.name)) {
                                VStack(alignment: .leading, spacing: 3) {
                                    SharedCover(file: album.coverFile)
                                        .aspectRatio(1, contentMode: .fit)
                                        .clipShape(.rect(cornerRadius: 10, style: .continuous))
                                    Text(album.name).font(.caption.weight(.semibold)).lineLimit(1)
                                    if family == .systemLarge, let artist = album.artist {
                                        Text(artist).font(.caption2).foregroundStyle(.secondary).lineLimit(1)
                                    }
                                }
                            }
                        }
                    }
                }
            }
        }
    }
}
