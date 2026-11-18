// CoverArtView — компонент отображения обложки с мгновенным показом из памяти и плавным появлением только при сетевой загрузке.
import SwiftUI

/// Обложка с заглушкой. `size` — сторона в пунктах; запрашиваем один из трёх размеров, чтобы кеш попадал чаще.
struct CoverArtView: View {
    let id: String?
    let size: CGFloat
    var radius: CGFloat = 10
    @Environment(AppModel.self) private var model
    @State private var loaded: (key: String, image: UIImage?)?

    private var pixels: Int { size <= 64 ? 200 : size <= 220 ? 500 : 900 }

    private var taskKey: String {
        "\(model.activeID?.uuidString ?? "")|\(id ?? "")|\(pixels)"
    }

    var body: some View {
        let memoryHit = id.flatMap { model.cachedCoverImage($0, pixels: pixels) }
        let shown: UIImage? = if let loaded, loaded.key == taskKey {
            loaded.image ?? memoryHit
        } else {
            memoryHit
        }

        ZStack {
            if let shown {
                Image(uiImage: shown).resizable().scaledToFill().transition(.opacity)
            } else {
                Rectangle().fill(.quaternary)
                    .overlay { Image(systemName: "music.note").font(.system(size: size * 0.3)).foregroundStyle(.tertiary) }
            }
        }
        .frame(width: size, height: size)
        .clipShape(.rect(cornerRadius: radius, style: .continuous))
        .overlay { RoundedRectangle(cornerRadius: radius, style: .continuous).strokeBorder(.primary.opacity(0.08), lineWidth: 0.5) }
        .task(id: taskKey) {
            let key = taskKey
            guard let id else {
                loaded = nil
                return
            }
            if let hit = model.cachedCoverImage(id, pixels: pixels) {
                loaded = (key, hit)
                return
            }
            let result = await model.coverImageResult(id, pixels: pixels)
            guard !Task.isCancelled else { return }
            if result.fromCache {
                loaded = (key, result.image)
            } else {
                withAnimation(.smooth) {
                    loaded = (key, result.image)
                }
            }
        }
        .accessibilityHidden(true)
    }
}
