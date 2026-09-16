package cli

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/RedeployAB/loft/internal/siterules"
)

// Guardrails: make the common mistakes impossible rather than merely warned. The rules themselves
// live in siterules, shared with the deploy API; this file applies them to a local folder before
// anything is uploaded, so `loft deploy .` from a project root (node_modules, a leaked .env), a stray
// huge video, or a folder that serves nothing is refused with every reason listed at once.

type fileEntry struct {
	abs  string
	rel  string // posix path within the site
	size int64
}

func collect(localDir string) ([]fileEntry, error) {
	var out []fileEntry
	err := filepath.WalkDir(localDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Skip symlinks: only regular files inside the folder are deployed, so a link can't pull in
		// content from outside the tree (or an unreadable/secret target) on upload.
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(localDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if siterules.Check(rel).Kind == siterules.Skip {
			return nil
		}
		out = append(out, fileEntry{abs: path, rel: rel, size: info.Size()})
		return nil
	})
	return out, err
}

// validate returns a single error listing every reason the folder can't be deployed, or nil.
func validate(entries []fileEntry) error {
	var problems []string
	add := func(s string) { problems = append(problems, s) }

	if len(entries) == 0 {
		add("the folder is empty — nothing to deploy")
	}
	if len(entries) > siterules.MaxFiles {
		add(fmt.Sprintf("too many files (%d > %d) — deploy a build output, not a project root", len(entries), siterules.MaxFiles))
	}
	if !hasRoot(entries, siterules.IndexFile) {
		add("no " + siterules.IndexFile + " at the root — a site needs an entry point (try deploying your build dir, e.g. ./dist)")
	}
	// Unlike the API, which stops at the first refused file, list every offender grouped by rule.
	byKind := map[siterules.Kind][]string{}
	for _, e := range entries {
		if v := siterules.Check(e.rel); v.Kind != siterules.OK {
			byKind[v.Kind] = append(byKind[v.Kind], e.rel)
		}
	}
	if under := byKind[siterules.ProjectFolder]; len(under) > 0 {
		d := siterules.BlockedDir(under[0])
		add(fmt.Sprintf("found %s/ — deploy a built site, not a project folder (exclude %s or deploy ./dist)", d, d))
	}
	if secrets := byKind[siterules.Secret]; len(secrets) > 0 {
		add("refusing to upload secret files: " + strings.Join(secrets, ", "))
	}
	if bad := byKind[siterules.NotAsset]; len(bad) > 0 {
		add("disallowed file types (static web assets only): " + preview(bad))
	}
	var tooBig []string
	var total int64
	for _, e := range entries {
		total += e.size
		if e.size > siterules.MaxFileBytes {
			tooBig = append(tooBig, fmt.Sprintf("%s (%s)", e.rel, mb(e.size)))
		}
	}
	if len(tooBig) > 0 {
		add(fmt.Sprintf("files over %s: %s", mb(siterules.MaxFileBytes), preview(tooBig)))
	}
	if total > siterules.MaxTotalBytes {
		add(fmt.Sprintf("site is too large: %s > %s", mb(total), mb(siterules.MaxTotalBytes)))
	}

	if len(problems) > 0 {
		return fmt.Errorf("deploy blocked:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

func hasRoot(entries []fileEntry, rel string) bool {
	for _, e := range entries {
		if e.rel == rel {
			return true
		}
	}
	return false
}

func mb(n int64) string { return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024)) }

func preview(xs []string) string {
	if len(xs) > 10 {
		return strings.Join(xs[:10], ", ") + fmt.Sprintf(" …and %d more", len(xs)-10)
	}
	return strings.Join(xs, ", ")
}
