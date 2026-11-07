import SwiftUI
import WidgetKit
import ActivityKit
import AppIntents

struct PlaybackLiveActivity: Widget {
    var body: some WidgetConfiguration {
        ActivityConfiguration(for: PlaybackActivityAttributes.self) { context in
            // Экран блокировки и баннер.
            HStack(spacing: 12) {
                SharedCover(file: context.state.coverFile).frame(width: 56, height: 56)
                    .clipShape(.rect(cornerRadius: 10, style: .continuous))
                VStack(alignment: .leading, spacing: 2) {
                    Text(context.state.title).font(.headline).lineLimit(1)
                    Text(context.state.artist).font(.subheadline).foregroundStyle(.secondary).lineLimit(1)
                }
                Spacer(minLength: 0)
                Controls(isPlaying: context.state.isPlaying, includePrevious: false)
            }
            .padding()
            .activityBackgroundTint(nil)
            .widgetURL(DeepLink.player)
        } dynamicIsland: { context in
            DynamicIsland {
                DynamicIslandExpandedRegion(.leading) {
                    SharedCover(file: context.state.coverFile).frame(width: 52, height: 52)
                        .clipShape(.rect(cornerRadius: 10, style: .continuous))
                }
                DynamicIslandExpandedRegion(.center) {
                    VStack(alignment: .leading, spacing: 0) {
                        Text(context.state.title).font(.headline).lineLimit(1)
                        Text(context.state.artist).font(.subheadline).foregroundStyle(.secondary).lineLimit(1)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                }
                DynamicIslandExpandedRegion(.bottom) {
                    Controls(isPlaying: context.state.isPlaying, includePrevious: true).frame(maxWidth: .infinity)
                }
            } compactLeading: {
                SharedCover(file: context.state.coverFile).frame(width: 22, height: 22)
                    .clipShape(.rect(cornerRadius: 5, style: .continuous))
            } compactTrailing: {
                Image(systemName: context.state.isPlaying ? "waveform" : "pause.fill")
                    .symbolEffect(.variableColor.iterative, isActive: context.state.isPlaying)
            } minimal: {
                Image(systemName: context.state.isPlaying ? "waveform" : "pause.fill")
            }
            .widgetURL(DeepLink.player)
        }
    }
}

private struct Controls: View {
    let isPlaying: Bool
    let includePrevious: Bool
    var body: some View {
        HStack(spacing: 26) {
            if includePrevious { Button(intent: PreviousTrackIntent()) { Image(systemName: "backward.fill") } }
            Button(intent: PlayPauseIntent()) {
                Image(systemName: isPlaying ? "pause.fill" : "play.fill").contentTransition(.symbolEffect(.replace))
            }
            Button(intent: NextTrackIntent()) { Image(systemName: "forward.fill") }
        }
        .buttonStyle(.plain).font(.title2)
    }
}
