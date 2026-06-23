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

// Package tabsession reads the locally-installed browser's saved session so the
// ProjectHub TUI companion can capture the currently-open tabs as an encrypted
// tab set. This is inherently a local-machine operation (a hosted web app cannot
// reach these files), which is why it lives in the companion, not the frontend.
package tabsession

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pierrec/lz4/v4"

	"github.com/Gerry3010/projecthub/internal/core/domain"
)

// mozLz4Magic prefixes Firefox's compressed session files (sessionstore.jsonlz4,
// recovery.jsonlz4): the 8-byte magic, a uint32 LE decompressed size, then a raw
// LZ4 block.
var mozLz4Magic = []byte("mozLz40\x00")

// DecompressMozLz4 decompresses a Firefox mozLz4 blob. If data lacks the magic it
// is returned unchanged (callers may pass already-plain JSON, e.g. in tests).
func DecompressMozLz4(data []byte) ([]byte, error) {
	if !bytes.HasPrefix(data, mozLz4Magic) {
		return data, nil
	}
	if len(data) < len(mozLz4Magic)+4 {
		return nil, fmt.Errorf("tabsession: truncated mozLz4 header")
	}
	size := binary.LittleEndian.Uint32(data[len(mozLz4Magic) : len(mozLz4Magic)+4])
	out := make([]byte, size)
	n, err := lz4.UncompressBlock(data[len(mozLz4Magic)+4:], out)
	if err != nil {
		return nil, fmt.Errorf("tabsession: lz4 decompress: %w", err)
	}
	return out[:n], nil
}

// Firefox session JSON (only the fields we need).
type ffSession struct {
	Windows []ffWindow `json:"windows"`
}
type ffWindow struct {
	Tabs []ffTab `json:"tabs"`
}
type ffTab struct {
	Entries []ffEntry `json:"entries"`
	Index   int       `json:"index"` // 1-based index of the active entry
	Pinned  bool      `json:"pinned"`
}
type ffEntry struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

// ParseFirefoxSession turns a (compressed or plain) Firefox session blob into the
// list of currently-open tabs, in window order. about:* and empty URLs are
// skipped.
func ParseFirefoxSession(raw []byte) ([]domain.Tab, error) {
	js, err := DecompressMozLz4(raw)
	if err != nil {
		return nil, err
	}
	var s ffSession
	if err := json.Unmarshal(js, &s); err != nil {
		return nil, fmt.Errorf("tabsession: parse session json: %w", err)
	}

	var tabs []domain.Tab
	for wi, w := range s.Windows {
		for _, t := range w.Tabs {
			if len(t.Entries) == 0 {
				continue
			}
			idx := t.Index - 1
			if idx < 0 || idx >= len(t.Entries) {
				idx = len(t.Entries) - 1
			}
			e := t.Entries[idx]
			if e.URL == "" || strings.HasPrefix(e.URL, "about:") {
				continue
			}
			tabs = append(tabs, domain.Tab{URL: e.URL, Title: e.Title, Window: wi, Pinned: t.Pinned})
		}
	}
	return tabs, nil
}

// FindFirefoxSessionFile locates the best session file across all Firefox profiles
// under ~/.mozilla/firefox. It prefers sessionstore.jsonlz4 (written on clean
// exit) and falls back to sessionstore-backups/recovery.jsonlz4 (updated while
// running). Returns the newest match.
func FindFirefoxSessionFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	base := filepath.Join(home, ".mozilla", "firefox")
	profiles, err := os.ReadDir(base)
	if err != nil {
		return "", fmt.Errorf("tabsession: no Firefox profiles at %s: %w", base, err)
	}

	type cand struct {
		path string
		mod  int64
	}
	var cands []cand
	for _, p := range profiles {
		if !p.IsDir() {
			continue
		}
		for _, rel := range []string{
			"sessionstore.jsonlz4",
			filepath.Join("sessionstore-backups", "recovery.jsonlz4"),
		} {
			full := filepath.Join(base, p.Name(), rel)
			if fi, err := os.Stat(full); err == nil {
				cands = append(cands, cand{full, fi.ModTime().UnixNano()})
			}
		}
	}
	if len(cands) == 0 {
		return "", fmt.Errorf("tabsession: no Firefox session file found under %s", base)
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].mod > cands[j].mod })
	return cands[0].path, nil
}

// CaptureFirefox finds and parses the current Firefox session into tabs.
func CaptureFirefox() ([]domain.Tab, error) {
	path, err := FindFirefoxSessionFile()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseFirefoxSession(raw)
}
