// Command phgather is a throwaway dev tool: for every ProjectHub project that has a
// LocalPath, it scans the real working directory for Markdown checklists, a few
// well-known docs, and links, and files them into that project's Notes/Todos — so a
// freshly seeded project (see cmd/phseed) isn't empty. Idempotent: it only ever adds
// what isn't already there (by todo text / note title), so re-running is harmless.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Gerry3010/projecthub/internal/core/crypto"
	"github.com/Gerry3010/projecthub/internal/core/domain"
	"github.com/Gerry3010/projecthub/internal/core/pbclient"
	"github.com/Gerry3010/projecthub/internal/core/store"
)

// Caps keep a single noisy project from flooding its Todos/Notes.
const (
	maxTodosPerProject = 40
	maxDocsFiles       = 20
	maxNoteBodyBytes   = 8 << 10 // 8 KiB — trimmed with a note so the reader knows it's partial
	maxLinksPerProject = 200
)

// noteTitles are the well-known files that become their own note, keyed by the note
// title they're filed under. Checked case-sensitively against the project root; the
// first match per title wins.
var noteTitles = []struct {
	title string
	names []string
}{
	{"README", []string{"README.md", "README"}},
	{"CLAUDE.md", []string{"CLAUDE.md"}},
	{"Notizen", []string{"NOTES.md"}},
}

// todoSourceNames are extra root files scanned for `- [ ]` checklist items but not
// filed as their own note (their content would just duplicate README/CLAUDE.md/NOTES.md
// above, or is too narrow — TODO.md — to deserve a whole note).
var todoSourceNames = []string{"TODO.md", "TODOS.md"}

var todoLineRe = regexp.MustCompile(`^\s*[-*]\s+\[([ xX])\]\s+(.+?)\s*$`)
var linkRe = regexp.MustCompile(`https?://[^\s)"'<>\]]+`)
var moduleLineRe = regexp.MustCompile(`(?m)^module\s+(\S+)`)

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

	projects, err := st.ListProjects(ctx)
	if err != nil {
		log.Fatalf("list projects: %v", err)
	}

	var totalTodos, totalNotes, skipped int
	for _, ref := range projects {
		if ref.LocalPath == "" {
			continue
		}
		if fi, err := os.Stat(ref.LocalPath); err != nil || !fi.IsDir() {
			log.Printf("  skip %-28s (dir missing): %s", ref.Title, ref.LocalPath)
			skipped++
			continue
		}
		nTodos, nNotes, err := gatherProject(ctx, st, ref)
		if err != nil {
			log.Printf("  FAIL %-28s: %v", ref.Title, err)
			continue
		}
		log.Printf("  ✓ %-28s +%d todos +%d notes", ref.Title, nTodos, nNotes)
		totalTodos += nTodos
		totalNotes += nNotes
	}
	log.Printf("done: %d todos, %d notes added across %d projects (%d skipped)",
		totalTodos, totalNotes, len(projects)-skipped, skipped)
}

