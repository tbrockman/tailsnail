package tsnode

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"time"
)

// browserTimeout bounds how long we wait on the platform opener. The command
// normally returns immediately; the timeout only guards a wedged helper.
const browserTimeout = 5 * time.Second

// OpenURL asks the desktop to open a URL in the user's browser.
//
// Failure is expected and fine: over SSH, on a headless box, or in a container
// there is no browser to open. The onboarding screen always shows the URL
// itself, so this is a convenience and never a requirement.
func OpenURL(ctx context.Context, raw string) error {
	// Only ever hand a real http(s) URL to a shell-adjacent command.
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("tsnode: %q is not a URL: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("tsnode: refusing to open a %q URL", u.Scheme)
	}

	name, args := browserCommand(u.String())
	if name == "" {
		return fmt.Errorf("tsnode: no browser opener known for %s", runtime.GOOS)
	}
	ctx, cancel := context.WithTimeout(ctx, browserTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tsnode: %s: %w", name, err)
	}
	return nil
}

// browserCommand returns the platform's URL opener. tailsnail targets macOS
// and Linux; anything else reports no opener and falls back to showing the URL.
func browserCommand(u string) (string, []string) {
	switch runtime.GOOS {
	case "darwin":
		return "open", []string{u}
	case "linux":
		return "xdg-open", []string{u}
	default:
		return "", nil
	}
}
