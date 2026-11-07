import SwiftUI

/// Мягкий голубо-розовый фон из палитры (#0593C6, #DB3996, #FCFFFF). Для экрана входа и, позже, плеера.
struct PaletteBackdrop: View {
    @Environment(\.colorScheme) private var scheme

    private static let blue = Color(red: 5/255, green: 147/255, blue: 198/255)
    private static let pink = Color(red: 219/255, green: 57/255, blue: 150/255)

    var body: some View {
        let base: Color = scheme == .dark ? .black : Color(red: 252/255, green: 1, blue: 1)
        MeshGradient(
            width: 3, height: 3,
            points: [[0, 0], [0.5, 0], [1, 0],
                     [0, 0.5], [0.5, 0.5], [1, 0.5],
                     [0, 1], [0.5, 1], [1, 1]],
            // Центр чистый: голубой уходит в правый верхний угол, розовый — в левый нижний.
            colors: [base, base, Self.blue,
                     base, base, base,
                     Self.pink, base, base]
        )
        .opacity(scheme == .dark ? 0.35 : 0.22)
        .background(base)
        .ignoresSafeArea()
    }
}
