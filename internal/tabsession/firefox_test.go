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
	"encoding/binary"
	"testing"

	"github.com/pierrec/lz4/v4"
)

const sampleSession = `{
  "windows": [
    {"tabs": [
      {"index": 2, "pinned": true, "entries": [
        {"url": "https://old.example", "title": "old"},
        {"url": "https://example.com", "title": "Example"}
      ]},
      {"index": 1, "entries": [{"url": "about:newtab", "title": "New Tab"}]}
    ]},
    {"tabs": [
      {"index": 1, "entries": [{"url": "https://go.dev", "title": "Go"}]}
    ]}
  ]
}`

func checkTabs(t *testing.T, raw []byte) {
	t.Helper()
	tabs, err := ParseFirefoxSession(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Expect: example.com (win 0, active entry, pinned), go.dev (win 1).
	// about:newtab is skipped.
	if len(tabs) != 2 {
		t.Fatalf("expected 2 tabs, got %d: %+v", len(tabs), tabs)
	}
	if tabs[0].URL != "https://example.com" || tabs[0].Title != "Example" || !tabs[0].Pinned || tabs[0].Window != 0 {
		t.Fatalf("tab0 mismatch: %+v", tabs[0])
	}
	if tabs[1].URL != "https://go.dev" || tabs[1].Window != 1 {
		t.Fatalf("tab1 mismatch: %+v", tabs[1])
	}
}

func TestParsePlainJSON(t *testing.T) {
	checkTabs(t, []byte(sampleSession))
}

func TestParseMozLz4(t *testing.T) {
	checkTabs(t, mozLz4Compress(t, []byte(sampleSession)))
}

// mozLz4Compress builds a Firefox-format mozLz4 blob from plaintext, exercising
// the full DecompressMozLz4 path.
func mozLz4Compress(t *testing.T, plain []byte) []byte {
	t.Helper()
	buf := make([]byte, lz4.CompressBlockBound(len(plain)))
	var c lz4.Compressor
	n, err := c.CompressBlock(plain, buf)
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	out := append([]byte{}, mozLz4Magic...)
	var size [4]byte
	binary.LittleEndian.PutUint32(size[:], uint32(len(plain)))
	out = append(out, size[:]...)
	out = append(out, buf[:n]...)
	return out
}
