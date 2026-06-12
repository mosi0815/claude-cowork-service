// Package transcript replicates the Claude CLI's on-disk transcript layout
// ($CLAUDE_CONFIG_DIR/projects/<slug(cwd)>/<sessionId>.jsonl) so backends can
// keep `claude --resume <id>` working when the spawn cwd differs from the cwd
// the transcript was created under. The CLI resolves --resume strictly under
// the project directory derived from its current cwd; a mismatch surfaces as
// "No conversation found with session ID" and Claude Desktop responds by
// discarding the session's cliSessionId, orphaning the transcript.
package transcript

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// maxSlugLen mirrors the CLI's project-dir truncation threshold. Past this
// length the CLI appends a hash we cannot reproduce, so callers must skip
// transcript handling instead of guessing.
const maxSlugLen = 200

// Slugify converts a cwd path to the CLI's project directory name: every
// character outside [a-zA-Z0-9] becomes '-'. Returns "" when the result would
// exceed the CLI's truncation threshold.
func Slugify(path string) string {
	var b strings.Builder
	b.Grow(len(path))
	for _, r := range path {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	s := b.String()
	if len(s) > maxSlugLen {
		return ""
	}
	return s
}

// ExtractResumeID returns the session id passed via "--resume <id>" or
// "--resume=<id>", or "" when absent.
func ExtractResumeID(args []string) string {
	for i, a := range args {
		if a == "--resume" {
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
		if v, ok := strings.CutPrefix(a, "--resume="); ok {
			return v
		}
	}
	return ""
}

// validSessionID rejects ids that could escape the projects tree. Session ids
// arrive via RPC args, so treat them as untrusted path components.
func validSessionID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	return !strings.ContainsAny(id, "/\\")
}

// FindTranscript returns the sorted project-dir names under
// <claudeConfigDir>/projects that contain <sessionID>.jsonl.
func FindTranscript(claudeConfigDir, sessionID string) []string {
	if claudeConfigDir == "" || !validSessionID(sessionID) {
		return nil
	}
	projectsDir := filepath.Join(claudeConfigDir, "projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(projectsDir, e.Name(), sessionID+".jsonl")); err == nil {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Strings(dirs)
	return dirs
}

// CopyTranscript copies <sessionID>.jsonl from one project dir to another
// inside the same config dir, creating the destination dir as needed. The
// write is atomic (temp file + rename, same filesystem). Returns (false, nil)
// without touching anything when the destination file already exists or the
// slugs are identical — never overwrites.
func CopyTranscript(claudeConfigDir, fromSlug, toSlug, sessionID string) (bool, error) {
	if !validSessionID(sessionID) {
		return false, fmt.Errorf("invalid session id %q", sessionID)
	}
	if fromSlug == "" || toSlug == "" || fromSlug == toSlug {
		return false, nil
	}
	projectsDir := filepath.Join(claudeConfigDir, "projects")
	src := filepath.Join(projectsDir, fromSlug, sessionID+".jsonl")
	dstDir := filepath.Join(projectsDir, toSlug)
	dst := filepath.Join(dstDir, sessionID+".jsonl")

	if _, err := os.Stat(dst); err == nil {
		return false, nil
	}
	in, err := os.Open(src)
	if err != nil {
		return false, fmt.Errorf("opening source transcript: %w", err)
	}
	defer func() { _ = in.Close() }()

	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return false, fmt.Errorf("creating project dir: %w", err)
	}
	tmp, err := os.CreateTemp(dstDir, "."+sessionID+".tmp-*")
	if err != nil {
		return false, fmt.Errorf("creating temp transcript: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return false, fmt.Errorf("copying transcript: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return false, fmt.Errorf("closing temp transcript: %w", err)
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		_ = os.Remove(tmpPath)
		return false, fmt.Errorf("renaming transcript into place: %w", err)
	}
	return true, nil
}
