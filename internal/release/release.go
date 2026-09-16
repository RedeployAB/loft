// Package release holds the version stamped into the loft binaries and the rules that let the CLI
// and loftd compare themselves with each other. Both ship from one tag, so a version is a plain
// X.Y.Z and "the server is newer than the CLI" means an update exists that the platform already
// speaks. The compatibility rule and its wording live here, once, so the CLI's own check and the
// deploy API's enforcement of it cannot drift apart.
package release

import (
	"errors"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// Version is the build version, set at build time with
// -ldflags "-X github.com/RedeployAB/loft/internal/release.Version=<tag>". It is "dev" for an
// unstamped local build, which Parse rejects, so a dev build takes part in no version comparison.
var Version = "dev"

// UpdateHint is how a user gets the current CLI; npm is the only published channel.
const UpdateHint = "npm i -g loft-cli@latest"

// agentPrefix is the User-Agent product token the CLI sends, followed by its version.
const agentPrefix = "loft-cli/"

// UserAgent identifies this build of the CLI to loftd: "loft-cli/v0.1.7 (darwin/arm64)".
func UserAgent() string {
	return agentPrefix + Version + " (" + runtime.GOOS + "/" + runtime.GOARCH + ")"
}

// ParseUserAgent recovers the CLI version from a UserAgent string. It reports false for anything
// else, including a dev build, so a caller can leave those alone.
func ParseUserAgent(ua string) (Semver, bool) {
	if !strings.HasPrefix(ua, agentPrefix) {
		return Semver{}, false
	}
	raw, _, _ := strings.Cut(strings.TrimPrefix(ua, agentPrefix), " ")
	return Parse(raw)
}

// Policy is what a platform requires of a CLI: the oldest release its deploy API accepts, and any
// releases it refuses outright (for one with a defect that a notice is not enough for). It is the
// "cli" object of the discovery document, so the JSON names are part of the wire format.
type Policy struct {
	Min     string   `json:"min"`
	Blocked []string `json:"blocked"`
}

// Check reports why a CLI at cur may not use the platform, or nil if it may. A Min that does not
// parse imposes nothing. The message is complete for a user: it names the versions and the fix.
func (p Policy) Check(cur Semver) error {
	if slices.ContainsFunc(p.Blocked, func(b string) bool { v, ok := Parse(b); return ok && v == cur }) {
		return errors.New("loft CLI " + cur.String() + " is blocked on this platform; update with: " + UpdateHint)
	}
	if minV, ok := Parse(p.Min); ok && cur.Less(minV) {
		return errors.New("loft CLI " + cur.String() + " is too old for this platform (needs " + minV.String() + " or newer); update with: " + UpdateHint)
	}
	return nil
}

// Semver is a parsed X.Y.Z.
type Semver struct {
	Major, Minor, Patch int
}

// Parse reads "X.Y.Z", with an optional leading "v" and an ignored "-prerelease" or "+build"
// suffix. It reports false for anything else, including "dev".
func Parse(s string) (Semver, bool) {
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Semver{}, false
	}
	var n [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || (len(p) > 1 && p[0] == '0') {
			return Semver{}, false
		}
		n[i] = v
	}
	return Semver{n[0], n[1], n[2]}, true
}

// Less reports whether v is older than o.
func (v Semver) Less(o Semver) bool {
	if v.Major != o.Major {
		return v.Major < o.Major
	}
	if v.Minor != o.Minor {
		return v.Minor < o.Minor
	}
	return v.Patch < o.Patch
}

// String renders the version with a leading "v", the form the tags and the CLI use.
func (v Semver) String() string {
	return "v" + strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
}
