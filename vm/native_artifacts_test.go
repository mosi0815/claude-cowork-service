package vm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/patrickjaja/claude-cowork-service/pipe"
)

// rootRel converts an absolute path into the root-relative form Desktop uses
// for mount specs ("home/u/..."). hostAbsFromSharedWithHome rejects absolute
// paths, so tests must feed it the same shape production traffic has.
func rootRel(path string) string {
	return strings.TrimPrefix(path, "/")
}

func mustMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", dir, err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink %s -> %s: %v", link, target, err)
	}
}

func TestSanitizeNativeArtifactsRemovesSessionMntSymlinks(t *testing.T) {
	home := t.TempDir()
	mnt := filepath.Join(home, ".local", "share", "claude-cowork", "sessions", "sess", "mnt")
	mustMkdirAll(t, mnt)

	wsTarget := filepath.Join(home, "workspace")
	mustMkdirAll(t, wsTarget)
	mustSymlink(t, wsTarget, filepath.Join(mnt, "ws"))
	// Dangling symlink - exactly what the guest trips over.
	mustSymlink(t, filepath.Join(home, "does-not-exist"), filepath.Join(mnt, ".claude"))

	realDir := filepath.Join(mnt, "realdir")
	mustMkdirAll(t, realDir)
	mustWriteFile(t, filepath.Join(realDir, "keep.txt"), "keep")
	mustWriteFile(t, filepath.Join(mnt, "afile"), "data")

	sanitizeNativeArtifacts(home, "sess", nil, false)

	for _, gone := range []string{"ws", ".claude"} {
		if _, err := os.Lstat(filepath.Join(mnt, gone)); !os.IsNotExist(err) {
			t.Errorf("symlink %q still present (Lstat err=%v)", gone, err)
		}
	}
	if _, err := os.Stat(filepath.Join(realDir, "keep.txt")); err != nil {
		t.Errorf("real dir content was touched: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mnt, "afile")); err != nil {
		t.Errorf("real file was touched: %v", err)
	}
}

func TestSanitizeNativeArtifactsRemovesNestedMountArtifacts(t *testing.T) {
	t.Run("removes symlink inside parent mount source", func(t *testing.T) {
		home := t.TempDir()
		parentSrc := filepath.Join(home, "cfg", ".claude")
		mustMkdirAll(t, parentSrc)
		skillsSrc := filepath.Join(home, "plugins", "skills")
		mustMkdirAll(t, skillsSrc)

		// Native-era artifact: nested mount symlink stranded in the parent
		// mount's source dir, next to real user data.
		mustSymlink(t, skillsSrc, filepath.Join(parentSrc, "skills"))
		mustMkdirAll(t, filepath.Join(parentSrc, "projects"))
		mustWriteFile(t, filepath.Join(parentSrc, "settings.json"), "{}")

		mounts := map[string]pipe.MountSpec{
			".claude":        {Path: rootRel(parentSrc)},
			".claude/skills": {Path: rootRel(skillsSrc)},
		}
		sanitizeNativeArtifacts(home, "", mounts, false)

		if _, err := os.Lstat(filepath.Join(parentSrc, "skills")); !os.IsNotExist(err) {
			t.Errorf("nested-mount symlink still present (Lstat err=%v)", err)
		}
		if _, err := os.Stat(filepath.Join(parentSrc, "projects")); err != nil {
			t.Errorf("sibling real dir was touched: %v", err)
		}
		if _, err := os.Stat(filepath.Join(parentSrc, "settings.json")); err != nil {
			t.Errorf("sibling real file was touched: %v", err)
		}
	})

	t.Run("nested mount without parent in map is left alone", func(t *testing.T) {
		home := t.TempDir()
		parentSrc := filepath.Join(home, "cfg", ".claude")
		mustMkdirAll(t, parentSrc)
		mustSymlink(t, filepath.Join(home, "elsewhere"), filepath.Join(parentSrc, "skills"))

		// Only the nested name is mounted - with no ".claude" parent there
		// is no source dir to resolve the artifact under.
		mounts := map[string]pipe.MountSpec{
			".claude/skills": {Path: rootRel(filepath.Join(home, "elsewhere"))},
		}
		sanitizeNativeArtifacts(home, "", mounts, false)

		if _, err := os.Lstat(filepath.Join(parentSrc, "skills")); err != nil {
			t.Errorf("symlink removed despite missing parent mount: %v", err)
		}
	})

	t.Run("nested artifact that is a real dir is untouched", func(t *testing.T) {
		home := t.TempDir()
		parentSrc := filepath.Join(home, "cfg", ".claude")
		realSkills := filepath.Join(parentSrc, "skills")
		mustMkdirAll(t, realSkills)
		mustWriteFile(t, filepath.Join(realSkills, "user.md"), "user data")

		mounts := map[string]pipe.MountSpec{
			".claude":        {Path: rootRel(parentSrc)},
			".claude/skills": {Path: rootRel(filepath.Join(home, "plugins", "skills"))},
		}
		sanitizeNativeArtifacts(home, "", mounts, false)

		if _, err := os.Stat(filepath.Join(realSkills, "user.md")); err != nil {
			t.Errorf("real dir at nested-mount location was touched: %v", err)
		}
	})
}

func TestSanitizeNativeArtifactsMissingSessionDirNoop(t *testing.T) {
	home := t.TempDir()

	sanitizeNativeArtifacts(home, "no-such-session", nil, false)

	// Must neither panic nor create the sessions tree.
	if _, err := os.Stat(filepath.Join(home, ".local")); !os.IsNotExist(err) {
		t.Errorf("sanitize created state under home (Stat err=%v)", err)
	}
}

func TestMigrateTranscriptForResume(t *testing.T) {
	const sessionID = "sess-1"
	const content = "line1\nline2\n"

	// setup builds a fake .claude config dir holding a native-era transcript
	// under a host-cwd slug, plus the mount map pointing at it.
	setup := func(t *testing.T) (home, cfg string, mounts map[string]pipe.MountSpec) {
		t.Helper()
		home = t.TempDir()
		cfg = filepath.Join(home, "cfg")
		oldDir := filepath.Join(cfg, "projects", "-home-u-old")
		mustMkdirAll(t, oldDir)
		mustWriteFile(t, filepath.Join(oldDir, sessionID+".jsonl"), content)
		mounts = map[string]pipe.MountSpec{".claude": {Path: rootRel(cfg)}}
		return home, cfg, mounts
	}

	countProjectDirs := func(t *testing.T, cfg string) int {
		t.Helper()
		entries, err := os.ReadDir(filepath.Join(cfg, "projects"))
		if err != nil {
			t.Fatalf("ReadDir projects: %v", err)
		}
		n := 0
		for _, e := range entries {
			if e.IsDir() {
				n++
			}
		}
		return n
	}

	t.Run("copies transcript to guest cwd slug", func(t *testing.T) {
		home, cfg, mounts := setup(t)

		migrateTranscriptForResume(home, []string{"--resume", sessionID}, "/sessions/mysess", mounts, false)

		got, err := os.ReadFile(filepath.Join(cfg, "projects", "-sessions-mysess", sessionID+".jsonl"))
		if err != nil {
			t.Fatalf("migrated transcript missing: %v", err)
		}
		if string(got) != content {
			t.Errorf("migrated transcript content = %q, want %q", got, content)
		}
		if _, err := os.Stat(filepath.Join(cfg, "projects", "-home-u-old", sessionID+".jsonl")); err != nil {
			t.Errorf("original transcript no longer present: %v", err)
		}
	})

	t.Run("already matching slug creates nothing", func(t *testing.T) {
		home, cfg, mounts := setup(t)
		matching := filepath.Join(cfg, "projects", "-sessions-mysess")
		mustMkdirAll(t, matching)
		mustWriteFile(t, filepath.Join(matching, sessionID+".jsonl"), content)

		migrateTranscriptForResume(home, []string{"--resume", sessionID}, "/sessions/mysess", mounts, false)

		if got := countProjectDirs(t, cfg); got != 2 {
			t.Errorf("project dir count = %d, want 2 (no duplicate)", got)
		}
	})

	t.Run("no resume arg does nothing", func(t *testing.T) {
		home, cfg, mounts := setup(t)

		migrateTranscriptForResume(home, []string{"-p", "--verbose"}, "/sessions/mysess", mounts, false)

		if got := countProjectDirs(t, cfg); got != 1 {
			t.Errorf("project dir count = %d, want 1", got)
		}
	})

	t.Run("no .claude mount does nothing", func(t *testing.T) {
		home, cfg, _ := setup(t)

		migrateTranscriptForResume(home, []string{"--resume", sessionID}, "/sessions/mysess", nil, false)

		if got := countProjectDirs(t, cfg); got != 1 {
			t.Errorf("project dir count = %d, want 1", got)
		}
	})
}
