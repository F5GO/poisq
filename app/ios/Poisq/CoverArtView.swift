import SwiftUI

/// Обложка с заглушкой. `size` — сторона в пунктах; запрашиваем один из трёх размеров, чтобы кеш попадал чаще.
struct CoverArtView: View {
    let id: String?
    let size: CGFloat
    var radius: CGFloat = 10
    @Environment(AppModel.self) private var model
    @State private var image: UIImage?

    private var pixels: Int { size <= 64 ? 200 : size <= 220 ? 500 : 900 }

    var body: some View {
        ZStack {
            if let image {
                Image(uiImage: image).resizable().scaledToFill().transition(.opacity)
            } else {
                Rectangle().fill(.quaternary)
                    .overlay { Image(systemName: "music.note").font(.system(size: size * 0.3)).foregroundStyle(.tertiary) }
            }
        }
        .frame(width: size, height: size)
        .clipShape(.rect(cornerRadius: radius, style: .continuous))
        .overlay { RoundedRectangle(cornerRadius: radius, style: .continuous).strokeBorder(.primary.opacity(0.08), lineWidth: 0.5) }
        .animation(.smooth, value: image)
        .task(id: id) {
            guard let id else { image = nil; return }
            image = await model.coverImage(id, pixels: pixels)
        }
        .accessibilityHidden(true)
    }
}
