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
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/Gerry3010/projecthub/internal/core/domain"
	"github.com/Gerry3010/projecthub/internal/core/store"
	"github.com/Gerry3010/projecthub/internal/native/nativeclient"
)

// applyBackground writes the wallpaper/glass settings as CSS custom properties on
// :root, so the workspace wallpaper (.ph-ws-wallpaper) and the translucent, blurred
// tiles (.ph-tile) pick them up live. imageURL is a pre-resolved data URL (empty for
// non-image backgrounds). A nil bg clears everything back to the flat --bg.
func applyBackground(bg *domain.Background, imageURL string) {
	doc := app.Window().Get("document")
	if !doc.Truthy() {
		return
	}
	style := doc.Get("documentElement").Get("style")
	clear := func(keys ...string) {
		for _, k := range keys {
			style.Call("removeProperty", k)
		}
	}
	set := func(k, v string) { style.Call("setProperty", k, v) }

	if bg == nil {
		clear("--bg-color", "--bg-image", "--panel-alpha", "--panel-blur", "--bg-dim")
		return
	}
	if bg.Type == "color" && bg.Color != "" {
		set("--bg-color", bg.Color)
	} else {
		clear("--bg-color")
	}
	if bg.Type == "image" && imageURL != "" {
		set("--bg-image", `url("`+imageURL+`")`)
	} else {
		set("--bg-image", "none")
	}
	alpha := bg.Alpha
	if alpha == 0 {
		alpha = 1
	}
	set("--panel-alpha", fmt.Sprintf("%.3f", alpha))
	set("--panel-blur", fmt.Sprintf("%dpx", bg.Blur))
	set("--bg-dim", fmt.Sprintf("%.3f", bg.Dim))
}

// resolveBgImageURL turns a background's image reference into a data URL: a local
// path ("file:<abs>") is fetched through the sidecar; anything else is treated as a
// ph-file entry id and decrypted from Passbubble. Returns "" for non-image or on
// error (the caller then shows no wallpaper image).
func resolveBgImageURL(st *store.Store, nc *nativeclient.Client, bg *domain.Background) string {
	if bg == nil || bg.Type != "image" || bg.Image == "" {
		return ""
	}
	if path, ok := strings.CutPrefix(bg.Image, "file:"); ok {
		if !nc.Available() {
			return ""
		}
		data, ct, err := nc.FetchFile(context.Background(), path)
		if err != nil {
			return ""
		}
		return bgDataURL(orMime(ct), data)
	}
	fb, err := st.GetFile(context.Background(), bg.Image)
	if err != nil {
		return ""
	}
	return dataURL(fb) // reuse project.go's FileBlob → data URL
}

func bgDataURL(mime string, data []byte) string {
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func orMime(ct string) string {
	if ct == "" {
		return "image/*"
	}
	// Strip any "; charset=…" the server may append.
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		return ct[:i]
	}
	return ct
}
