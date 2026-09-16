package deploy

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RedeployAB/loft/internal/config"
)

// buildForm writes a deploy multipart body: the site field first (the server requires it before the
// files), then each path->content as a "files" part whose multipart filename carries the full path.
func buildForm(t *testing.T, site string, files map[string]string) *multipart.Reader {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("site", site); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		fw, err := mw.CreateFormFile("files", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return multipart.NewReader(&buf, mw.Boundary())
}

// TestStagePreservesNestedPaths is the regression for the RFC-7578 flattening bug: a build with
// subdirectories must stage with its directory structure intact. part.FileName() applies
// filepath.Base, which would land every file in the site root and break every reference under
// /assets/*.
func TestStagePreservesNestedPaths(t *testing.T) {
	staging := t.TempDir()
	files := map[string]string{
		"index.html":                "<h1>hi</h1>",
		"assets/index-abc.js":       "console.log(1)",
		"assets/modules/vue-xyz.js": "/* vue */",
	}
	mr := buildForm(t, "demo", files)

	out, uerr := stage(mr, staging)
	if uerr != nil {
		t.Fatalf("stage failed: %+v", uerr)
	}
	if out.site != "demo" {
		t.Fatalf("site = %q, want demo", out.site)
	}
	if out.files != len(files) {
		t.Fatalf("files = %d, want %d", out.files, len(files))
	}

	for rel, want := range files {
		got, err := os.ReadFile(filepath.Join(staging, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("expected staged file %q: %v", rel, err)
		}
		if string(got) != want {
			t.Fatalf("staged %q = %q, want %q", rel, got, want)
		}
	}
}

// TestStageRejectsTraversal checks that path traversal in a part filename is neutralized: the
// escaping segments are dropped and the file stays inside the staging dir. (cleanRelPath strips
// ".."; within() is the backstop.)
func TestStageRejectsTraversal(t *testing.T) {
	staging := t.TempDir()
	mr := buildForm(t, "demo", map[string]string{
		"index.html":      "<h1>hi</h1>",
		"../../escape.js": "pwned",
	})

	if _, uerr := stage(mr, staging); uerr != nil {
		t.Fatalf("stage failed: %+v", uerr)
	}
	// Nothing may be written outside the staging dir.
	if _, err := os.Stat(filepath.Join(filepath.Dir(staging), "escape.js")); !os.IsNotExist(err) {
		t.Fatalf("traversal escaped staging: %v", err)
	}
	// The sanitized basename stays inside staging.
	if _, err := os.Stat(filepath.Join(staging, "escape.js")); err != nil {
		t.Fatalf("expected sanitized file inside staging: %v", err)
	}
}

// TestStageRequiresRootIndex checks that a deploy with no index.html at the site root is refused.
// nginx only serves the root from index.html, so such a site answers "/" with a bare 403; a nested
// or differently named entry point does not count.
func TestStageRequiresRootIndex(t *testing.T) {
	cases := map[string]map[string]string{
		"single page under another name": {"report.html": "<h1>hi</h1>"},
		"index nested one level down":    {"dist/index.html": "<h1>hi</h1>", "dist/app.js": "1"},
		"assets only":                    {"assets/app.js": "1", "assets/app.css": "2"},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			_, uerr := stage(buildForm(t, "report", files), t.TempDir())
			if uerr == nil {
				t.Fatal("expected stage to refuse a site without a root index.html")
			}
			if uerr.status != http.StatusBadRequest || !strings.Contains(uerr.msg, "index.html") {
				t.Fatalf("got %d %q, want 400 naming index.html", uerr.status, uerr.msg)
			}
		})
	}

	if _, uerr := stage(buildForm(t, "report", map[string]string{"index.html": "<h1>hi</h1>"}), t.TempDir()); uerr != nil {
		t.Fatalf("a root index.html alone should deploy: %+v", uerr)
	}
}