// gatherProject scans one project's LocalPath and files what's missing. Returns how
// many todos/notes it actually created (idempotency means a re-run mostly returns 0/0).
func gatherProject(ctx context.Context, st *store.Store, ref domain.ProjectRef) (nTodos, nNotes int, err error) {
	existingNotes, err := st.ListNotes(ctx, ref.FolderID)
	if err != nil {
		return 0, 0, fmt.Errorf("list notes: %w", err)
	}
	haveNote := map[string]bool{}
	for _, n := range existingNotes {
		haveNote[n.Doc.Title] = true
	}
	existingTodos, err := st.ListTodos(ctx, ref.FolderID)
	if err != nil {
		return 0, 0, fmt.Errorf("list todos: %w", err)
	}
	haveTodo := map[string]bool{}
	for _, t := range existingTodos {
		haveTodo[strings.TrimSpace(t.Val.Text)] = true
	}

	dir := ref.LocalPath
	var allSourceBodies []string // every scanned file's text, for todo/link extraction

	// Named notes: README, CLAUDE.md, Notizen — only created if the title is new.
	for _, nt := range noteTitles {
		path, body := firstExisting(dir, nt.names)
		if path == "" {
			continue
		}
		allSourceBodies = append(allSourceBodies, body)
		if haveNote[nt.title] {
			continue
		}
		if _, err := st.CreateNote(ctx, ref.FolderID, domain.NoteDoc{
			Title: nt.title, Body: truncate(body, maxNoteBodyBytes), UpdatedAt: now(),
		}); err != nil {
			return nTodos, nNotes, fmt.Errorf("create note %q: %w", nt.title, err)
		}
		haveNote[nt.title] = true
		nNotes++
	}

	// Extra todo-only sources (TODO.md, TODOS.md) plus a capped docs/**/*.md sweep.
	for _, name := range todoSourceNames {
		if _, body := firstExisting(dir, []string{name}); body != "" {
			allSourceBodies = append(allSourceBodies, body)
		}
	}
	allSourceBodies = append(allSourceBodies, scanDocs(dir)...)

	// Todos: every unchecked/checked Markdown checklist line, deduped within the
	// project and against what's already there, capped so one huge doc doesn't flood
	// the list.
	seenTodo := map[string]bool{}
	for _, body := range allSourceBodies {
		if nTodos+len(existingTodos) >= maxTodosPerProject {
			break
		}
		for line := range strings.SplitSeq(body, "\n") {
			m := todoLineRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			text := strings.TrimSpace(m[2])
			if text == "" || seenTodo[text] || haveTodo[text] {
				continue
			}
			seenTodo[text] = true
			done := m[1] == "x" || m[1] == "X"
			item := domain.TodoItem{Text: text, Done: done, CreatedAt: now()}
			if done {
				item.DoneAt = now()
			}
			if _, err := st.CreateTodo(ctx, ref.FolderID, item); err != nil {
				return nTodos, nNotes, fmt.Errorf("create todo: %w", err)
			}
			nTodos++
			if nTodos+len(existingTodos) >= maxTodosPerProject {
				break
			}
		}
	}

	// Links: every URL found in the scanned sources, plus the git remote and module/
	// package repo links, deduped and sorted into one "Links" note (skipped if it
	// already exists — v1 doesn't merge).
	if !haveNote["Links"] {
		links := map[string]bool{}
		for _, body := range allSourceBodies {
			for _, u := range linkRe.FindAllString(body, -1) {
				links[strings.TrimRight(u, ".,;:)")] = true
			}
		}
		for _, u := range extraLinks(dir) {
			links[u] = true
		}
		if len(links) > 0 {
			sorted := make([]string, 0, len(links))
			for u := range links {
				sorted = append(sorted, u)
			}
			sort.Strings(sorted)
			if len(sorted) > maxLinksPerProject {
				sorted = sorted[:maxLinksPerProject]
			}
			var b strings.Builder
			for _, u := range sorted {
				b.WriteString("- ")
				b.WriteString(u)
				b.WriteString("\n")
			}
			if _, err := st.CreateNote(ctx, ref.FolderID, domain.NoteDoc{
				Title: "Links", Body: b.String(), UpdatedAt: now(),
			}); err != nil {
				return nTodos, nNotes, fmt.Errorf("create links note: %w", err)
			}
			nNotes++
		}
	}

	return nTodos, nNotes, nil
}

// firstExisting returns the path and text of the first name (relative to dir) that
// exists as a regular file, or ("", "") if none do.
func firstExisting(dir string, names []string) (string, string) {
	for _, name := range names {
		path := filepath.Join(dir, name)
		if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err == nil {
				return path, string(data)
			}
		}
	}
	return "", ""
}

