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

package store

import (
	"encoding/base64"
	"strings"

	"github.com/Gerry3010/projecthub/internal/core/domain"
	"github.com/Gerry3010/projecthub/internal/core/pbclient"
)

// emptyNonceB64 is the 12-byte zero placeholder Passbubble stores in data_nonce.
// The real nonce is embedded as the prefix of encrypted_data (see crypto.Seal),
// matching the convention in Passbubble's cli/internal/vault.
var emptyNonceB64 = base64.StdEncoding.EncodeToString(make([]byte, 12))

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func unb64(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

// findFolder returns the id of the first top-level folder named name, or "".
func findFolder(folders []pbclient.FolderResponse, name string) string {
	for _, f := range folders {
		if f.Name == name {
			return f.ID
		}
	}
	return ""
}

// uniqueSlug derives a filesystem-safe slug from title, appending -2, -3, … if it
// collides with an existing project's slug.
func uniqueSlug(title string, existing []domain.ProjectRef) string {
	base := slugify(title)
	taken := make(map[string]bool, len(existing))
	for _, p := range existing {
		taken[p.Slug] = true
	}
	if !taken[base] {
		return base
	}
	for i := 2; ; i++ {
		cand := base + "-" + itoa(i)
		if !taken[cand] {
			return cand
		}
	}
}

// slugify lowercases title and replaces runs of non-alphanumeric characters with
// a single hyphen, trimming hyphens from the ends. Falls back to "project".
func slugify(title string) string {
	var b strings.Builder
	prevHyphen := false
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevHyphen = false
		default:
			if !prevHyphen {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "project"
	}
	return s
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}
