import SwiftUI

/// Загруженная часть трека на системной полосе (ProgressView, Slider): дорожка темнеет по мере загрузки,
/// как в Музыке и YouTube. Системные полосы буфер не показывают, а их дорожка непрозрачна, поэтому слой
/// кладётся поверх, а под заливкой акцентом вырезан: заливка остаётся нетронутой.
struct BufferedTrack: View {
    /// Сколько сыграно, 0…1.
    let played: Double
    /// Сколько загружено, 0…1; nil — неизвестно, слой не рисуется.
    let loaded: Double?
    /// Толщина дорожки системной полосы.
    let thickness: CGFloat

    var body: some View {
        GeometryReader { g in
            let w = g.size.width
            let played = min(max(self.played, 0), 1)
            if let loaded, loaded > played {
                // Слой идёт от начала дорожки, а сыгранная часть вырезана той же капсулой, что рисует система:
                // стык точно повторяет закругление заливки, без собственного скругления слева.
                Capsule()
                    .fill(Color.primary.opacity(0.25))
                    .frame(width: w * min(loaded, 1), height: thickness)
                    .mask {
                        ZStack(alignment: .leading) {
                            Rectangle()
                            Capsule().frame(width: w * played).blendMode(.destinationOut)
                        }
                        .compositingGroup()
                    }
                    .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .leading)
                    .animation(.smooth, value: loaded)
            }
        }
        .allowsHitTesting(false)
        .accessibilityHidden(true)
    }
}
