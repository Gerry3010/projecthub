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

package tabsession

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/Gerry3010/projecthub/internal/core/domain"
)

// Claude Code persists a session's task list as one JSON file per task under
// <ClaudeTasksDir>/<sessionId>/<taskId>.json. Reading these lets the Claude tile
// show a session's live plan as a proper checklist instead of raw tool-call JSON.

// ClaudeTasksDir returns ~/.claude/tasks, where Claude Code stores per-session
// task lists.
func ClaudeTasksDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "tasks"), nil
}

// taskFile is the on-disk shape (camelCase, as Claude Code writes it).
type taskFile struct {
	ID          string `json:"id"`
	Subject     string `json:"subject"`
	Description string `json:"description"`
	ActiveForm  string `json:"activeForm"`
	Status      string `json:"status"`
}

// ScanClaudeTasks returns the task list a Claude Code session recorded, ordered by
// numeric task id (falling back to string order). A missing session dir is not an
// error — it just yields no tasks (the session used no task tool).
func ScanClaudeTasks(sessionID string) ([]domain.ClaudeTask, error) {
	if sessionID == "" {
		return nil, nil
	}
	base, err := ClaudeTasksDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(base, sessionID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []domain.ClaudeTask
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue // skip .lock and any non-task files
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue // best-effort: skip unreadable files
		}
		var tf taskFile
		if err := json.Unmarshal(raw, &tf); err != nil || tf.ID == "" {
			continue
		}
		out = append(out, domain.ClaudeTask{
			ID:          tf.ID,
			Subject:     tf.Subject,
			Description: tf.Description,
			ActiveForm:  tf.ActiveForm,
			Status:      tf.Status,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		ai, aerr := strconv.Atoi(out[i].ID)
		bi, berr := strconv.Atoi(out[j].ID)
		if aerr == nil && berr == nil {
			return ai < bi
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}
