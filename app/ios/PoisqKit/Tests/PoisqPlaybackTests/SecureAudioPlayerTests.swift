import AVFoundation
import AudioStreaming
import Foundation
import Testing
@testable import PoisqCore
@testable import PoisqPlayback

/// Трёхминутный FLAC (тихий синус), создаётся один раз на прогон тестов.
private let flacFile: URL = {
    let url = FileManager.default.temporaryDirectory.appending(path: "poisq-sine-180s.flac")
    if FileManager.default.fileExists(atPath: url.path) { return url }
    let rate = 44100.0
    let pcm = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: rate, channels: 2, interleaved: false)!
    let settings: [String: Any] = [AVFormatIDKey: kAudioFormatFLAC, AVSampleRateKey: rate, AVNumberOfChannelsKey: 2]
    let file = try! AVAudioFile(forWriting: url, settings: settings, commonFormat: .pcmFormatFloat32, interleaved: false)
    let frames = AVAudioFrameCount(rate)
    let buffer = AVAudioPCMBuffer(pcmFormat: pcm, frameCapacity: frames)!
    buffer.frameLength = frames
    for second in 0..<180 {
        for i in 0..<Int(frames) {
            let v = Float(0.05 * sin(2 * Double.pi * 440 * Double(second * Int(frames) + i) / rate))
            buffer.floatChannelData![0][i] = v
            buffer.floatChannelData![1][i] = v
        }
        try! file.write(from: buffer)
    }
    return url
}()

/// Канал из файла: отдаёт куски настоящего FLAC с задержкой, как сеть.
private final class FileFetcher: SecureMediaFetching, @unchecked Sendable {
    let file: Data
    init() throws { file = try Data(contentsOf: flacFile) }
    func mediaChunk(for url: URL, offset: Int, length: Int) async throws -> SecureMediaChunk {
        try? await Task.sleep(for: .milliseconds(30))
        guard offset < file.count else { return SecureMediaChunk(data: Data(), total: file.count, pastEnd: true) }
        return SecureMediaChunk(data: file.subdata(in: offset..<min(file.count, offset + length)), total: file.count, pastEnd: false)
    }
}

private let format = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: 44100, channels: 2, interleaved: true)!
private let url = URL(string: "poisq-secure://stream?id=sine&suffix=flac")!

@MainActor
private func waitFor(_ seconds: Double, _ condition: () -> Bool) async -> Bool {
    let deadline = Date().addingTimeInterval(seconds)
    while !condition() {
        if Date() > deadline { return false }
        try? await Task.sleep(for: .milliseconds(50))
    }
    return true
}

/// Позиция двигается — значит, звук действительно идёт, а не только сменилось состояние.
@MainActor
private func advances(_ player: AudioPlayer, within seconds: Double = 4) async -> Bool {
    let start = player.progress
    return await waitFor(seconds) { player.progress > start + 0.7 }
}

/// Настоящий AudioPlayer со звуком: сценарии, которые ломались на iPhone. Без звукового устройства — POISQ_SKIP_AUDIO.
@Suite(.serialized, .enabled(if: ProcessInfo.processInfo.environment["POISQ_SKIP_AUDIO"] == nil))
@MainActor
struct SecureAudioPlayerTests {
    private func start(_ useFileSource: Bool = false) async throws -> AudioPlayer {
        let player = AudioPlayer()
        player.volume = 0
        if useFileSource {
            player.play(url: flacFile)
        } else {
            player.play(source: SecureAudioSource(url: url, fetcher: try FileFetcher(), queue: player.sourceQueue), entryId: url.absoluteString, format: format)
        }
        #expect(await waitFor(5) { player.state == .playing }, "playback did not start")
        return player
    }

    @Test(arguments: [false, true])
    func seeksDuringPlayback(fileSource: Bool) async throws {
        let player = try await start(fileSource)
        defer { player.stop() }
        #expect(await advances(player))
        player.seek(to: 100)
        #expect(await waitFor(5) { player.progress >= 99.5 })
        #expect(await advances(player), "playback stalled after seek (progress \(player.progress))")
    }

    @Test(arguments: [false, true])
    func restoresSavedPositionOnStart(fileSource: Bool) async throws {
        let player = try await start(fileSource)
        defer { player.stop() }
        player.seek(to: 75)          // как Player: сразу в момент «трек начал играть»
        #expect(await waitFor(5) { player.progress >= 74.5 })
        #expect(await advances(player), "playback stalled after restore (progress \(player.progress))")
    }

    @Test(arguments: [false, true])
    func pauseAndResumeAfterSeek(fileSource: Bool) async throws {
        let player = try await start(fileSource)
        defer { player.stop() }
        player.seek(to: 60)
        #expect(await advances(player), "no audio after seek")
        player.pause()
        #expect(await waitFor(3) { player.state == .paused })
        let paused = player.progress
        try await Task.sleep(for: .milliseconds(500))
        #expect(abs(player.progress - paused) < 0.2, "progress moved while paused")
        player.resume()
        #expect(await advances(player), "no audio after resume (state \(player.state) progress \(player.progress))")
    }

    // Только защищённый источник: файловый источник AudioStreaming в этом сценарии теряет звук (известное поведение библиотеки).
    @Test(arguments: [false])
    func pauseImmediatelyAfterSeek(fileSource: Bool) async throws {
        let player = try await start(fileSource)
        defer { player.stop() }
        player.seek(to: 90)
        player.pause()               // до того, как пошёл звук с нового места
        #expect(await waitFor(3) { player.state == .paused })
        try await Task.sleep(for: .milliseconds(300))
        player.resume()
        #expect(await waitFor(5) { player.progress >= 89.5 }, "seek lost after pause (progress \(player.progress))")
        #expect(await advances(player), "no audio after resume (state \(player.state) progress \(player.progress))")
    }
}
