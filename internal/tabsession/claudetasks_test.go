// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

package tabsession

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanClaudeTasks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sid := "sess-123"
	dir := filepath.Join(home, ".claude", "tasks", sid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Written out of numeric order + a non-json file to be ignored.
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("2.json", `{"id":"2","subject":"Second","activeForm":"Doing second","status":"in_progress"}`)
	write("10.json", `{"id":"10","subject":"Tenth","status":"pending"}`)
	write("1.json", `{"id":"1","subject":"First","status":"completed"}`)
	write(".lock", ``)
	write("notes.txt", `ignore me`)

	tasks, err := ScanClaudeTasks(sid)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("want 3 tasks, got %d: %+v", len(tasks), tasks)
	}
	// numeric order: 1, 2, 10
	if tasks[0].ID != "1" || tasks[1].ID != "2" || tasks[2].ID != "10" {
		t.Fatalf("wrong order: %s,%s,%s", tasks[0].ID, tasks[1].ID, tasks[2].ID)
	}
	if tasks[0].Status != "completed" || tasks[1].ActiveForm != "Doing second" {
		t.Fatalf("field mismatch: %+v", tasks)
	}

	// Missing session dir → no tasks, no error.
	empty, err := ScanClaudeTasks("does-not-exist")
	if err != nil || len(empty) != 0 {
		t.Fatalf("missing dir should be empty, got %v err=%v", empty, err)
	}
}
