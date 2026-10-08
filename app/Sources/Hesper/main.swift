import AppKit

setvbuf(stdout, nil, _IOLBF, 0)
// Geist / Geist Mono for the chrome, before any view or font is made.
BrandFonts.register()
let app = NSApplication.shared
// Dev tool: the top bar drawn offscreen into PNGs (no window, no daemon).
if let i = CommandLine.arguments.firstIndex(of: "--render-topbar"), i + 1 < CommandLine.arguments.count {
    MainActor.assumeIsolated { TopBarRender.run(CommandLine.arguments[i + 1]) }
    exit(0)
}
// Dev tool: the project sidebar drawn offscreen into PNGs (no window, no daemon).
if let i = CommandLine.arguments.firstIndex(of: "--render-sidebar"), i + 1 < CommandLine.arguments.count {
    MainActor.assumeIsolated { SidebarRender.run(CommandLine.arguments[i + 1]) }
    exit(0)
}
// Dev tool: the closing-agents UI drawn offscreen into PNGs (no window, no daemon).
if let i = CommandLine.arguments.firstIndex(of: "--render-close"), i + 1 < CommandLine.arguments.count {
    MainActor.assumeIsolated { CloseRender.run(CommandLine.arguments[i + 1]) }
    exit(0)
}
// Dev tool: moving work across Macs (progress line, preflight strips) drawn offscreen into PNGs.
if let i = CommandLine.arguments.firstIndex(of: "--render-move"), i + 1 < CommandLine.arguments.count {
    MainActor.assumeIsolated { MoveRender.run(CommandLine.arguments[i + 1]) }
    exit(0)
}
// Dev tool: a draft tile (a project window's, project locked) drawn offscreen into PNGs.
if let i = CommandLine.arguments.firstIndex(of: "--render-draft"), i + 1 < CommandLine.arguments.count {
    MainActor.assumeIsolated { DraftRender.run(CommandLine.arguments[i + 1]) }
    exit(0)
}
app.setActivationPolicy(.regular)
let delegate = AppDelegate()
app.delegate = delegate
app.run()
