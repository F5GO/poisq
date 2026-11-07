// swift-tools-version:6.2
import PackageDescription

let package = Package(
    name: "PoisqKit",
    platforms: [.iOS(.v26), .macOS(.v26)],
    products: [
        .library(name: "PoisqCore", targets: ["PoisqCore"]),
        .library(name: "PoisqPlayback", targets: ["PoisqPlayback"]),
    ],
    dependencies: [
        // MIT. Единственная внешняя зависимость; текст лицензии показываем в настройках.
        .package(url: "https://github.com/dimitris-c/AudioStreaming.git", from: "1.4.5"),
    ],
    targets: [
        // Модели, клиент Subsonic, Capabilities. Без UI и без аудио — переносится на Android через Skip.
        .target(name: "PoisqCore"),
        .target(
            name: "PoisqPlayback",
            dependencies: [
                "PoisqCore",
                .product(name: "AudioStreaming", package: "AudioStreaming"),
            ]
        ),
        .testTarget(name: "PoisqCoreTests", dependencies: ["PoisqCore"]),
        .testTarget(name: "PoisqPlaybackTests", dependencies: ["PoisqPlayback", "PoisqCore"]),
    ]
)
