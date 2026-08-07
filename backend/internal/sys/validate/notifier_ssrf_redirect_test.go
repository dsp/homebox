package validate_test

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/nicholas-fedor/shoutrrr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/sysadminsmedia/homebox/backend/internal/sys/config"
	"github.com/sysadminsmedia/homebox/backend/internal/sys/validate"
)

// startVictimAndRedirector spins up two loopback servers: a "victim" that records
// whether it was reached, and a "redirector" that 307-redirects any request to the
// victim. It returns the generic+ notifier URL pointing at the redirector and a
// function reporting how many times the victim was hit.
func startVictimAndRedirector(t *testing.T) (notifierURL string, victimHits func() int32) {
	t.Helper()

	var hits int32
	victim := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(victim.Close)

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", victim.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)

	// shoutrrr's generic service wraps a plain http(s) URL as generic+http(s)://...
	return "generic+" + redirector.URL, func() int32 { return atomic.LoadInt32(&hits) }
}

// TestNotifierRedirectSSRF_DefaultClientGuardIsBypassed documents why
// deliveries must not use shoutrrr.Send: shoutrrr constructs its own
// http.Client per send, so even with the redirect guard installed on
// http.DefaultClient the redirect to a blocked (loopback) destination is
// followed and delivered. If this test ever fails because the victim is NOT
// reached, shoutrrr has started honoring http.DefaultClient again and the
// guard strategy can be revisited.
func TestNotifierRedirectSSRF_DefaultClientGuardIsBypassed(t *testing.T) {
	saved := http.DefaultClient.CheckRedirect
	http.DefaultClient.CheckRedirect = validate.NotifierRedirectGuard(&config.NotifierConf{BlockLocalhost: true})
	t.Cleanup(func() { http.DefaultClient.CheckRedirect = saved })

	notifierURL, victimHits := startVictimAndRedirector(t)

	err := shoutrrr.Send(notifierURL, "Test message from Homebox")
	require.NoError(t, err)
	assert.Equal(t, int32(1), victimHits(),
		"shoutrrr.Send uses its own client: the DefaultClient guard does not apply (which is why SendGuardedNotification exists)")
}

// TestNotifierRedirectSSRF_Guarded verifies the actual delivery path: with the
// guarded client injected into the sender, a 307 to a blocked (loopback)
// destination is refused, the send fails, and the victim is never reached.
func TestNotifierRedirectSSRF_Guarded(t *testing.T) {
	notifierURL, victimHits := startVictimAndRedirector(t)

	err := validate.SendGuardedNotification(&config.NotifierConf{BlockLocalhost: true}, notifierURL, "Test message from Homebox")
	require.Error(t, err, "guarded: redirect to loopback must be refused and the send must fail")
	assert.Equal(t, int32(0), victimHits(), "guarded: the blocked redirect target must never be reached")
}

// TestNotifierRedirectSSRF_GuardedAllowsPermittedRedirects verifies the guard
// only blocks policy violations: with no blocking configured, the same
// redirect chain is followed and delivery succeeds.
func TestNotifierRedirectSSRF_GuardedAllowsPermittedRedirects(t *testing.T) {
	notifierURL, victimHits := startVictimAndRedirector(t)

	err := validate.SendGuardedNotification(&config.NotifierConf{}, notifierURL, "Test message from Homebox")
	require.NoError(t, err, "a redirect permitted by policy must still be followed")
	assert.Equal(t, int32(1), victimHits())
}
