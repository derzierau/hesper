import AppKit
import QuartzCore
import os

/// Measures what the wall actually shows: a display link on the main thread
/// records every vsync interval (hitches = main-thread/compositor stalls) and,
/// on each tick, whether each tile's layer got a new frame from libghostty's
/// renderer (its IOSurface contents changed). Signposts mark every tick so a
/// trace in Instruments lines up with the numbers.
@MainActor
final class FrameProbe: NSObject {
    static let log = OSLog(subsystem: "de.olezierau.hesper", category: .pointsOfInterest)
    private var link: CADisplayLink?
    private var last: CFTimeInterval = 0
    private(set) var intervals: [Double] = []
    private var lastContents: [ObjectIdentifier: ObjectIdentifier] = [:]
    private var lastSeed: [ObjectIdentifier: UInt32] = [:]
    private(set) var tileFrames: [ObjectIdentifier: Int] = [:]
    private(set) var ticks = 0
    var views: () -> [NSView] = { [] }
    private(set) var started: CFTimeInterval = 0
    private(set) var maxFPS: Int = 60

    func start(on view: NSView) {
        maxFPS = view.window?.screen?.maximumFramesPerSecond ?? 60
        let l = view.displayLink(target: self, selector: #selector(tick(_:)))
        l.preferredFrameRateRange = CAFrameRateRange(minimum: Float(maxFPS), maximum: Float(maxFPS), preferred: Float(maxFPS))
        l.add(to: .main, forMode: .common)
        link = l
        reset()
    }

    func reset() {
        intervals.removeAll(keepingCapacity: true)
        tileFrames.removeAll()
        ticks = 0
        last = 0
        started = CACurrentMediaTime()
    }

    func stop() {
        link?.invalidate()
        link = nil
    }

    @objc private func tick(_ l: CADisplayLink) {
        os_signpost(.event, log: Self.log, name: "vsync")
        let t = l.timestamp
        if last > 0 { intervals.append(t - last) }
        last = t
        ticks += 1
        for v in views() {
            guard let layer = v.layer else { continue }
            let key = ObjectIdentifier(v)
            var changed = false
            if let c = layer.contents as AnyObject? {
                let cid = ObjectIdentifier(c)
                if lastContents[key] != cid { changed = true; lastContents[key] = cid }
                if CFGetTypeID(c) == IOSurfaceGetTypeID() {
                    let seed = IOSurfaceGetSeed(c as! IOSurfaceRef)
                    if lastSeed[key] != seed { changed = true; lastSeed[key] = seed }
                }
            }
            if changed { tileFrames[key, default: 0] += 1 }
        }
    }

    struct Report: Codable {
        var seconds: Double
        var displayMaxFPS: Int
        var vsyncTicks: Int
        var mainFPS: Double
        var hitches: Int           // intervals > 1.5 × nominal
        var droppedFrames: Int     // sum of missed vsyncs
        var worstIntervalMs: Double
        var p99IntervalMs: Double
        var tiles: Int
        var tileFPSMin: Double
        var tileFPSMedian: Double
        var tileFPSMax: Double
    }

    func report(tiles: Int) -> Report {
        let secs = CACurrentMediaTime() - started
        let nominal = 1.0 / Double(maxFPS)
        let sorted = intervals.sorted()
        let hitches = intervals.filter { $0 > nominal * 1.5 }.count
        let dropped = intervals.reduce(0) { $0 + max(0, Int(($1 / nominal).rounded()) - 1) }
        let fps = tileFrames.values.map { Double($0) / secs }.sorted()
        let pad = Array(repeating: 0.0, count: max(0, tiles - fps.count))
        let all = (pad + fps).sorted()
        return Report(
            seconds: secs, displayMaxFPS: maxFPS, vsyncTicks: ticks,
            mainFPS: Double(ticks) / secs, hitches: hitches, droppedFrames: dropped,
            worstIntervalMs: (sorted.last ?? 0) * 1000,
            p99IntervalMs: sorted.isEmpty ? 0 : sorted[min(sorted.count - 1, Int(Double(sorted.count) * 0.99))] * 1000,
            tiles: tiles, tileFPSMin: all.first ?? 0, tileFPSMedian: all.isEmpty ? 0 : all[all.count / 2], tileFPSMax: all.last ?? 0)
    }
}
