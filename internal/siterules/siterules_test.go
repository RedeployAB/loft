package siterules

import "testing"

func TestCheck(t *testing.T) {
	cases := map[string]Kind{
		// static assets
		"index.html":         OK,
		"assets/app.js":      OK,
		"assets/app.js.map":  OK,
		"fonts/a.woff2":      OK,
		"img/logo.SVG":       OK,
		"site.webmanifest":   OK,
		"docs/readme.md":     OK,
		"media/captions.vtt": OK,
		// the .well-known namespace is served by name, extension or not
		".well-known/apple-app-site-association": OK,
		".well-known/security.txt":               OK,
		".well-known/.env":                       Secret, // still not a place for secrets
		// OS folder metadata is skipped, wherever it sits
		".DS_Store":        Skip,
		"assets/.DS_Store": Skip,
		"Thumbs.db":        Skip,
		"img/desktop.ini":  Skip,
		// project checkout markers, top level only
		"node_modules/a/index.js": ProjectFolder,
		"node_modules":            ProjectFolder,
		".git/HEAD":               ProjectFolder,
		// dotenv files anywhere; lookalikes are judged by extension instead
		".env":                   Secret,
		".env.local":             Secret,
		"config/.env.production": Secret,
		"env.js":                 OK,
		"assets/.envelope.png":   OK,
		// not a web asset: wrong or no extension
		"LICENSE":               NotAsset,
		"deploy.sh":             NotAsset,
		"bin/tool.exe":          NotAsset,
		"src/app.ts":            NotAsset,
		"archive.zip":           NotAsset,
		".htaccess":             NotAsset,
		"ds_store.txt":          OK,
		"vendor/node_modules/x": NotAsset, // nested: not a project marker, just an extensionless file
		"assets/.gitkeep":       NotAsset,
	}
	for rel, want := range cases {
		v := Check(rel)
		if v.Kind != want {
			t.Errorf("Check(%q).Kind = %d, want %d", rel, v.Kind, want)
		}
		if refused := want != OK && want != Skip; (v.Reason != "") != refused {
			t.Errorf("Check(%q).Reason = %q, want set=%v", rel, v.Reason, refused)
		}
	}
}
