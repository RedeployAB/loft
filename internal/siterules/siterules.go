// Package siterules decides, file by file, what a deployable site may contain. Loft serves a site
// as plain static files behind auth, so a deploy is limited to real web assets with an entry
// point, kept within size limits, and stripped of the things that ride along when someone deploys
// a project root instead of its build output (node_modules, a .git tree, a leaked .env).
//
// This is the one Go definition of those rules. The CLI applies Check to a local folder before
// uploading, for a fast message that lists every problem at once, and the deploy API applies it
// again to each part as it streams in, as the gate every client passes through. The rule order
// and the per-file wording live here so the two cannot drift. The browser console repeats only the
// entry-point check, deliberately, to save an upload that would be refused.
package siterules

import (
	"path"
	"strings"
)

// Size limits, shared by the CLI's local check and the API's streaming check.
const (
	MaxFileBytes  = 25 * 1024 * 1024  // per file
	MaxTotalBytes = 100 * 1024 * 1024 // per site
	MaxFiles      = 2000              // per site
)

// IndexFile is the entry point nginx serves the site root from. Without it every visit to "/"
// gets a bare 403, so a site is refused rather than published in that state. It is a whole-site
// rule, checked by the callers once all files are known; Check judges single files.
const IndexFile = "index.html"

// Kind is Check's decision about one file.
type Kind int

const (
	// OK means the file is a static web asset and deploys as-is.
	OK Kind = iota
	// Skip means OS folder metadata (.DS_Store and friends): dropped silently, not refused. Refusing
	// a whole deploy over a Finder artifact helps nobody.
	Skip
	// ProjectFolder means the file lies under a directory that marks a project checkout, not a
	// built site.
	ProjectFolder
	// Secret means a dotenv file (.env, .env.local, ...) anywhere in the tree.
	Secret
	// NotAsset means the extension is not one a static site serves. A file with no extension is
	// never an asset.
	NotAsset
)

// Verdict is what Check decided about one file. Reason is set for a refused file and names it, so
// a caller that stops at the first problem can show it as-is.
type Verdict struct {
	Kind   Kind
	Reason string
}

var (
	blockedDirs = []string{"node_modules", ".git"}
	junkFiles   = map[string]bool{".DS_Store": true, "Thumbs.db": true, "desktop.ini": true}
	allowedExt  = map[string]bool{
		"html": true, "htm": true, "css": true, "js": true, "mjs": true, "cjs": true, "json": true,
		"map": true, "wasm": true, "webmanifest": true,
		"svg": true, "png": true, "jpg": true, "jpeg": true, "gif": true, "webp": true, "avif": true,
		"ico": true, "bmp": true,
		"woff": true, "woff2": true, "ttf": true, "otf": true, "eot": true,
		"txt": true, "xml": true, "csv": true, "pdf": true, "md": true,
		"mp4": true, "webm": true, "ogg": true, "mp3": true, "wav": true, "m4a": true, "vtt": true,
	}
)

// Check judges one file by its forward-slash path within the site. Junk is recognized before the
// extension rule on purpose: .DS_Store has no extension and would otherwise be refused.
func Check(rel string) Verdict {
	base := path.Base(rel)
	if junkFiles[base] {
		return Verdict{Kind: Skip}
	}
	if d := BlockedDir(rel); d != "" {
		return Verdict{ProjectFolder, "found " + d + "/ (deploy a built site, not a project folder)"}
	}
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return Verdict{Secret, "refusing to upload secret file " + rel}
	}
	// RFC 8615 files (apple-app-site-association, assetlinks.json, security.txt) are served by
	// name and often have no extension, so the namespace is trusted as a whole.
	if strings.HasPrefix(rel, ".well-known/") {
		return Verdict{Kind: OK}
	}
	if !allowedExt[ext(base)] {
		return Verdict{NotAsset, "disallowed file type (static web assets only): " + rel}
	}
	return Verdict{Kind: OK}
}

// BlockedDir returns the top-level project directory rel lies under, or "" if none. Only the top
// level counts: a vendored node_modules deeper in a built site is the site's own business.
func BlockedDir(rel string) string {
	for _, d := range blockedDirs {
		if rel == d || strings.HasPrefix(rel, d+"/") {
			return d
		}
	}
	return ""
}

// ext is the lowercased extension of a basename, without the dot. A dotfile such as ".env" or a
// bare name such as "LICENSE" has none, which is why path.Ext (".env") is not used.
func ext(base string) string {
	if dot := strings.LastIndex(base, "."); dot > 0 {
		return strings.ToLower(base[dot+1:])
	}
	return ""
}
