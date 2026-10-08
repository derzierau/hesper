import HesperCore
import SwiftUI

/// ⌘K: the search surface in its "All" scope: agents (with their
/// actions), drafts, actions, projects, history, machines, layouts,
/// settings. ⇥ (or the History pill) switches to the History scope (⌘Y)
/// with the query; ⇥ on an agent shows its actions instead. The query and
/// selection live on the model, so the palette comes back as it was left;
/// ↑↓ ⏎ ⌘⏎ ⇥ esc reach it through the window (OverlayKeys), typing goes to
/// the field.
struct PaletteView: View {
    @Bindable var model: AppModel
    @FocusState private var focused: Bool
    /// The field's own copy (an NSTextField being edited ignores outside
    /// changes); the model keeps the query across closes.
    @State private var text = ""

    var body: some View {
        let list = model.filteredPalette()
        let sel = model.paletteList.selection(enabled: list.map(\.enabled))
        let history = historyRows(list)
        VStack(spacing: 0) {
            header
            Rectangle().fill(Theme.stroke).frame(height: 1)
            ScrollViewReader { proxy in
                ScrollView(showsIndicators: false) {
                    VStack(alignment: .leading, spacing: 0) {
                        ForEach(Array(list.enumerated()), id: \.element.id) { i, item in
                            if i == 0 || list[i - 1].section != item.section {
                                Text((item.section ?? "").uppercased()).font(DS.font(.meta, .medium)).foregroundStyle(Theme.dim)
                                    .padding(.horizontal, DS.Spacing.m).padding(.top, i == 0 ? DS.Spacing.s : DS.Spacing.l).padding(.bottom, DS.Spacing.xs)
                                    .accessibilityAddTraits(.isHeader)
                            }
                            row(item, selected: i == sel, history: history[item.id])
                                .id(i)
                                .onTapGesture { if item.enabled { item.run() } }
                        }
                        if list.isEmpty {
                            Text(model.paletteScope == nil ? "Nothing matches. ⇥ searches History." : "Nothing matches")
                                .font(DS.font(.chrome)).foregroundStyle(Theme.dim).padding(DS.Spacing.xl)
                        }
                    }
                    .padding(DS.Spacing.s)
                }
                .frame(maxHeight: SearchLook.paletteListMaxHeight)
                .onChange(of: sel) { if let sel { proxy.scrollTo(sel) } }
            }
            Hints(items: hints(list, sel), leading: "\(list.count) results")
        }
        .dsSheet() // opaque, over the scrim
        .onAppear {
            text = model.paletteList.query
            DispatchQueue.main.async { focused = true }
        }
        .accessibilityIdentifier("palette")
    }

    // MARK: Header: the scope, the field

    private var header: some View {
        HStack(spacing: DS.Spacing.m) {
            Image(systemName: "magnifyingglass").font(DS.font(.panelTitle)).foregroundStyle(Theme.dim).accessibilityHidden(true)
            if let scope = model.paletteScope, let a = model.agent(scope) {
                Pill(a.name, variant: .segment(selected: true), mark: StateMarkKind(a.state))
                    .accessibilityLabel("Actions for \(a.name)")
            } else {
                scopePills
            }
            TextField("", text: $text, prompt: Text(model.paletteScope == nil ? "Agents, actions, history, projects, machines, settings…" : "Actions…")
                .foregroundStyle(Theme.dim))
                .textFieldStyle(.plain)
                .font(DS.font(.panelTitle))
                .foregroundStyle(Theme.fg)
                .focused($focused)
                .onChange(of: text) { if model.paletteList.query != text { model.paletteList.query = text; model.paletteList.index = 0 } }
                .accessibilityIdentifier("palette.field")
        }
        .padding(.horizontal, DS.Spacing.xl)
        .frame(height: SearchLook.fieldHeight)
    }

    /// "All" (this scope), "History" (a click switches, like ⇥), then ⇥.
    @ViewBuilder private var scopePills: some View {
        HStack(spacing: DS.Spacing.xs) {
            Pill(SearchScope.all.title, variant: .segment(selected: true))
                .accessibilityLabel("Scope: All (selected)")
            if model.historyAvailable {
                Button { model.switchSearchScope(to: .history, text: text) } label: {
                    Pill(SearchScope.history.title, variant: .segment(selected: false))
                }
                .buttonStyle(.plain)
                .help("Search History (⇥, \(SearchScope.history.shortcut))")
                .accessibilityLabel("Scope: History")
                .accessibilityHint("Switches to History with this search")
                .accessibilityIdentifier("palette.scope.history")
                Kbd("⇥").padding(.leading, DS.Spacing.xs).accessibilityHidden(true)
            }
        }
        .fixedSize()
    }

    // MARK: Rows

    @ViewBuilder
    private func row(_ item: OverlayItem, selected: Bool, history: SearchRowText?) -> some View {
        if let history {
            SearchResultRow(row: history, selected: selected)
        } else {
            let kbd = SearchKeys.isShortcut(item.detail) ? item.detail : nil
            let meta = item.checked ? (kbd == nil && !item.detail.isEmpty ? "current · " + item.detail : "current") : (kbd == nil && !item.detail.isEmpty ? item.detail : nil)
            Row(title: item.title, meta: meta, mark: mark(item), kbd: kbd, selected: selected)
                .opacity(item.enabled ? 1 : SearchLook.disabledOpacity)
                .accessibilityAddTraits(selected ? .isSelected : [])
        }
    }

    private func mark(_ item: OverlayItem) -> StateMarkKind? {
        if let m = item.stateMark { return m }
        guard let dot = item.dot else { return nil }
        if dot == Theme.Token.done.hex { return .done }
        if dot == Theme.Token.dim.hex { return .idle }
        return .working
    }

    /// The History section's rows (title 600, snippet with highlights,
    /// "machine · age"), from the sessions ⌘K's search found.
    private func historyRows(_ list: [OverlayItem]) -> [String: SearchRowText] {
        guard list.contains(where: { $0.section == "History" }) else { return [:] }
        let names = MachineLabel.names(model.machines)
        let now = Date()
        var out: [String: SearchRowText] = [:]
        for s in HistoryPaletteSource.shared(for: model).results {
            out["history:" + s.id] = SearchRowText(s, machines: names, now: now, snippetMax: SearchLook.paletteSnippetMax)
        }
        return out
    }

    private func hints(_ list: [OverlayItem], _ sel: Int?) -> [(String, String)] {
        var h = [("↑↓", "select"), ("⏎", "go / do"), ("⌘⏎", "open full size")]
        if model.paletteScope == nil {
            let agent = sel.map { list.indices.contains($0) && list[$0].id.hasPrefix("agent:") } ?? false
            h.append(("⇥", agent ? "actions" : "history"))
        }
        h.append(("esc", model.paletteScope == nil ? "close" : "back"))
        return h
    }
}
