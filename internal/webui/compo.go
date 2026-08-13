// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, either version 3 of the
// License, or (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package webui

// ─── keyed-wrapper reconciliation ─────────────────────────────────────────────
//
// go-app reconciles two components of the same type by copying fields from the
// freshly rendered instance onto the mounted one via reflection
// (nodeManager.updateComponent, pkg/app/node.go). It walks the struct fields and
// skips every field that is not settable — i.e. every UNEXPORTED one — then bails
// out early:
//
//	if !modifiedFields { return v, nil }
//
// No OnUpdate, no re-render of the component's subtree. That is the whole story
// behind a class of "the UI just doesn't refresh" bugs here: our keyed wrappers
// (the little components that exist only to give a list row or a layout node a
// stable CompoID) carried their inputs in unexported fields — `t *sessionsTile`,
// `item store.Item[…]`. Nothing settable, so from their first render onwards they
// were frozen: a renamed session kept its old title, a layout node kept its old
// focus ring, a row kept rendering the read-only view after the parent had flipped
// into edit mode.
//
// Every such wrapper therefore follows two rules:
//
//  1. Inputs the wrapper RENDERS from are exported (`Item`, `P`, `Node`, …), so
//     reflection refreshes the mounted instance instead of leaving it on a stale
//     copy. The parent back-pointer stays unexported on purpose — it is the same
//     pointer every time, so copying it would be pure noise.
//  2. Each wrapper carries `Rev int`, seeded from nextRev(). It differs on every
//     parent render, which is what actually gets updateComponent past the early
//     return. It is needed because a wrapper's visible output often depends on
//     parent state it does not own (sessionsTile.renaming, Workspace.focused) —
//     state no field of the wrapper would ever reflect.
//
// Re-rendering a wrapper is cheap and safe: it re-runs Render, and go-app diffs the
// resulting HTML. Child components keep their own state, because their state lives
// in unexported fields — the very rule that bites the wrappers protects the tiles.
var compoRev int

// nextRev returns a fresh reconciliation token for a keyed wrapper. Single counter,
// no locking: go-app's render loop is single-threaded (wasm, one goroutine).
func nextRev() int {
	compoRev++
	return compoRev
}
