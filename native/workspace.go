package native

import (
	"log"
	"os"
	"sort"
	"strings"

	"github.com/patrickjaja/claude-cowork-service/pipe"
	"github.com/patrickjaja/claude-cowork-service/transcript"
)

// wsMount is a workspace mount that passed eligibility filtering, paired with
// its resolved host path.
type wsMount struct {
	name     string
	hostPath string
}

// eligibleWorkspaceMounts returns the mounts that can serve as the spawn cwd:
// real user workspaces, not infrastructure mounts ("."-prefixed like .claude,
// or the session-internal uploads/outputs dirs). Host paths are resolved via
// resolveSubpath and must exist as directories - a cwd that fails chdir kills
// the spawn. The result is sorted by mount name so callers see a stable order
// instead of Go's randomized map iteration (the root cause of issue #66).
func eligibleWorkspaceMounts(home string, mounts map[string]pipe.MountSpec) []wsMount {
	var eligible []wsMount
	for name, mount := range mounts {
		if strings.HasPrefix(name, ".") || name == "uploads" || name == "outputs" {
			continue
		}
		hostPath := resolveSubpath(home, mount.Path)
		if info, err := os.Stat(hostPath); err != nil || !info.IsDir() {
			continue
		}
		eligible = append(eligible, wsMount{name: name, hostPath: hostPath})
	}
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].name < eligible[j].name })
	return eligible
}

// resolveClaudeConfigDir returns the host path of the CLI config dir, where
// transcripts live under projects/. The ".claude" mount is authoritative
// (Desktop mounts the config dir into every session); CLAUDE_CONFIG_DIR is
// the fallback and is returned as-is because the caller already remapped it
// from the /sessions/<name> form. Empty when neither is available - resume
// transcript handling must then be skipped.
func resolveClaudeConfigDir(home string, env map[string]string, mounts map[string]pipe.MountSpec) string {
	if m, ok := mounts[".claude"]; ok && m.Path != "" {
		return resolveSubpath(home, m.Path)
	}
	if v := env["CLAUDE_CONFIG_DIR"]; v != "" {
		return v
	}
	return ""
}

// orderedCwdCandidates ranks eligible workspace host paths for cwd selection.
// CLAUDE_CODE_WORKSPACE_HOST_PATHS (pipe-delimited, in the order the user
// attached folders) promotes matching mounts to the front in env order; the
// remaining eligible mounts follow in mount-name order. Entries arrive in
// mixed forms (absolute, root-relative, home-relative) and may use symlinked
// spellings of the same directory, so matching falls back to canonicalized
// comparison. Paths not backed by an eligible mount are never returned.
func orderedCwdCandidates(home string, env map[string]string, eligible []wsMount) []string {
	seen := make(map[string]bool, len(eligible))
	candidates := make([]string, 0, len(eligible))
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			candidates = append(candidates, p)
		}
	}
	if raw := env["CLAUDE_CODE_WORKSPACE_HOST_PATHS"]; raw != "" {
		canon := make([]string, len(eligible))
		for i, m := range eligible {
			canon[i] = canonicalizePath(m.hostPath)
		}
		for _, entry := range strings.Split(raw, "|") {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			want := entry
			if !strings.HasPrefix(entry, "/") {
				want = resolveSubpath(home, entry)
			}
			wantCanon := canonicalizePath(want)
			for i, m := range eligible {
				if m.hostPath == want || canon[i] == wantCanon {
					add(m.hostPath)
				}
			}
		}
	}
	for _, m := range eligible {
		add(m.hostPath)
	}
	return candidates
}

// chooseSpawnCwd picks the working directory for a spawned CLI process.
//
// The CLI stores transcripts under $CLAUDE_CONFIG_DIR/projects/<slug(cwd)>
// and resolves --resume strictly within the slug of its current cwd, so the
// choice must be deterministic across spawns of the same session (issue #66:
// random map iteration picked a different mount after process exit, breaking
// resume). Selection order:
//
//  1. No eligible workspace mounts: keep currentCwd (plain sessions keep
//     today's behavior).
//  2. --resume present and a candidate's slug already holds the transcript:
//     use that candidate, even when the fresh rule would pick another.
//  3. --resume present, transcript exists only under a foreign slug (kvm to
//     native migration, or the original folder is no longer mounted): copy
//     the transcript into the first candidate's project dir, then use it.
//  4. Otherwise: first candidate (env-hint order, then mount-name order).
//
// Abnormal/healing outcomes log unconditionally - they are rare and the only
// trail when a resume goes sideways. The routine fresh-rule line stays
// debug-gated like the code it replaced.
func chooseSpawnCwd(home, currentCwd string, args []string, env map[string]string, mounts map[string]pipe.MountSpec, debug bool) string {
	eligible := eligibleWorkspaceMounts(home, mounts)
	if len(eligible) == 0 {
		return currentCwd
	}
	candidates := orderedCwdCandidates(home, env, eligible)

	if id := transcript.ExtractResumeID(args); id != "" {
		if cfg := resolveClaudeConfigDir(home, env, mounts); cfg == "" {
			log.Printf("[native] resume: no claude config dir resolvable; using fresh cwd rule")
		} else {
			dirs := transcript.FindTranscript(cfg, id)
			dirSet := make(map[string]bool, len(dirs))
			for _, d := range dirs {
				dirSet[d] = true
			}
			for _, c := range append(append([]string{}, candidates...), currentCwd) {
				if s := transcript.Slugify(c); s != "" && dirSet[s] {
					log.Printf("[native] resume: transcript %s found under slug of %s - using as cwd", id, c)
					return c
				}
			}
			if len(dirs) > 0 {
				// The transcript exists but was created under a cwd we can no
				// longer pick. Heal by copying it under the slug of the cwd we
				// are about to use; a copy failure must not block the spawn
				// (the CLI just reports the missing conversation).
				chosen := candidates[0]
				if s := transcript.Slugify(chosen); s != "" {
					copied, err := transcript.CopyTranscript(cfg, dirs[0], s, id)
					switch {
					case err != nil:
						log.Printf("[native] resume: transcript %s copy %s -> %s failed: %v", id, dirs[0], s, err)
					case copied:
						log.Printf("[native] resume: transcript %s copied %s -> %s to keep --resume working", id, dirs[0], s)
					default:
						log.Printf("[native] resume: transcript %s already present in %s; copy from %s skipped", id, s, dirs[0])
					}
				}
				return chosen
			}
			log.Printf("[native] resume: no transcript found for %s - CLI will report it and Desktop starts a fresh session", id)
		}
	}

	chosen := candidates[0]
	if debug {
		log.Printf("[native] using workspace mount as cwd: %s (was %s)", chosen, currentCwd)
	}
	return chosen
}
