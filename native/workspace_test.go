package native

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/patrickjaja/claude-cowork-service/pipe"
	"github.com/patrickjaja/claude-cowork-service/transcript"
)

// makeWorkspaceMounts creates one real directory per name under home and
// returns mounts keyed by name. Mount paths are root-relative (leading "/"
// stripped) so resolveSubpath runs the same code path as production traffic.
func makeWorkspaceMounts(t *testing.T, home string, names ...string) (map[string]pipe.MountSpec, map[string]string) {
	t.Helper()
	mounts := make(map[string]pipe.MountSpec, len(names))
	paths := make(map[string]string, len(names))
	for _, n := range names {
		p := filepath.Join(home, n)
		if err := os.Mkdir(p, 0755); err != nil {
			t.Fatal(err)
		}
		paths[n] = p
		mounts[n] = pipe.MountSpec{Path: strings.TrimPrefix(p, "/"), Mode: "rw"}
	}
	return mounts, paths
}

// writeTranscript creates <cfgDir>/projects/<slug>/<id>.jsonl and returns its path.
func writeTranscript(t *testing.T, cfgDir, slug, id string) string {
	t.Helper()
	dir := filepath.Join(cfgDir, "projects", slug)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, []byte("{\"type\":\"summary\"}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestChooseSpawnCwdDeterministic(t *testing.T) {
	home := t.TempDir()
	mounts, paths := makeWorkspaceMounts(t, home, "beta", "alpha", "gamma", "delta", "epsilon")

	// Go map iteration order is randomized per run; 50 calls flush out any
	// order-dependence like the pre-fix range+break selection.
	results := make(map[string]struct{})
	for i := 0; i < 50; i++ {
		results[chooseSpawnCwd(home, "/fallback", nil, nil, mounts, false)] = struct{}{}
	}
	if len(results) != 1 {
		t.Fatalf("got %d distinct results over 50 runs, want 1: %v", len(results), results)
	}
	if _, ok := results[paths["alpha"]]; !ok {
		t.Errorf("chose %v, want lexicographically-smallest mount %q", results, paths["alpha"])
	}
}

func TestChooseSpawnCwdWorkspaceHostPathsPriority(t *testing.T) {
	home := t.TempDir()
	mounts, paths := makeWorkspaceMounts(t, home, "alpha", "beta", "gamma")

	tests := []struct {
		name      string
		hostPaths string
		want      string
	}{
		{
			name:      "AbsoluteEntryPromotesGamma",
			hostPaths: paths["gamma"] + "|" + paths["alpha"],
			want:      paths["gamma"],
		},
		{
			name:      "RootRelativeEntryPromotesGamma",
			hostPaths: strings.TrimPrefix(paths["gamma"], "/"),
			want:      paths["gamma"],
		},
		{
			name:      "UnknownEntryIgnoredFallsBackToSorted",
			hostPaths: filepath.Join(home, "not-a-mount"),
			want:      paths["alpha"],
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{"CLAUDE_CODE_WORKSPACE_HOST_PATHS": tt.hostPaths}
			got := chooseSpawnCwd(home, "/fallback", nil, env, mounts, false)
			if got != tt.want {
				t.Errorf("chooseSpawnCwd = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestChooseSpawnCwdResumeMatch(t *testing.T) {
	home := t.TempDir()
	mounts, paths := makeWorkspaceMounts(t, home, "alpha", "gamma")
	cfgDir := filepath.Join(home, "claude-config")
	mounts[".claude"] = pipe.MountSpec{Path: strings.TrimPrefix(cfgDir, "/"), Mode: "rw"}
	writeTranscript(t, cfgDir, transcript.Slugify(paths["gamma"]), "sess-1")

	got := chooseSpawnCwd(home, "/fallback", []string{"--resume", "sess-1"}, nil, mounts, false)
	if got != paths["gamma"] {
		t.Errorf("chooseSpawnCwd = %q, want transcript home %q (fresh rule would pick %q)",
			got, paths["gamma"], paths["alpha"])
	}
}

func TestChooseSpawnCwdResumeNoMatchCopies(t *testing.T) {
	home := t.TempDir()
	mounts, paths := makeWorkspaceMounts(t, home, "alpha", "beta")
	cfgDir := filepath.Join(home, "claude-config")
	mounts[".claude"] = pipe.MountSpec{Path: strings.TrimPrefix(cfgDir, "/"), Mode: "rw"}
	// Transcript exists only under a slug no candidate can produce
	// (kvm -> native migration, or the original folder was unmounted).
	original := writeTranscript(t, cfgDir, "-some-foreign-slug", "sess-1")

	got := chooseSpawnCwd(home, "/fallback", []string{"--resume", "sess-1"}, nil, mounts, false)
	if got != paths["alpha"] {
		t.Fatalf("chooseSpawnCwd = %q, want deterministic first candidate %q", got, paths["alpha"])
	}
	healed := filepath.Join(cfgDir, "projects", transcript.Slugify(paths["alpha"]), "sess-1.jsonl")
	if _, err := os.Stat(healed); err != nil {
		t.Errorf("transcript not copied to %s: %v", healed, err)
	}
	if _, err := os.Stat(original); err != nil {
		t.Errorf("original transcript missing after copy-heal: %v", err)
	}
}

func TestChooseSpawnCwdNoEligibleMounts(t *testing.T) {
	home := t.TempDir()
	// All names are filtered by the eligibility rules even though the
	// directories exist on disk.
	mounts, _ := makeWorkspaceMounts(t, home, "outputs", "uploads", ".claude")

	got := chooseSpawnCwd(home, "/session/dir", nil, nil, mounts, false)
	if got != "/session/dir" {
		t.Errorf("chooseSpawnCwd = %q, want currentCwd /session/dir unchanged", got)
	}
}

func TestChooseSpawnCwdStatFailingMountExcluded(t *testing.T) {
	home := t.TempDir()
	mounts, paths := makeWorkspaceMounts(t, home, "zzz-exists")
	// "aaa-missing" sorts before "zzz-exists" but its host path does not exist.
	missing := filepath.Join(home, "aaa-missing")
	mounts["aaa-missing"] = pipe.MountSpec{Path: strings.TrimPrefix(missing, "/"), Mode: "rw"}

	got := chooseSpawnCwd(home, "/fallback", nil, nil, mounts, false)
	if got != paths["zzz-exists"] {
		t.Errorf("chooseSpawnCwd = %q, want existing mount %q (missing mount must be excluded)",
			got, paths["zzz-exists"])
	}
}

func TestEligibleWorkspaceMountsSorted(t *testing.T) {
	home := t.TempDir()
	mounts, paths := makeWorkspaceMounts(t, home, "zeta", "mid", "abc", "uploads", "outputs", ".hidden")
	// Regular-file target: excluded because cwd must be a directory.
	filePath := filepath.Join(home, "file-mount")
	if err := os.WriteFile(filePath, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	mounts["file-mount"] = pipe.MountSpec{Path: strings.TrimPrefix(filePath, "/"), Mode: "rw"}
	// Non-existent target: excluded by the stat check.
	mounts["gone"] = pipe.MountSpec{Path: strings.TrimPrefix(filepath.Join(home, "gone"), "/"), Mode: "rw"}

	got := eligibleWorkspaceMounts(home, mounts)
	want := []wsMount{
		{name: "abc", hostPath: paths["abc"]},
		{name: "mid", hostPath: paths["mid"]},
		{name: "zeta", hostPath: paths["zeta"]},
	}
	if len(got) != len(want) {
		t.Fatalf("eligibleWorkspaceMounts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("eligible[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestResolveClaudeConfigDir(t *testing.T) {
	home := t.TempDir()
	cfgDir := filepath.Join(home, "cfg")

	t.Run("MountWinsOverEnv", func(t *testing.T) {
		mounts := map[string]pipe.MountSpec{
			".claude": {Path: strings.TrimPrefix(cfgDir, "/"), Mode: "rw"},
		}
		env := map[string]string{"CLAUDE_CONFIG_DIR": "/somewhere/else"}
		if got := resolveClaudeConfigDir(home, env, mounts); got != cfgDir {
			t.Errorf("got %q, want mount-derived %q", got, cfgDir)
		}
	})

	t.Run("EnvFallback", func(t *testing.T) {
		env := map[string]string{"CLAUDE_CONFIG_DIR": "/already/remapped/cfg"}
		if got := resolveClaudeConfigDir(home, env, nil); got != "/already/remapped/cfg" {
			t.Errorf("got %q, want env value returned as-is", got)
		}
	})

	t.Run("EmptyMountPathFallsToEnv", func(t *testing.T) {
		mounts := map[string]pipe.MountSpec{".claude": {Path: "", Mode: "rw"}}
		env := map[string]string{"CLAUDE_CONFIG_DIR": "/env/cfg"}
		if got := resolveClaudeConfigDir(home, env, mounts); got != "/env/cfg" {
			t.Errorf("got %q, want env fallback /env/cfg", got)
		}
	})

	t.Run("NeitherReturnsEmpty", func(t *testing.T) {
		if got := resolveClaudeConfigDir(home, nil, nil); got != "" {
			t.Errorf("got %q, want empty string", got)
		}
	})
}
