import SwiftUI

/// Загруженная часть трека на системной полосе (ProgressView, Slider): дорожка темнеет по мере загрузки,
/// как в Музыке и YouTube. Системные полосы буфер не показывают, а их дорожка непрозрачна, поэтому слой
/// кладётся поверх и только на участок от сыгранного до загруженного: заливка акцентом остаётся нетронутой.
struct BufferedTrack: View {
    /// Сколько сыграно, 0…1.
    let played: Double
    /// Сколько загружено, 0…1; nil — неизвестно, слой не рисуется.
    let loaded: Double?
    /// Толщина дорожки системной полосы.
    let thickness: CGFloat

    var body: some View {
        GeometryReader { g in
            let start = min(max(played, 0), 1)
            if let loaded, loaded > start {
                Capsule()
                    .fill(Color.primary.opacity(0.25))
                    .frame(width: g.size.width * (min(loaded, 1) - start), height: thickness)
                    .offset(x: g.size.width * start)
                    .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .leading)
                    .animation(.smooth, value: loaded)
            }
        }
        .allowsHitTesting(false)
        .accessibilityHidden(true)
    }
}
