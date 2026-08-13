package validate

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/nicholas-fedor/shoutrrr"
	"github.com/nicholas-fedor/shoutrrr/pkg/types"
	"github.com/sysadminsmedia/homebox/backend/internal/sys/config"
)

// maxNotifierRedirects caps redirect hops for outbound delivery, matching net/http's
// default so behavior is unchanged for legitimate redirect chains.
const maxNotifierRedirects = 10

// InstallNotifierRedirectGuard hardens http.DefaultClient so redirects are
// re-validated against the notifier SSRF policy on every hop.
//
// This alone is NOT sufficient: current shoutrrr constructs its own
// http.Client per send unless one is injected, so deliveries must go through
// SendGuardedNotification, which injects a guarded client into the sender.
// The default-client guard stays installed as defense in depth for any code
// path that still delivers via http.DefaultClient.
func InstallNotifierRedirectGuard(cfg *config.NotifierConf) {
	http.DefaultClient.CheckRedirect = NotifierRedirectGuard(cfg)
}

// NotifierHTTPClient returns the HTTP client notifier deliveries must use:
// every redirect hop is re-validated against the SSRF policy, so a host that
// passes the initial ValidateNotifierURL gate cannot 30x-redirect to
// localhost / link-local / cloud-metadata / other blocked destinations.
func NotifierHTTPClient(cfg *config.NotifierConf) *http.Client {
	return &http.Client{
		CheckRedirect: NotifierRedirectGuard(cfg),
	}
}

// SendGuardedNotification delivers one notification via shoutrrr with the
// SSRF-guarded client injected into the sender. All notifier deliveries must
// go through this function rather than shoutrrr.Send: the package-level Send
// uses shoutrrr's own per-send client, which follows redirects with no
// policy re-check. Kept in this file so the policy and every enforcement
// point stay together.
func SendGuardedNotification(cfg *config.NotifierConf, rawURL, message string) error {
	sender, err := shoutrrr.CreateSenderWithOptions(types.SenderOptions{
		HTTPClient: NotifierHTTPClient(cfg),
	}, rawURL)
	if err != nil {
		return fmt.Errorf("locating notifier service: %w", err)
	}

	return errors.Join(sender.Send(message, nil)...)
}

// NotifierRedirectGuard returns an http.Client CheckRedirect hook that refuses any
// redirect whose target resolves to an address blocked by cfg, and caps the number
// of hops. Returning a non-nil error aborts the request with that error rather than
// following the redirect, so a blocked hop surfaces as a delivery failure.
func NotifierRedirectGuard(cfg *config.NotifierConf) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxNotifierRedirects {
			return fmt.Errorf("stopped after %d redirects", maxNotifierRedirects)
		}

		// Defensive: with no policy configured, preserve default behavior (only the
		// hop cap above applies) rather than blocking every redirect.
		if cfg == nil {
			return nil
		}

		if err := validateHostAgainstPolicy(req.URL.Hostname(), cfg); err != nil {
			return fmt.Errorf("redirect to %s blocked by notifier SSRF policy: %w", req.URL.Redacted(), err)
		}
		return nil
	}
}