// TestStageRefusesNonSiteFiles checks that the API applies the shared content rules itself and does
// not rely on the CLI having done so: a project folder marker, a dotenv file, or anything that is
// not a static web asset ends the deploy with a 400 that names the offender, and nothing from that
// part is left in staging. OS folder metadata is dropped without failing the deploy.
func TestStageRefusesNonSiteFiles(t *testing.T) {
	cases := map[string]struct {
		rel    string
		want   string // substring of the 400 message, or "" when the deploy succeeds
		staged bool   // whether rel ends up in staging on success
	}{
		"node_modules":    {"node_modules/left-pad/index.js", "node_modules/", false},
		"git tree":        {".git/config", ".git/", false},
		"dotenv":          {".env", "secret file .env", false},
		"nested dotenv":   {"config/.env.production", "secret file config/.env.production", false},
		"shell script":    {"deploy.sh", "disallowed file type", false},
		"binary":          {"bin/tool.exe", "disallowed file type", false},
		"no extension":    {"LICENSE", "disallowed file type", false},
		"source map":      {"assets/app.js.map", "", true},
		"finder metadata": {".DS_Store", "", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			staging := t.TempDir()
			files := map[string]string{"index.html": "<h1>hi</h1>", tc.rel: "x"}
			_, uerr := stage(buildForm(t, "site", files), staging)
			if tc.want != "" {
				if uerr == nil {
					t.Fatalf("expected stage to refuse %q", tc.rel)
				}
				if uerr.status != http.StatusBadRequest || !strings.Contains(uerr.msg, tc.want) {
					t.Fatalf("got %d %q, want 400 containing %q", uerr.status, uerr.msg, tc.want)
				}
			} else if uerr != nil {
				t.Fatalf("expected %q to deploy: %+v", tc.rel, uerr)
			}
			_, err := os.Stat(filepath.Join(staging, filepath.FromSlash(tc.rel)))
			if present := err == nil; present != tc.staged {
				t.Fatalf("%q in staging = %v, want %v", tc.rel, present, tc.staged)
			}
		})
	}
}

// TestPolicy checks the wiring, since the rule itself is tested in release: the service advertises
// the minimum it enforces, and blocked versions from config come through as a JSON array even when
// none are set.
func TestPolicy(t *testing.T) {
	p := New(config.Config{}).Policy()
	if p.Min != minCLIVersion || p.Blocked == nil || len(p.Blocked) != 0 {
		t.Fatalf("Policy() = %+v, want min %s and an empty, non-nil blocked list", p, minCLIVersion)
	}
	p = New(config.Config{CLIBlockedVersions: []string{"v0.1.4"}}).Policy()
	if len(p.Blocked) != 1 || p.Blocked[0] != "v0.1.4" {
		t.Fatalf("Policy().Blocked = %v, want [v0.1.4]", p.Blocked)
	}
}

// writeTree materializes rel->content files (creating parent dirs) under root, for building a live
// site or a staging tree in mirror tests.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestMirrorReconcilesAcrossRedeploys checks that re-deploying over a live site reconciles the tree
// when a path changes shape: a file becomes a directory (and vice versa) must not fail the deploy,
// and a directory a later deploy drops must be pruned rather than linger.
func TestMirrorReconcilesAcrossRedeploys(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "site")
	// The live site from a previous deploy.
	writeTree(t, dest, map[string]string{
		"docs":          "v1 was a file", // becomes a directory in v2
		"assets/app.js": "v1",            // updated in v2
		"old/legacy.js": "stale",         // whole folder dropped in v2
	})

	// The new deploy, already staged.
	staging := filepath.Join(root, "staging")
	writeTree(t, staging, map[string]string{
		"docs/index.html":     "v2 is a dir now",
		"assets/app.js":       "v2",
		"assets/img/logo.svg": "<svg/>",
	})

	if err := mirror(staging, dest); err != nil {
		t.Fatalf("mirror failed: %v", err)
	}

	// file -> directory flip resolved.
	if fi, err := os.Stat(filepath.Join(dest, "docs")); err != nil || !fi.IsDir() {
		t.Fatalf("docs should be a directory now: fi=%v err=%v", fi, err)
	}
	assertFileContent(t, dest, "docs/index.html", "v2 is a dir now")
	// updated file and new nested file present.
	assertFileContent(t, dest, "assets/app.js", "v2")
	assertFileContent(t, dest, "assets/img/logo.svg", "<svg/>")
	// dropped folder pruned entirely.
	if _, err := os.Stat(filepath.Join(dest, "old")); !os.IsNotExist(err) {
		t.Fatalf("dropped directory old/ should be pruned, got err=%v", err)
	}
}

// TestMirrorFlipsDirectoryToFile is the reverse of the flip above: a directory in the live site
// becomes a regular file in the new deploy.
func TestMirrorFlipsDirectoryToFile(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "site")
	writeTree(t, dest, map[string]string{
		"index.html":   "home",
		"blog/post.md": "a directory in v1",
	})
	staging := filepath.Join(root, "staging")
	writeTree(t, staging, map[string]string{
		"index.html": "home",
		"blog":       "now a plain file",
	})

	if err := mirror(staging, dest); err != nil {
		t.Fatalf("mirror failed: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(dest, "blog")); err != nil || fi.IsDir() {
		t.Fatalf("blog should be a regular file now: fi=%v err=%v", fi, err)
	}
	assertFileContent(t, dest, "blog", "now a plain file")
}

func assertFileContent(t *testing.T, root, rel, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("expected file %q: %v", rel, err)
	}
	if string(got) != want {
		t.Fatalf("file %q = %q, want %q", rel, got, want)
	}
}
