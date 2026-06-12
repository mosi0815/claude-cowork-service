package vm

import (
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/patrickjaja/claude-cowork-service/pipe"
	"github.com/patrickjaja/claude-cowork-service/transcript"
)

// sanitizeNativeArtifacts removes symlinks the native backend left behind in
// session state that the KVM backend now shares with the guest. The native
// Spawn creates absolute symlinks at
// ~/.local/share/claude-cowork/sessions/<name>/mnt/<mountName> for every
// mount; for NESTED mount names (".claude/skills", "<folder>/.mcpb-cache")
// the symlink is created through the parent symlink, so it physically lands
// inside the parent mount's source directory. Inside the guest those absolute
// targets do not exist, and the dangling links make the guest sdk-daemon's
// mountpoint MkdirAll fail with EEXIST ("failed to create mount point").
// The guest binary is upstream and unmodifiable, so we sanitize host-side.
// Every one of these links is daemon-created and the native backend recreates
// them on demand, so removing them on a kvm spawn is always safe.
func sanitizeNativeArtifacts(home, sessionName string, mounts map[string]pipe.MountSpec, debug bool) {
	// Session mnt/ directory: every symlink in there is a native-backend
	// artifact. Never remove non-symlinks.
	if sessionName != "" {
		dir := filepath.Join(home, ".local", "share", "claude-cowork", "sessions", sessionName, "mnt")
		entries, err := os.ReadDir(dir)
		if err != nil {
			// Missing dir means no native-era state - nothing to do.
			entries = nil
		}
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink == 0 {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			if err := os.Remove(path); err != nil {
				log.Printf("[kvm] removing native-era symlink %s: %v", path, err)
				continue
			}
			log.Printf("[kvm] removed native-era symlink %s", path)
		}
	}

	// Nested-mount artifacts stranded inside the SOURCE directory of their
	// parent mount. Iterate sorted for deterministic behavior and logs.
	names := make([]string, 0, len(mounts))
	for name := range mounts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !strings.Contains(name, "/") {
			continue
		}
		// The artifact lives under the longest enclosing mount's source dir
		// (".claude/skills/x" nests under ".claude/skills" before ".claude").
		parent := ""
		for _, p := range names {
			if p != name && strings.HasPrefix(name, p+"/") && len(p) > len(parent) {
				parent = p
			}
		}
		if parent == "" {
			continue
		}
		parentSrc, err := hostAbsFromSharedWithHome(mounts[parent].Path, home)
		if err != nil {
			if debug {
				log.Printf("[kvm] sanitize: cannot resolve parent mount %s=%q: %v", parent, mounts[parent].Path, err)
			}
			continue
		}
		artifact := filepath.Join(parentSrc, name[len(parent)+1:])
		// Safety: the parent source dir holds user data. Only ever remove
		// symlinks - never directories or regular files.
		info, err := os.Lstat(artifact)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		if err := os.Remove(artifact); err != nil {
			log.Printf("[kvm] removing native-era symlink %s (nested mount %s): %v", artifact, name, err)
			continue
		}
		log.Printf("[kvm] removed native-era symlink %s (nested mount %s)", artifact, name)
	}
}

// migrateTranscriptForResume keeps `claude --resume <id>` working when a
// session created under the native backend is resumed in the guest. The
// native backend stores transcripts under a host-cwd slug in
// <.claude mount source>/projects/, but the guest CLI's cwd is
// /sessions/<name>, so it resolves --resume under the "-sessions-<name>"
// slug, finds nothing, and reports "No conversation found" - Desktop then
// drops the cliSessionId and orphans the session. Copy (never move) the
// transcript to the slug the guest will look under. Best effort only:
// failures are logged and never fail the spawn.
func migrateTranscriptForResume(home string, args []string, cwd string, mounts map[string]pipe.MountSpec, debug bool) {
	id := transcript.ExtractResumeID(args)
	if id == "" {
		return
	}
	spec, ok := mounts[".claude"]
	if !ok || spec.Path == "" {
		if debug {
			log.Printf("[kvm] resume: no .claude mount; skipping transcript migration")
		}
		return
	}
	cfg, err := hostAbsFromSharedWithHome(spec.Path, home)
	if err != nil {
		log.Printf("[kvm] resume: cannot resolve .claude mount %q: %v", spec.Path, err)
		return
	}
	// cwd exactly as Desktop sent it - that is the cwd the guest CLI runs
	// with, hence the project slug it resolves --resume under.
	want := transcript.Slugify(cwd)
	if want == "" {
		log.Printf("[kvm] resume: cwd %q yields no usable project slug; skipping transcript migration", cwd)
		return
	}
	dirs := transcript.FindTranscript(cfg, id)
	if len(dirs) == 0 {
		log.Printf("[kvm] resume: no transcript found for %s under %s - CLI will report it", id, cfg)
		return
	}
	for _, d := range dirs {
		if d == want {
			// Already where the guest CLI will look (normal kvm resume).
			return
		}
	}
	copied, err := transcript.CopyTranscript(cfg, dirs[0], want, id)
	if err != nil {
		log.Printf("[kvm] resume: transcript migration for %s failed: %v", id, err)
		return
	}
	if copied {
		log.Printf("[kvm] resume: migrated transcript %s from %s to %s", id, dirs[0], want)
	} else {
		log.Printf("[kvm] resume: transcript %s already present at %s", id, want)
	}
}
