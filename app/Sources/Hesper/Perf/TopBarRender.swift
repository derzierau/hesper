import AppKit
import HesperCore

/// Dev tool: `Hesper --render-topbar <dir>` draws the top bar offscreen
/// (no window, no daemon) into PNGs, Dusk and Daylight, wide and narrow,
/// with nothing and with two agents needing you, then exits. Fake traffic
/// lights mark where the window's would be.
@MainActor
enum TopBarRender {
    static let lightsWidth: CGFloat = 78
    static let height: CGFloat = 40

    static func run(_ dir: String) {
        try? FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        let machines = [Machine(short: "laptop", name: "ABC123456", online: true, route: "local"),
                        Machine(short: "mini", name: "XYZ987654 Mac mini", online: true, rttMs: 41, route: "relay")]
        for (scheme, appearance) in [("dusk", NSAppearance.Name.darkAqua), ("daylight", .aqua)] {
            for width in [CGFloat(1200), 700] {
                for needs in [0, 2] {
                    var c = StateCounts()
                    c.total = 7; c.working = 3; c.approval = needs; c.done = 4 - needs
                    let data = TopBarData(counts: c, machines: machines)
                    let name = "topbar-\(scheme)-\(Int(width))-needs\(needs).png"
                    render(data, width: width, appearance: appearance, to: (dir as NSString).appendingPathComponent(name))
                }
            }
        }
        // A scoped wall and an offline machine.
        var c = StateCounts()
        c.total = 10; c.working = 2
        var off = machines
        off[1].online = false
        let scoped = TopBarData(counts: c, machines: off, scope: .overflow, scopeName: "Overflow", scopeCount: 4)
        render(scoped, width: 1200, appearance: .darkAqua, to: (dir as NSString).appendingPathComponent("topbar-dusk-1200-overflow.png"))
    }

    static func render(_ data: TopBarData, width: CGFloat, appearance: NSAppearance.Name, to path: String) {
        let bar = TopBar(source: .fixed(data))
        bar.appearance = NSAppearance(named: appearance)
        bar.fallbackLead = lightsWidth + DS.Spacing.l
        bar.alwaysShowsGlass = true
        bar.glass.forceSolid = true
        bar.frame = NSRect(x: 0, y: 0, width: width, height: height)
        // Two passes: the hosting views size themselves, then the bar.
        for _ in 0..<3 {
            bar.needsLayout = true
            bar.layoutSubtreeIfNeeded()
            RunLoop.current.run(until: Date().addingTimeInterval(0.05))
        }
        guard let rep = bar.bitmapImageRepForCachingDisplay(in: bar.bounds) else { return }
        bar.cacheDisplay(in: bar.bounds, to: rep)
        // The traffic lights, where the window's would be.
        NSGraphicsContext.saveGraphicsState()
        NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)
        let colors: [NSColor] = [.systemRed, .systemYellow, .systemGreen]
        let side: CGFloat = 12
        for (i, col) in colors.enumerated() {
            col.setFill()
            NSBezierPath(ovalIn: NSRect(x: 20 + CGFloat(i) * 20, y: (height - side) / 2, width: side, height: side)).fill()
        }
        NSGraphicsContext.restoreGraphicsState()
        if let png = rep.representation(using: .png, properties: [:]) {
            try? png.write(to: URL(fileURLWithPath: path))
            print("wrote \(path)")
        }
    }
}
