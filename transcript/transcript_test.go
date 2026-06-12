package transcript

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"simple path", "/home/u/project", "-home-u-project"},
		{"dotfile dir", "/home/u/.claude", "-home-u--claude"},
		{"dashes survive as dashes", "/sessions/abc-def", "-sessions-abc-def"},
		{"underscores and dots", "/home/u/my_proj.v2", "-home-u-my-proj-v2"},
		{"spaces", "/home/u/My Project", "-home-u-My-Project"},
		{"empty", "", ""},
		{"exactly 200 chars kept", "/" + strings.Repeat("a", 199), "-" + strings.Repeat("a", 199)},
		{"over 200 chars dropped", "/" + strings.Repeat("a", 200), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Slugify(tt.in); got != tt.want {
				t.Errorf("Slugify(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestExtractResumeID(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"separate arg form", []string{"-p", "--resume", "abc-123", "--verbose"}, "abc-123"},
		{"equals form", []string{"--resume=abc-123"}, "abc-123"},
		{"absent", []string{"-p", "--verbose"}, ""},
		{"flag without value", []string{"-p", "--resume"}, ""},
		{"empty args", nil, ""},
		{"first occurrence wins", []string{"--resume", "one", "--resume", "two"}, "one"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractResumeID(tt.args); got != tt.want {
				t.Errorf("ExtractResumeID(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

// writeTranscript creates projects/<slug>/<id>.jsonl with the given content.
func writeTranscript(t *testing.T, cfg, slug, id, content string) string {
	t.Helper()
	dir := filepath.Join(cfg, "projects", slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFindTranscript(t *testing.T) {
	cfg := t.TempDir()
	writeTranscript(t, cfg, "-home-u-projB", "sess-1", "{}\n")
	writeTranscript(t, cfg, "-home-u-projA", "sess-1", "{}\n")
	writeTranscript(t, cfg, "-home-u-projC", "other", "{}\n")
	// A stray file (not dir) under projects/ must be ignored.
	if err := os.WriteFile(filepath.Join(cfg, "projects", "stray.jsonl"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := FindTranscript(cfg, "sess-1")
	want := []string{"-home-u-projA", "-home-u-projB"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FindTranscript = %v, want %v", got, want)
	}

	if got := FindTranscript(cfg, "missing"); got != nil {
		t.Errorf("FindTranscript(missing) = %v, want nil", got)
	}
	if got := FindTranscript(cfg, "../escape"); got != nil {
		t.Errorf("FindTranscript with path traversal = %v, want nil", got)
	}
	if got := FindTranscript("", "sess-1"); got != nil {
		t.Errorf("FindTranscript with empty config dir = %v, want nil", got)
	}
	if got := FindTranscript(filepath.Join(cfg, "nonexistent"), "sess-1"); got != nil {
		t.Errorf("FindTranscript with missing projects dir = %v, want nil", got)
	}
}

func TestCopyTranscript(t *testing.T) {
	t.Run("copies into new project dir", func(t *testing.T) {
		cfg := t.TempDir()
		writeTranscript(t, cfg, "-old-slug", "sess-1", "line1\nline2\n")

		copied, err := CopyTranscript(cfg, "-old-slug", "-new-slug", "sess-1")
		if err != nil {
			t.Fatalf("CopyTranscript: %v", err)
		}
		if !copied {
			t.Fatal("expected copied=true")
		}
		data, err := os.ReadFile(filepath.Join(cfg, "projects", "-new-slug", "sess-1.jsonl"))
		if err != nil {
			t.Fatalf("reading copy: %v", err)
		}
		if string(data) != "line1\nline2\n" {
			t.Errorf("copy content = %q", data)
		}
		// Original must remain.
		if _, err := os.Stat(filepath.Join(cfg, "projects", "-old-slug", "sess-1.jsonl")); err != nil {
			t.Errorf("original removed: %v", err)
		}
		// No temp litter in the destination dir.
		entries, err := os.ReadDir(filepath.Join(cfg, "projects", "-new-slug"))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Name() != "sess-1.jsonl" {
			t.Errorf("unexpected entries in destination: %v", entries)
		}
	})

	t.Run("refuses to overwrite existing destination", func(t *testing.T) {
		cfg := t.TempDir()
		writeTranscript(t, cfg, "-old-slug", "sess-1", "new content\n")
		writeTranscript(t, cfg, "-new-slug", "sess-1", "existing content\n")

		copied, err := CopyTranscript(cfg, "-old-slug", "-new-slug", "sess-1")
		if err != nil {
			t.Fatalf("CopyTranscript: %v", err)
		}
		if copied {
			t.Error("expected copied=false for existing destination")
		}
		data, _ := os.ReadFile(filepath.Join(cfg, "projects", "-new-slug", "sess-1.jsonl"))
		if string(data) != "existing content\n" {
			t.Errorf("destination was overwritten: %q", data)
		}
	})

	t.Run("same slug is a no-op", func(t *testing.T) {
		cfg := t.TempDir()
		writeTranscript(t, cfg, "-slug", "sess-1", "x\n")
		copied, err := CopyTranscript(cfg, "-slug", "-slug", "sess-1")
		if err != nil || copied {
			t.Errorf("same-slug copy = (%v, %v), want (false, nil)", copied, err)
		}
	})

	t.Run("missing source errors", func(t *testing.T) {
		cfg := t.TempDir()
		if _, err := CopyTranscript(cfg, "-old", "-new", "sess-1"); err == nil {
			t.Error("expected error for missing source")
		}
	})

	t.Run("invalid session id errors", func(t *testing.T) {
		cfg := t.TempDir()
		if _, err := CopyTranscript(cfg, "-old", "-new", "../escape"); err == nil {
			t.Error("expected error for traversal id")
		}
	})
}
