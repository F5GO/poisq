import SwiftUI
import AVKit
import PoisqCore

/// Где играть: этот iPhone или колонка Music Assistant (трансляция через прокси poisq).
/// Как окно выбора устройства в «Музыке»: список устройств с галочкой, у выбранной колонки — громкость.
struct CastPickerView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var volume: Double = 0
    @State private var loading = true

    var body: some View {
        NavigationStack {
            List {
                Section {
                    row(Text("This iPhone"), systemImage: "iphone", selected: model.castZone == nil) { model.cast(to: nil) }
                }
                Section {
                    ForEach(model.castZones) { zone in
                        row(Text(verbatim: zone.name), systemImage: "hifispeaker", selected: model.castZone?.id == zone.id) { model.cast(to: zone) }
                        if model.castZone?.id == zone.id, zone.volume >= 0 {
                            Slider(value: $volume, in: 0...100) { Text("Volume") } minimumValueLabel: {
                                Image(systemName: "speaker.fill")
                            } maximumValueLabel: {
                                Image(systemName: "speaker.wave.3.fill")
                            } onEditingChanged: { editing in
                                if !editing { model.setCastVolume(Int(volume)) }
                            }
                            .foregroundStyle(.secondary)
                        }
                    }
                } header: {
                    Text("Speakers")
                } footer: {
                    if !loading, model.castZones.isEmpty { Text("No speakers available. Speakers are set up in poisq Studio.") }
                }
                // Список устройств AirPlay приложению недоступен — его показывает только системное окно, как в «Музыке».
                // Звук по AirPlay идёт с iPhone, поэтому трансляцию на колонку сначала выключаем.
                Section {
                    AirPlayRow { model.cast(to: nil); model.player?.activateAudioSession() }
                }
            }
            .navigationTitle("Play On")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done", systemImage: "checkmark") { dismiss() } } }
            .overlay { if loading, model.castZones.isEmpty { ProgressView() } }
        }
        .presentationDetents([.medium, .large])
        .task {
            await model.loadCastZones()
            loading = false
        }
        .onChange(of: model.castZone?.id, initial: true) { volume = Double(max(model.castZone?.volume ?? 0, 0)) }
    }

    private func row(_ title: Text, systemImage: String, selected: Bool, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack {
                Label { title } icon: { Image(systemName: systemImage) }.foregroundStyle(.primary)
                Spacer()
                if selected { Image(systemName: "checkmark").foregroundStyle(Color.accentColor).fontWeight(.semibold) }
            }
        }
        .tint(.primary)
        .accessibilityAddTraits(selected ? .isSelected : [])
    }
}

/// Строка «AirPlay и Bluetooth»: открывает системное окно выбора устройства.
/// Открыть его умеет только AVRoutePickerView: создаём его на время нажатия, кладём в окно и нажимаем программно.
private struct AirPlayRow: View {
    let before: () -> Void

    var body: some View {
        Button {
            before()
            // Окно открываем, когда звук уже переключён на iPhone: смена движка и аудиосессии во время показа оставляла список устройств пустым.
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.3) {
                // shortcut: у AVRoutePickerView нет публичного способа открыть окно, повторяем события нажатия его внутренней кнопки; если Apple это сломает — вернуть видимый AVRoutePickerView
                guard let window = UIApplication.shared.connectedScenes.compactMap({ ($0 as? UIWindowScene)?.keyWindow }).first else { return }
                let picker = AVRoutePickerView(frame: CGRect(x: -100, y: 0, width: 1, height: 1))   // окно открывается только от view, который в иерархии
                window.addSubview(picker)
                let button = Self.button(in: picker)
                button?.sendActions(for: .touchUpInside)
                button?.sendActions(for: .primaryActionTriggered)
                DispatchQueue.main.asyncAfter(deadline: .now() + 1) { picker.removeFromSuperview() }
            }
        } label: {
            Label("AirPlay & Bluetooth", systemImage: "airplay.audio").foregroundStyle(.primary)
        }
        .tint(.primary)
    }

    private static func button(in view: UIView) -> UIButton? {
        for sub in view.subviews {
            if let b = sub as? UIButton { return b }
            if let b = button(in: sub) { return b }
        }
        return nil
    }
}
