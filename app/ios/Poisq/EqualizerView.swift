import SwiftUI
import UniformTypeIdentifiers
import PoisqPlayback

/// Эквалайзер: включение, пресеты по категориям, кривая отклика, десять полос, предусилитель,
/// автоподбор по жанру, свои пресеты и импорт профилей коррекции наушников (AutoEq).
struct EqualizerView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var category: EQPreset.Category = .basic
    @State private var saving = false
    @State private var newName = ""
    @State private var importing = false
    @State private var importFailed = false

    private var eq: Equalizer { model.equalizer }

    var body: some View {
        @Bindable var eq = model.equalizer
        List {
            Section {
                Toggle("Equalizer", systemImage: "slider.vertical.3", isOn: $eq.isEnabled)
            } footer: {
                Text("Shapes the sound of everything you play. A limiter keeps strong boosts from distorting.")
            }

            if eq.isEnabled {
                Section {
                    curve
                        .frame(height: 120)
                        .listRowInsets(EdgeInsets(top: 12, leading: 16, bottom: 4, trailing: 16))
                    if eq.bands.count == EQPreset.graphicFrequencies.count {
                        sliders.listRowInsets(EdgeInsets(top: 4, leading: 8, bottom: 12, trailing: 8))
                    } else {
                        Label("Headphone correction is active. Bands are set by the profile.", systemImage: "headphones")
                            .font(.footnote).foregroundStyle(.secondary)
                    }
                } header: {
                    HStack {
                        Text("Current")
                        Spacer()
                        (eq.activeIsBuiltIn ? Text(LocalizedStringKey(eq.activeName)) : Text(verbatim: eq.activeName)).textCase(nil).foregroundStyle(.tint)
                    }
                }

                presetsSection

                Section {
                    HStack {
                        Text("Preamp")
                        Spacer()
                        Text(String(format: "%+.1f dB", eq.effectivePreamp)).monospacedDigit().foregroundStyle(.secondary)
                    }
                    Slider(value: Binding(get: { Double(eq.preamp) }, set: { eq.preamp = Float($0) }), in: -12...12, step: 0.5)
                    Toggle("Prevent Clipping", isOn: $eq.headroom)
                    Toggle("Match Genre Automatically", isOn: $eq.autoByGenre)
                } footer: {
                    Text("Prevent Clipping lowers the overall level by the strongest boost. Match Genre picks a preset for each song from its genre tag.")
                }

                Section {
                    Button("Save as Preset…", systemImage: "plus") { newName = ""; saving = true }
                    Button("Import Headphone Profile…", systemImage: "square.and.arrow.down") { importing = true }
                    Button("Reset", systemImage: "arrow.counterclockwise", role: .destructive) { withAnimation { eq.reset() } }
                } footer: {
                    Text("Import a ParametricEQ.txt profile from AutoEq to correct your headphones.")
                }
            }
        }
        .navigationTitle("Equalizer")
        .navigationBarTitleDisplayMode(.inline)
        .animation(.default, value: eq.isEnabled)
        .sensoryFeedback(.selection, trigger: eq.selectedID)
        .alert("Save Preset", isPresented: $saving) {
            TextField("Name", text: $newName)
            Button("Cancel", role: .cancel) {}
            Button("Save") {
                let n = newName.trimmingCharacters(in: .whitespaces)
                if !n.isEmpty { eq.saveCurrent(as: n); category = .custom }
            }
        }
        .alert("Couldn't read the profile", isPresented: $importFailed) { Button("OK", role: .cancel) {} } message: {
            Text("Use a ParametricEQ.txt file exported by AutoEq.")
        }
        .fileImporter(isPresented: $importing, allowedContentTypes: [.plainText, .text]) { result in
            guard case .success(let url) = result else { return }
            let access = url.startAccessingSecurityScopedResource()
            defer { if access { url.stopAccessingSecurityScopedResource() } }
            let name = url.deletingPathExtension().lastPathComponent.replacingOccurrences(of: "ParametricEQ", with: "").trimmingCharacters(in: .whitespaces)
            if let text = try? String(contentsOf: url, encoding: .utf8),
               let preset = EQPreset.parseAutoEq(text, name: name.isEmpty ? "Headphones" : name) {
                eq.addImported(preset); category = .headphone
            } else { importFailed = true }
        }
    }

    // MARK: Пресеты

    private var presetsSection: some View {
        Section("Presets") {
            Picker("Category", selection: $category) {
                ForEach(EQPreset.Category.allCases.filter { $0 == .basic || $0 == .genre || $0 == .situation || hasPresets(in: $0) }, id: \.self) { c in
                    Text(title(for: c)).tag(c)
                }
            }
            .pickerStyle(.segmented)
            .listRowBackground(Color.clear).listRowInsets(EdgeInsets())

            ForEach(eq.allPresets.filter { $0.category == category }) { p in
                Button { withAnimation { eq.select(p) } } label: {
                    HStack {
                        if p.builtIn { Text(LocalizedStringKey(p.name)) } else { Text(verbatim: p.name) }
                        Spacer()
                        if eq.selectedID == p.id { Image(systemName: "checkmark").foregroundStyle(.tint) }
                    }
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .swipeActions { if !p.builtIn { Button("Delete", role: .destructive) { eq.delete(p) } } }
            }
        }
    }

    private func hasPresets(in c: EQPreset.Category) -> Bool { eq.allPresets.contains { $0.category == c } }

    private func title(for c: EQPreset.Category) -> LocalizedStringKey {
        switch c { case .basic: "Basic"; case .genre: "Genres"; case .situation: "Situations"; case .headphone: "Headphones"; case .custom: "Mine" }
    }

    // MARK: Кривая и полосы

    /// Кривая суммарной АЧХ: сглаженная сумма колоколов полос в логарифмической шкале частот.
    private var curve: some View {
        Canvas { ctx, size in
            let range: CGFloat = 12
            func x(_ f: Float) -> CGFloat { CGFloat((log10(Double(f)) - log10(20)) / (log10(20000) - log10(20))) * size.width }
            func y(_ db: CGFloat) -> CGFloat { size.height / 2 - db / range * (size.height / 2) }
            // Сетка: 0 дБ и ±6 дБ.
            for db in [-6.0, 0, 6] {
                var line = Path(); line.move(to: CGPoint(x: 0, y: y(db))); line.addLine(to: CGPoint(x: size.width, y: y(db)))
                ctx.stroke(line, with: .color(.secondary.opacity(db == 0 ? 0.5 : 0.2)), lineWidth: 0.5)
            }
            var path = Path()
            let steps = 120
            for i in 0...steps {
                let t = Double(i) / Double(steps)
                let f = Float(pow(10, log10(20) + t * (log10(20000) - log10(20))))
                let db = max(-range, min(range, response(at: f)))
                let p = CGPoint(x: CGFloat(t) * size.width, y: y(db))
                if i == 0 { path.move(to: p) } else { path.addLine(to: p) }
            }
            var fill = path
            fill.addLine(to: CGPoint(x: size.width, y: y(0))); fill.addLine(to: CGPoint(x: 0, y: y(0))); fill.closeSubpath()
            ctx.fill(fill, with: .color(.accentColor.opacity(0.18)))
            ctx.stroke(path, with: .color(.accentColor), style: StrokeStyle(lineWidth: 2.5, lineCap: .round, lineJoin: .round))
            _ = x
        }
        .accessibilityHidden(true)
    }

    private func response(at f: Float) -> CGFloat {
        var total: Float = 0
        for b in eq.bands {
            let octaves = log2(f / b.frequency)
            switch b.kind {
            case .peak:
                let width = max(0.35, 1.3 / max(b.q, 0.3))
                total += b.gain * exp(-(octaves * octaves) / (2 * width * width * 0.5))
            case .lowShelf: total += b.gain / (1 + exp(octaves * 3))
            case .highShelf: total += b.gain / (1 + exp(-octaves * 3))
            }
        }
        return CGFloat(total)
    }

    /// Десять вертикальных ползунков с подписями частот.
    private var sliders: some View {
        HStack(spacing: 0) {
            ForEach(Array(eq.bands.enumerated()), id: \.offset) { i, band in
                VStack(spacing: 6) {
                    Text(String(format: "%+.0f", band.gain)).font(.caption2.monospacedDigit()).foregroundStyle(.secondary)
                    Slider(value: Binding(get: { Double(band.gain) }, set: { eq.setGain(Float($0), at: i) }), in: -12...12)
                        .frame(width: 150)
                        .rotationEffect(.degrees(-90))
                        .frame(width: 30, height: 150)
                        .accessibilityLabel(Text(Self.label(band.frequency)))
                        .accessibilityValue(Text(String(format: "%+.1f dB", band.gain)))
                    Text(Self.label(band.frequency)).font(.caption2).foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity)
            }
        }
    }

    private static func label(_ f: Float) -> String { f >= 1000 ? "\(Int(f / 1000))k" : "\(Int(f))" }
}
