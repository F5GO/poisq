import SwiftUI
import VisionKit

/// Сканер QR сопряжения poisq на системном DataScannerViewController. Без камеры (симулятор) — вставка кода из буфера.
struct SecureQRScannerSheet: View {
    let onCode: (String) -> Void
    @Environment(\.dismiss) private var dismiss

    private var hasCamera: Bool { DataScannerViewController.isSupported && DataScannerViewController.isAvailable }

    var body: some View {
        NavigationStack {
            Group {
                if hasCamera {
                    QRScannerView { code in
                        onCode(code)
                        dismiss()
                    }
                    .ignoresSafeArea()
                } else {
                    ContentUnavailableView {
                        Label("Camera Unavailable", systemImage: "camera")
                    } description: {
                        Text("Copy the code from poisq Studio and paste it here.")
                    } actions: {
                        PasteButton(payloadType: String.self) { values in
                            guard let code = values.first else { return }
                            onCode(code)
                            dismiss()
                        }
                    }
                }
            }
            .navigationTitle("Scan poisq QR")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel", systemImage: "xmark") { dismiss() } }
                if hasCamera {
                    ToolbarItem(placement: .confirmationAction) {
                        PasteButton(payloadType: String.self) { values in
                            guard let code = values.first else { return }
                            onCode(code)
                            dismiss()
                        }
                        .labelStyle(.iconOnly)
                    }
                }
            }
        }
    }
}

private struct QRScannerView: UIViewControllerRepresentable {
    let onCode: (String) -> Void

    func makeUIViewController(context: Context) -> DataScannerViewController {
        let scanner = DataScannerViewController(recognizedDataTypes: [.barcode(symbologies: [.qr])],
                                                qualityLevel: .balanced, isHighlightingEnabled: true)
        scanner.delegate = context.coordinator
        try? scanner.startScanning()
        return scanner
    }

    func updateUIViewController(_ controller: DataScannerViewController, context: Context) {}

    static func dismantleUIViewController(_ controller: DataScannerViewController, coordinator: Coordinator) {
        controller.stopScanning()
    }

    func makeCoordinator() -> Coordinator { Coordinator(onCode: onCode) }

    final class Coordinator: NSObject, DataScannerViewControllerDelegate {
        private let onCode: (String) -> Void
        private var delivered = false
        init(onCode: @escaping (String) -> Void) { self.onCode = onCode }

        func dataScanner(_ scanner: DataScannerViewController, didAdd items: [RecognizedItem], allItems: [RecognizedItem]) {
            // Берём первый QR с текстом poisq; чужие коды игнорируем, чтобы не закрывать сканер случайным кодом рядом.
            for case .barcode(let code) in items {
                guard !delivered, let text = code.payloadStringValue, text.hasPrefix("poisq-secure:") else { continue }
                delivered = true
                onCode(text)
            }
        }
    }
}