// scanDocs walks docs/ for Markdown files (todo/link extraction only — these don't
// become their own notes, just feed the same regexes as the named sources), capped at
// maxDocsFiles so a huge docs tree doesn't blow up the scan.
func scanDocs(dir string) []string {
	root := filepath.Join(dir, "docs")
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return nil
	}
	var out []string
	count := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || count >= maxDocsFiles {
			return filepath.SkipAll
		}
		if d.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".md") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		out = append(out, string(data))
		count++
		return nil
	})
	return out
}

// extraLinks collects a project's canonical links that don't live in prose: its git
// remote, and (best effort) its module/package repo. Every step is best-effort — a
// missing git repo, go.mod, or package.json just yields nothing from that step.
func extraLinks(dir string) []string {
	var out []string
	if u := gitRemoteURL(dir); u != "" {
		out = append(out, u)
	}
	out = append(out, moduleLinks(dir)...)
	out = append(out, packageJSONLinks(dir)...)
	return out
}

// gitRemoteURL shells out to `git remote get-url origin`; not every project dir is a
// git repo (or has an "origin"), so a non-zero exit just means no link.
func gitRemoteURL(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	u := strings.TrimSpace(string(out))
	// Normalize a git@host:path SSH remote into an https:// browsable link.
	if rest, ok := strings.CutPrefix(u, "git@"); ok {
		if host, path, ok := strings.Cut(rest, ":"); ok {
			u = "https://" + host + "/" + strings.TrimSuffix(path, ".git")
		}
	}
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return strings.TrimSuffix(u, ".git")
	}
	return ""
}

// knownVCSHosts are the module-path prefixes phgather is willing to turn into a link;
// an arbitrary private module path isn't necessarily browsable, so unknown hosts are
// skipped rather than guessed at.
var knownVCSHosts = []string{"github.com/", "gitlab.com/", "bitbucket.org/", "codeberg.org/"}

// moduleLinks reads go.mod's module path and links it if it's on a known host.
func moduleLinks(dir string) []string {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return nil
	}
	m := moduleLineRe.FindStringSubmatch(string(data))
	if m == nil {
		return nil
	}
	mod := m[1]
	for _, host := range knownVCSHosts {
		if strings.HasPrefix(mod, host) {
			return []string{"https://" + mod}
		}
	}
	return nil
}

// packageJSONLinks reads package.json's "homepage" and "repository" fields (the latter
// may be a bare string or a {type,url} object per npm convention).
func packageJSONLinks(dir string) []string {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil
	}
	var pkg struct {
		Homepage   string          `json:"homepage"`
		Repository json.RawMessage `json:"repository"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil
	}
	var out []string
	if strings.HasPrefix(pkg.Homepage, "http") {
		out = append(out, pkg.Homepage)
	}
	if len(pkg.Repository) > 0 {
		var s string
		if json.Unmarshal(pkg.Repository, &s) == nil && strings.HasPrefix(s, "http") {
			out = append(out, s)
		} else {
			var obj struct {
				URL string `json:"url"`
			}
			if json.Unmarshal(pkg.Repository, &obj) == nil {
				u := strings.TrimPrefix(strings.TrimPrefix(obj.URL, "git+"), "git://")
				if strings.HasPrefix(u, "http") {
					out = append(out, strings.TrimSuffix(u, ".git"))
				}
			}
		}
	}
	return out
}

// truncate caps a note body so a huge README doesn't inflate the entry; appends a
// marker so the reader knows it's partial.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n\n…(gekürzt)"
}

// now is the store timestamp; a var (not a call to time.Now inline) only so it reads
// consistently next to CreatedAt/DoneAt/UpdatedAt call sites above.
func now() time.Time { return time.Now() }

// ─── login (same pattern as cmd/phseed) ───────────────────────────────────────────

func login(ctx context.Context, server, email, password string) (*store.Store, error) {
	api := pbclient.New(server)
	resp, err := api.Login(ctx, pbclient.LoginRequest{Email: email, Password: password})
	if err != nil {
		return nil, err
	}
	if resp.RequiresTOTP() {
		return nil, fmt.Errorf("account requires 2FA (status %q) — not supported by phgather", resp.Status)
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
