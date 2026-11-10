import HesperDesign
import HesperFleet
import SwiftUI

/// Home's top: the scope pill (All · Needs you · Working, with their
/// counts) and the menu button that reaches Agents, Macs, Search and
/// Settings.
struct ScopeBar: View {
    @Binding var scope: HomeScope
    let counts: HomeCounts
    let menu: (MenuRoute) -> Void

    var body: some View {
        HStack(spacing: DS.Spacing.l) {
            HStack(spacing: 0) {
                ForEach(HomeScope.allCases, id: \.self) { segment($0) }
            }
            .frame(minHeight: DS.Size.control)
            .fixedSize(horizontal: false, vertical: true)
            .background(Theme.background)
            .overlay {
                RoundedRectangle(cornerRadius: DS.Radius.console).strokeBorder(Theme.accent, lineWidth: DS.Edge.line)
            }
            .clipShape(.rect(cornerRadius: DS.Radius.console))

            Menu {
                ForEach(MenuRoute.allCases, id: \.self) { route in
                    Button(route.title) { menu(route) }
                }
            } label: {
                Image(systemName: "line.3.horizontal")
                    .font(.title3)
                    .foregroundStyle(Theme.accent)
                    .frame(width: DS.Size.control, height: DS.Size.control)
                    .background(Theme.background)
                    .overlay {
                        RoundedRectangle(cornerRadius: DS.Radius.console).strokeBorder(Theme.accent, lineWidth: DS.Edge.line)
                    }
                    .contentShape(.rect)
            }
            .accessibilityLabel("Menu")
        }
        .padding(.horizontal, DS.Spacing.xl)
        .padding(.top, DS.Spacing.l)
        .padding(.bottom, DS.Spacing.xs)
        .background(Theme.background)
        .dynamicTypeSize(...DS.chromeTypeLimit)
    }

    private func segment(_ s: HomeScope) -> some View {
        let selected = scope == s
        return Button {
            scope = s
        } label: {
            Text("\(s.title) \(count(s))")
                .font(selected ? .footnote.weight(.semibold) : .footnote)
                .foregroundStyle(selected ? Theme.onAccent : Theme.text)
                .multilineTextAlignment(.center)
                .padding(.horizontal, DS.Spacing.xs)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(selected ? Theme.accent : Theme.surface)
                .overlay {
                    if !selected { Rectangle().strokeBorder(Theme.line, lineWidth: DS.Edge.line) }
                }
                .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(selected ? .isSelected : [])
    }

    private func count(_ s: HomeScope) -> Int {
        switch s {
        case .all: counts.all
        case .needsYou: counts.needsYou
        case .working: counts.working
        }
    }
}
