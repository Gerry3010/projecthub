// Command phseed is a throwaway dev seeder: it logs into the local Passbubble backend
// and creates a ProjectHub project (+ a 4-tile workspace: tabs, sessions, todo, notes)
// for each curated Claude-Code working directory. Idempotent by LocalPath.
package main

import (
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/Gerry3010/projecthub/internal/core/crypto"
	"github.com/Gerry3010/projecthub/internal/core/domain"
	"github.com/Gerry3010/projecthub/internal/core/pbclient"
	"github.com/Gerry3010/projecthub/internal/core/store"
)

// Curated real project dirs (navigation dirs like ~/, ~/Downloads are excluded).
var dirs = []string{
	"/home/user/Projects/GO-Projekte/projecthub",
	"/home/user/Projects/GO-Projekte/Password-Manager",
	"/home/user/Projects/GO-Projekte/pipepush",
	"/home/user/Projects/GO-Projekte/syno-abb-viewer",
	"/home/user/Projects/Minecraft/VanillaPlusAdditions",
	"/home/user/Projects/Minecraft/mc-model-studio",
	"/home/user/Projects/Minecraft/minecraft-world-ai",
	"/home/user/Projects/Minecraft/neoforge-world-switcher",
	"/home/user/Projects/business/gh-gallery-revamped",
	"/home/user/Projects/business/Homepage",
	"/home/user/Projects/business/steuer-rechner",
	"/home/user/Projects/GTK-Projekte/mission-ws",
	"/home/user/Projects/GTK-Projekte/micdrop",
	"/home/user/Projects/FlutterApps/sleep_app_starter",
	"/home/user/Projects/Chattr2/chattr",
	"/home/user/Projects/clients/Projekte/example-client",
	"/home/user/Projects/Python-Stuff/psono-explorer",
	"/home/user/Projects/Python-Stuff/python-tui-react",
	"/home/user/Projects/AI-STUFF/heyclaude",
	"/home/user/Projects/heyclaude",
}

func main() {
	log.SetFlags(0)
	server := flag.String("server", env("PROJECTHUB_SERVER", "http://localhost:8765"), "Passbubble URL")
	email := flag.String("email", "test@ph.local", "account email")
	password := flag.String("password", "test1234", "account password")
	flag.Parse()

	ctx := context.Background()
	st, err := login(ctx, *server, *email, *password)
	if err != nil {
		log.Fatalf("login: %v", err)
	}
	log.Printf("logged in to %s as %s", *server, *email)

	// Existing projects → skip dirs already seeded (idempotent).
	existing, err := st.ListProjects(ctx)
	if err != nil {
		log.Fatalf("list projects: %v", err)
	}
	have := map[string]bool{}
	for _, r := range existing {
		if r.LocalPath != "" {
			have[r.LocalPath] = true
		}
	}

	created, skipped := 0, 0
	for _, dir := range dirs {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			log.Printf("  skip (missing): %s", dir)
			continue
		}
		if have[dir] {
			log.Printf("  skip (exists): %s", dir)
			skipped++
			continue
		}
		title := titleFor(dir)
		proj, err := st.CreateProject(ctx, title, "", dir)
		if err != nil {
			log.Printf("  FAIL create %q: %v", title, err)
			continue
		}
		// resolve the Passbubble folder id for this project
		refs, err := st.ListProjects(ctx)
		if err != nil {
			log.Printf("  FAIL list after create %q: %v", title, err)
			continue
		}
		var folderID string
		for _, r := range refs {
			if r.ID == proj.ID {
				folderID = r.FolderID
			}
		}
		if folderID == "" {
			log.Printf("  FAIL: no folder id for %q", title)
			continue
		}
		if _, err := st.SetLayout(ctx, folderID, fourTileLayout()); err != nil {
			log.Printf("  FAIL layout %q: %v", title, err)
			continue
		}
		log.Printf("  ✓ %-28s → %s", title, dir)
		created++
	}
	log.Printf("done: %d created, %d skipped", created, skipped)
}

// fourTileLayout is a 2×2 workspace: left column Browser-Tabs (top) + Claude-Sessions
// (bottom), right column Todo (top) + Notes (bottom).
func fourTileLayout() domain.Layout {
	leaf := func(t domain.TileType) *domain.LayoutNode {
		return &domain.LayoutNode{PaneID: uuid.NewString(), Type: t}
	}
	return domain.Layout{Version: 1, Root: &domain.LayoutNode{
		Dir: "row", Ratio: 0.5, PaneID: uuid.NewString(),
		A: &domain.LayoutNode{Dir: "col", Ratio: 0.5, PaneID: uuid.NewString(),
			A: leaf(domain.TileTabs),
			B: leaf(domain.TileSessions)},
		B: &domain.LayoutNode{Dir: "col", Ratio: 0.5, PaneID: uuid.NewString(),
			A: leaf(domain.TileTodo),
			B: leaf(domain.TileNotes)},
	}}
}

// titleFor derives a readable project title from the dir, disambiguating the two
// heyclaude dirs by their parent.
func titleFor(dir string) string {
	base := filepath.Base(dir)
	if base == "heyclaude" {
		return base + " (" + filepath.Base(filepath.Dir(dir)) + ")"
	}
	return base
}

func login(ctx context.Context, server, email, password string) (*store.Store, error) {
	api := pbclient.New(server)
	resp, err := api.Login(ctx, pbclient.LoginRequest{Email: email, Password: password})
	if err != nil {
		return nil, err
	}
	if resp.RequiresTOTP() {
		return nil, fmt.Errorf("account requires 2FA (status %q) — not supported by seeder", resp.Status)
	}
	salt := b64d(resp.KDFSalt)
	encX := b64d(resp.EncPrivX25519)
	encM := b64d(resp.EncPrivMLKEM768)
	pubX := b64d(resp.PubX25519)
	pubM := b64d(resp.PubMLKEM768)
	keys, err := crypto.Unlock(password, salt, resp.KDFTime, resp.KDFMemory, resp.UserID, encX, encM, pubX, pubM)
	if err != nil {
		return nil, fmt.Errorf("unlock: %w", err)
	}
	api.SetSession(resp)
	return store.New(api, keys), nil
}

func b64d(s string) []byte {
	b, _ := base64.StdEncoding.DecodeString(s)
	return b
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
