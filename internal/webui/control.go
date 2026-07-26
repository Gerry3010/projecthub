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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/Gerry3010/projecthub/internal/control"
	"github.com/Gerry3010/projecthub/internal/core/domain"
)

// The MCP control loop: while a project workspace is open, the renderer long-polls
// the sidecar for renderer-bound MCP tool calls (vault + workspace), executes them,
// and posts results back. Only the renderer holds the Passbubble keys and owns the
// tiling tree, so these tools can only run here (see internal/control, internal/mcp,
// cmd/phmcp). A tool referencing "the active project" targets THIS workspace.

// startControlLoop begins long-polling for MCP commands (desktop build only).
func (w *Workspace) startControlLoop(ctx app.Context) {
	if w.Native == nil {
		return
	}
	w.ctlStop = make(chan struct{})
	stop := w.ctlStop
	native := w.Native
	ctx.Async(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			pollCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			cmd, ok, err := native.ControlNext(pollCtx)
			cancel()
			if err != nil {
				select {
				case <-stop:
					return
				case <-time.After(2 * time.Second): // back off on transient error
				}
				continue
			}
			if !ok {
				continue // poll timed out with no command; poll again
			}
			res := w.dispatchControl(ctx, cmd)
			postCtx, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
			_ = native.ControlResult(postCtx, res)
			cancel2()
		}
	})
}

// stopControlLoop ends the long-poll loop (called on workspace dismount).
func (w *Workspace) stopControlLoop() {
	if w.ctlStop != nil {
		close(w.ctlStop)
		w.ctlStop = nil
	}
}

// dispatchControl executes one MCP command and wraps the outcome as a Result. Vault
// tools (Store HTTP) run in this goroutine; tile tools touch go-app state and are
// marshalled onto the UI goroutine via execTileControlSync.
func (w *Workspace) dispatchControl(ctx app.Context, cmd control.Command) control.Result {
	res := control.Result{ID: cmd.ID}
	out, err := w.runControlTool(ctx, cmd.Tool, cmd.Args)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	b, mErr := json.Marshal(out)
	if mErr != nil {
		res.Error = mErr.Error()
		return res
	}
	res.Result = b
	return res
}

func (w *Workspace) runControlTool(ctx app.Context, tool string, args json.RawMessage) (any, error) {
	bg := context.Background()
	switch tool {
	case "project_list":
		refs, err := w.Store.ListProjects(bg)
		if err != nil {
			return nil, err
		}
		out := make([]map[string]string, 0, len(refs))
		for _, p := range refs {
			out = append(out, map[string]string{"id": p.ID, "title": p.Title, "local_path": p.LocalPath})
		}
		return out, nil
	case "todo_list":
		todos, err := w.Store.ListTodos(bg, w.Ref.FolderID)
		if err != nil {
			return nil, err
		}
		out := make([]map[string]any, 0, len(todos))
		for _, t := range todos {
			out = append(out, map[string]any{"text": t.Val.Text, "done": t.Val.Done, "order": t.Val.Order})
		}
		return out, nil
	case "todo_create":
		var a struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(args, &a)
		if strings.TrimSpace(a.Text) == "" {
			return nil, errors.New("text required")
		}
		if _, err := w.Store.CreateTodo(bg, w.Ref.FolderID, domain.TodoItem{Text: a.Text, CreatedAt: time.Now()}); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	case "tile_list", "tile_create", "tile_close", "tile_focus":
		return w.execTileControlSync(ctx, tool, args)
	}
	return nil, fmt.Errorf("unknown tool %q", tool)
}

// execTileControlSync runs a tile tool on the UI goroutine (it mutates the layout /
// calls phShell) and waits for the result.
func (w *Workspace) execTileControlSync(ctx app.Context, tool string, args json.RawMessage) (any, error) {
	type outcome struct {
		out any
		err error
	}
	ch := make(chan outcome, 1)
	ctx.Dispatch(func(app.Context) {
		out, err := w.execTileControl(tool, args)
		ch <- outcome{out, err}
	})
	select {
	case o := <-ch:
		return o.out, o.err
	case <-time.After(10 * time.Second):
		return nil, errors.New("tile operation timed out")
	}
}

// execTileControl runs on the UI goroutine.
func (w *Workspace) execTileControl(tool string, args json.RawMessage) (any, error) {
	switch tool {
	case "tile_list":
		out := []map[string]string{}
		for _, leaf := range leaves(w.layout.Root) {
			out = append(out, map[string]string{"pane_id": leaf.PaneID, "type": string(leaf.Type)})
		}
		return out, nil
	case "tile_create":
		var a struct {
			Type   string            `json:"type"`
			Params map[string]string `json:"params"`
		}
		_ = json.Unmarshal(args, &a)
		if a.Type == "" {
			return nil, errors.New("type required")
		}
		paneID := w.addTile(domain.TileType(a.Type), a.Params)
		return map[string]string{"pane_id": paneID}, nil
	case "tile_close":
		var a struct {
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(args, &a)
		if findLeaf(w.layout.Root, a.PaneID) == nil {
			return nil, errors.New("no such tile: " + a.PaneID)
		}
		w.closeTile(a.PaneID)
		return map[string]bool{"ok": true}, nil
	case "tile_focus":
		var a struct {
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(args, &a)
		if findLeaf(w.layout.Root, a.PaneID) == nil {
			return nil, errors.New("no such tile: " + a.PaneID)
		}
		w.focused = a.PaneID
		return map[string]bool{"ok": true}, nil
	}
	return nil, fmt.Errorf("unknown tile tool %q", tool)
}
