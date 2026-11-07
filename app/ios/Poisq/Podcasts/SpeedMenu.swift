import SwiftUI
import PoisqPlayback

/// Выбор скорости речи 0.75–2×. Применяется ко всем подкастам; музыка не затрагивается.
struct SpeedMenu: View {
    let player: Player
    var body: some View {
        Menu {
            Picker("Speed", selection: Binding(get: { player.speechRate }, set: { player.speechRate = $0 })) {
                ForEach(speechRates, id: \.self) { Text(rateLabel($0)).tag($0) }
            }
        } label: {
            Text(rateLabel(player.speechRate)).font(.subheadline.weight(.semibold).monospacedDigit())
                .frame(minWidth: 44, minHeight: 44)
        }
        .accessibilityLabel("Speed")
    }
}

/// Управление в большом плеере для подкаста: скорость, назад 15 с, пауза, вперёд 30 с.
struct SpokenControls: View {
    let player: Player
    var body: some View {
        HStack {
            SpeedMenu(player: player).frame(width: 44, height: 44)
            Spacer()
            Button { player.seek(to: max(player.progress - 15, 0)) } label: { Image(systemName: "gobackward.15").font(.system(size: 32)) }
                .accessibilityLabel("Back 15 Seconds")
            Spacer()
            Button { player.togglePlayPause() } label: {
                Image(systemName: player.isPlaying ? "pause.fill" : "play.fill").font(.system(size: 46))
                    .contentTransition(.symbolEffect(.replace)).frame(width: 60)
            }
            .accessibilityLabel(player.isPlaying ? "Pause" : "Play")
            Spacer()
            Button { player.seek(to: min(player.progress + 30, max(player.duration - 1, 0))) } label: { Image(systemName: "goforward.30").font(.system(size: 32)) }
                .accessibilityLabel("Forward 30 Seconds")
            Spacer()
            Color.clear.frame(width: 44, height: 44)
        }
        .buttonStyle(.borderless)
        .foregroundStyle(.primary)
    }
}

/// Радио: один большой Play/Pause, без перемотки и очереди.
struct RadioControls: View {
    let player: Player
    var body: some View {
        Button { player.togglePlayPause() } label: {
            Image(systemName: player.isPlaying ? "pause.fill" : "play.fill").font(.system(size: 46))
                .contentTransition(.symbolEffect(.replace)).frame(width: 60)
        }
        .buttonStyle(.borderless)
        .foregroundStyle(.primary)
        .accessibilityLabel(player.isPlaying ? "Pause" : "Play")
    }
}

/// Вместо полосы прогресса у радио — отметка прямого эфира.
struct LiveIndicator: View {
    var body: some View {
        HStack {
            Image(systemName: "dot.radiowaves.left.and.right")
            Text("Live").font(.caption.weight(.semibold))
            Spacer()
        }
        .foregroundStyle(.secondary)
        .frame(height: 30)
    }
}
