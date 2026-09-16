package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/RedeployAB/loft/internal/release"
)

// checkTimeout caps the discovery fetch, so a slow platform costs at most this much and an
// unreachable one costs nothing visible: the command itself fails on its own terms right after.
const checkTimeout = time.Second

// versionCheck asks the platform where this CLI stands: still accepted by its deploy API, and
// current or behind the release it runs. The platform is the source of truth for both, since it and
// the CLI ship from one tag. The fetch runs in the background while the command does its local
// work, and a missing answer means no verdict; the deploy API refuses an unsupported CLI on its own,
// so nothing is lost when the check cannot speak.
type versionCheck struct {
	done chan struct{}
	cfg  cliConfig // zero when there was no answer
}

// startVersionCheck begins the fetch, unless there is no platform to ask or the user opted out with
// LOFT_NO_UPDATE_CHECK. Call it before the local work so the round trip overlaps with it.
func startVersionCheck(ctx context.Context, base string) *versionCheck {
	vc := &versionCheck{done: make(chan struct{})}
	if base == "" || os.Getenv("LOFT_NO_UPDATE_CHECK") != "" {
		close(vc.done)
		return vc
	}
	go func() {
		defer close(vc.done)
		ctx, cancel := context.WithTimeout(ctx, checkTimeout)
		defer cancel()
		if cfg, err := discoverConfig(ctx, base); err == nil {
			vc.cfg = cfg
		}
	}()
	return vc
}

// result waits for the answer, bounded by checkTimeout.
func (vc *versionCheck) result() cliConfig {
	<-vc.done
	return vc.cfg
}

// errUnsupported is the platform's refusal of this CLI, to act on before uploading anything. A dev
// build is not a release and is never refused here.
func (vc *versionCheck) errUnsupported() error {
	cur, ok := release.Parse(release.Version)
	if !ok {
		return nil
	}
	return vc.result().CLI.Check(cur)
}

// printUpdateNotice writes one line to stderr when the platform runs a newer release than this
// CLI. It is advisory and stays quiet where a notice is noise: CI, captured output, or an unstamped
// build.
func (vc *versionCheck) printUpdateNotice() {
	if os.Getenv("CI") != "" || !isTerminal(os.Stderr) {
		return
	}
	if latest, cur, ok := vc.newerRelease(); ok {
		fmt.Fprintf(os.Stderr, "\nloft %s is available (you have %s): %s\n", latest, cur, release.UpdateHint)
	}
}

// newerRelease reports the platform's release and this CLI's when the platform is ahead.
func (vc *versionCheck) newerRelease() (latest, cur release.Semver, ok bool) {
	cur, ok = release.Parse(release.Version)
	if !ok {
		return latest, cur, false
	}
	latest, ok = release.Parse(vc.result().Version)
	return latest, cur, ok && cur.Less(latest)
}
