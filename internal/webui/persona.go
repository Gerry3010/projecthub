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

import (
	"strings"

	"github.com/Gerry3010/projecthub/internal/core/domain"
)

// phManagerPersona is the system prompt that turns the embedded sidebar chat into the
// "ProjectHub manager": a cross-project advisor that knows the user's projects and helps
// plan across them. It is appended to Claude's own system prompt (--append-system-prompt),
// so Claude keeps its normal abilities but takes on this framing. Deliberately advisory:
// no file/vault changes unless the user explicitly asks in a project's own Claude tile.
const phManagerPersona = `Du bist der ProjectHub-Assistent — Gerrys projektübergreifender Manager und Sparringspartner im ProjectHub-Cockpit.

Deine Rolle:
- Du behältst den Überblick über alle Projekte (siehe Liste unten) und denkst projektübergreifend: Prioritäten, Abhängigkeiten, „was als Nächstes", Zusammenhänge zwischen Projekten.
- Du berätst, planst und strukturierst. Du bist knapp, konkret und handlungsorientiert; antworte auf Deutsch.
- Du bist ein reiner Chat-/Planungs-Assistent in der Seitenleiste. Ändere NICHTS an Dateien, Code oder dem Vault und führe keine Terminal-Aktionen aus. Für echte Arbeit in einem Projekt verweist du auf die Claude-Code-Tile dieses Projekts.

Wenn dir Kontext fehlt, frag nach, statt zu raten.`

// buildManagerSystemPrompt assembles the full system prompt for the sidebar chat:
// the persona plus a live snapshot of the user's projects (title + local path), so
// Claude "sees" the projects without needing MCP. Called from Root, which already has
// the unlocked project list in memory.
func buildManagerSystemPrompt(projects []domain.ProjectRef) string {
	var b strings.Builder
	b.WriteString(phManagerPersona)
	b.WriteString("\n\n## Gerrys Projekte in ProjectHub\n")
	if len(projects) == 0 {
		b.WriteString("(noch keine Projekte angelegt)\n")
		return b.String()
	}
	for _, p := range projects {
		b.WriteString("- ")
		b.WriteString(p.Title)
		if p.LocalPath != "" {
			b.WriteString(" — ")
			b.WriteString(p.LocalPath)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
