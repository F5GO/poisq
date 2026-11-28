import SwiftUI
import PoisqCore

/// Состояние шифрования и эмодзи ключа сервера; по нажатию на эмодзи — подсказка и ключ целиком.
/// Общий для добавления сервера и его настроек, чтобы пользователь сверял одно и то же изображение.
struct SecureKeyBadge: View {
    let pairing: SecureServerPairing
    /// Канал только что подтвердил ключ. Иначе показываем, что шифрование включено, но не утверждаем, что проверено сейчас.
    let verified: Bool
    @State private var showDetails = false

    var body: some View {
        VStack(spacing: 8) {
            Group {
                if verified {
                    Label("Connection is secure", systemImage: "lock.shield.fill").foregroundStyle(.green)
                } else {
                    Label("Encryption is on", systemImage: "lock.fill").foregroundStyle(.secondary)
                }
            }
            .font(.subheadline.weight(.semibold))
            Button { showDetails = true } label: {
                Text(pairing.emojiFingerprint.joined(separator: " "))
                    .font(.title2)
            }
            .buttonStyle(.plain)
            .accessibilityLabel(Text("Key emoji"))
            .accessibilityHint(Text("Shows how to check the key"))
            .popover(isPresented: $showDetails) {
                VStack(alignment: .leading, spacing: 8) {
                    Text("Compare the emoji with poisq Studio. If they differ, don't sign in.")
                    Text("Key \(pairing.fingerprint)").font(.caption.monospaced()).foregroundStyle(.secondary)
                }
                .font(.footnote)
                .padding()
                .frame(idealWidth: 280)
                .presentationCompactAdaptation(.popover)
            }
        }
        .frame(maxWidth: .infinity)
    }
}
