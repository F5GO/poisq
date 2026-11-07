import SwiftUI
import WidgetKit

@main
struct PoisqWidgetsBundle: WidgetBundle {
    var body: some Widget {
        NowPlayingWidget()
        RecentAlbumsWidget()
        PlaybackLiveActivity()
    }
}

/// Обложка из общего кэша (App Group); без файла — системная заглушка.
struct SharedCover: View {
    let file: String?
    var body: some View {
        if let url = PoisqShared.coverURL(file), let image = UIImage(contentsOfFile: url.path) {
            Image(uiImage: image).resizable().scaledToFill()
        } else {
            Rectangle().fill(.quaternary)
                .overlay { Image(systemName: "music.note").font(.title2).foregroundStyle(.tertiary) }
        }
    }
}
