import AppKit
import CryptoKit
import HesperCore

// Drop to attach (docs: "As built — drop to attach"): what a drop on an
// agent's terminal does once the pasteboard is read (UI/TerminalDrop).

extension AppModel {
    /// Delivers dropped payloads to an agent: it becomes the one the
    /// keyboard types into (active tile, or its focus view), payloads
    /// without a usable file are saved, files for another machine are
    /// uploaded there first (files.put), then the paths go in as bracketed
    /// pastes (DropPlan; never submitted). `progress` gets a label while
    /// an upload runs, nil when done.
    func deliverDrop(_ id: String, payloads: [DropPayload], progress: @escaping @MainActor (String?) -> Void) async {
        guard let a = agent(id) else { return }
        guard a.isRunning else { showToast("\(a.name) has exited", error: true); return }
        bringForInput(id)
        let dir = attachmentsDir(for: AttachmentNaming.folder(id))
        var paths: [String] = []
        var texts: [String] = []
        for p in payloads {
            switch p {
            case .file(let path, let isDir):
                if !isDir && DropPlan.needsConversion(path) {
                    if let png = AttachmentStore.convertToPNG(path, in: dir) { paths.append(png) } else { paths.append(path) }
                } else if isDir && a.machine != localMachine {
                    showToast(AttachmentUpload.refusal(name: (path as NSString).lastPathComponent, size: 0, isDirectory: true) ?? "", error: true)
                } else {
                    paths.append(path)
                }
            case .image(let data, let name):
                do { paths.append(try AttachmentStore.saveImage(data, name: name, in: dir)) } catch {
                    showToast("Could not save the dropped image", error: true)
                }
            case .text(let t):
                texts.append(t)
            }
        }
        if a.machine != localMachine, !paths.isEmpty {
            paths = await uploadAll(paths, to: .agent(id), machine: a.machine, progress: progress)
            progress(nil)
            if paths.isEmpty && texts.isEmpty { return }
        }
        for paste in DropPlan.pastes(paths: paths, texts: texts) { sendInput(id, paste, paste: true) }
        lastDrop = (id, DropPlan.pastes(paths: paths, texts: texts))
    }

    /// The agent gets the keyboard: the focus view already showing it
    /// keeps it; on the wall its tile becomes active (a shelf card opens
    /// the focus view).
    func bringForInput(_ id: String) {
        if mode == .focus {
            if focusedID != id { focus(id) }
            return
        }
        if activeTileID != id { activate(id) }
    }

    /// Uploads each file (sizes checked first); the paths on the machine,
    /// in order. Failures show a toast and are left out.
    func uploadAll(_ paths: [String], to target: DaemonClient.FileTarget, machine: String, progress: @escaping @MainActor (String?) -> Void) async -> [String] {
        let fm = FileManager.default
        let sizes = paths.map { (try? fm.attributesOfItem(atPath: $0)[.size] as? NSNumber)?.int64Value ?? 0 }
        let total = sizes.reduce(0, +)
        let where_ = self.machine(machine)?.displayName ?? machine
        var done: Int64 = 0
        var out: [String] = []
        for (p, size) in zip(paths, sizes) {
            let name = (p as NSString).lastPathComponent
            if let why = AttachmentUpload.refusal(name: name, size: size, isDirectory: false) {
                showToast(why, error: true)
                continue
            }
            func label(_ sent: Int64) -> String {
                total >= AttachmentUpload.progressThreshold
                    ? "uploading to \(where_)… \(AttachmentUpload.megabytes(done + sent)) of \(AttachmentUpload.megabytes(total))"
                    : "uploading to \(where_)…"
            }
            progress(label(0))
            do {
                let remote = try await upload(p, size: size, to: target) { sent in progress(label(sent)) }
                out.append(remote)
            } catch {
                showToast("Could not upload \(name) to \(where_): \(describe(error))", error: true)
            }
            done += size
        }
        return out
    }

    /// One file to another machine: files.put, then 512 KiB chunks
    /// (files.chunk); the path there.
    func upload(_ path: String, size: Int64, to target: DaemonClient.FileTarget, sent: @escaping @MainActor (Int64) -> Void) async throws -> String {
        let url = URL(fileURLWithPath: path)
        let sha = try await Task.detached { try Self.sha256(url) }.value
        let (upload, chunk) = try await client.filesPut(target, name: url.lastPathComponent, size: size, sha256: sha)
        let fh = try FileHandle(forReadingFrom: url)
        defer { try? fh.close() }
        for c in AttachmentUpload.chunks(size: size, chunk: chunk) {
            let data = try fh.read(upToCount: c.length) ?? Data()
            let r = try await client.filesChunk(upload: upload, offset: c.offset, data: data, last: c.last)
            sent(c.offset + Int64(data.count))
            if c.last {
                guard let r else { throw RPCError(code: -32000, message: "hesperd returned no path", kind: .remote) }
                return r
            }
        }
        throw RPCError(code: -32000, message: "upload incomplete", kind: .remote)
    }

    nonisolated static func sha256(_ url: URL) throws -> String {
        let fh = try FileHandle(forReadingFrom: url)
        defer { try? fh.close() }
        var h = SHA256()
        while let d = try fh.read(upToCount: 1 << 20), !d.isEmpty { h.update(data: d) }
        return h.finalize().map { String(format: "%02x", $0) }.joined()
    }

    /// A draft started on another machine: the files it names (its
    /// attachments) go there first, and the task names them by their
    /// paths there. Nil and `error` when an upload fails.
    func uploadDraftAttachments(_ draftID: String, machine: String, task: String) async -> String? {
        guard let d = drafts[draftID], !d.attachments.isEmpty else { return task }
        var out = task
        for p in d.attachments {
            let escaped = ShellEscape.quote(p)
            let legacy = "\"\(p)\""
            guard out.contains(escaped) || out.contains(legacy) || out.contains(p) else { continue }
            let size = (try? FileManager.default.attributesOfItem(atPath: p)[.size] as? NSNumber)?.int64Value ?? -1
            guard size >= 0 else { continue }
            let remote = await uploadAll([p], to: .draft(machine: machine, draft: draftID), machine: machine) { _ in }
            guard let r = remote.first else { return nil }
            let q = ShellEscape.quote(r)
            out = out.replacingOccurrences(of: escaped, with: q).replacingOccurrences(of: legacy, with: q)
            if !out.contains(q) { out = out.replacingOccurrences(of: p, with: q) }
        }
        return out
    }
}
