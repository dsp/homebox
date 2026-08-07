package config

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sentinel = redactedValue

func Test_AuthConfig_RedactsAPIKeyPepper(t *testing.T) {
	c := AuthConfig{APIKeyPepper: "super-secret-pepper"}

	out, err := json.Marshal(c)
	require.NoError(t, err)

	assert.NotContains(t, string(out), "super-secret-pepper")
	assert.Contains(t, string(out), sentinel)
}

func Test_AuthConfig_EmptyAPIKeyPepperStaysEmpty(t *testing.T) {
	c := AuthConfig{}

	out, err := json.Marshal(c)
	require.NoError(t, err)

	assert.NotContains(t, string(out), sentinel)
}

func Test_OIDCConf_RedactsClientSecret(t *testing.T) {
	c := OIDCConf{ClientID: "public-client-id", ClientSecret: "shh"}

	out, err := json.Marshal(c)
	require.NoError(t, err)

	assert.Contains(t, string(out), "public-client-id")
	assert.NotContains(t, string(out), `"shh"`)
	assert.Contains(t, string(out), sentinel)
}

func Test_MailerConf_RedactsPassword(t *testing.T) {
	c := MailerConf{Host: "smtp.example.com", Username: "u", Password: "pw", From: "f"}

	out, err := json.Marshal(c)
	require.NoError(t, err)

	assert.NotContains(t, string(out), `"pw"`)
	assert.Contains(t, string(out), `"u"`)
	assert.Contains(t, string(out), sentinel)
}

func Test_Database_RedactsPasswordAndPubSubCreds(t *testing.T) {
	c := Database{
		Driver:           "postgres",
		Username:         "homebox",
		Password:         "dbpass",
		Host:             "db",
		Port:             "5432",
		Database:         "homebox",
		PubSubConnString: "postgres://pubuser:pubpass@db:5432/homebox?sslmode=disable",
	}

	out, err := json.Marshal(c)
	require.NoError(t, err)

	s := string(out)
	assert.NotContains(t, s, "dbpass")
	assert.NotContains(t, s, "pubpass")
	assert.Contains(t, s, "pubuser", "username portion should remain visible")
	assert.Contains(t, s, sentinel)
}

func Test_Database_LeavesUncredentialedPubSubAlone(t *testing.T) {
	c := Database{PubSubConnString: "mem://{{ .Topic }}"}

	out, err := json.Marshal(c)
	require.NoError(t, err)

	assert.Contains(t, string(out), "mem://")
}

func Test_Storage_RedactsConnStringUserinfo(t *testing.T) {
	c := Storage{ConnString: "s3://AKIA:secret@bucket.example.com/path"}

	out, err := json.Marshal(c)
	require.NoError(t, err)

	assert.NotContains(t, string(out), "secret@bucket")
	assert.Contains(t, string(out), "REDACTED")
}

func Test_BarcodeAPIConf_RedactsToken(t *testing.T) {
	c := BarcodeAPIConf{TokenBarcodespider: "token-xyz", OpenFoodFactsContact: "contact@example.com"}

	out, err := json.Marshal(c)
	require.NoError(t, err)

	assert.NotContains(t, string(out), "token-xyz")
	assert.Contains(t, string(out), "contact@example.com")
	assert.Contains(t, string(out), sentinel)
}

func Test_SpeechConf_RedactsAPIKey(t *testing.T) {
	c := SpeechConf{BaseURL: "https://api.mistral.ai/v1", Model: "voxtral-mini-transcribe", APIKey: "speech-secret"}

	out, err := json.Marshal(c)
	require.NoError(t, err)

	assert.NotContains(t, string(out), "speech-secret")
	assert.Contains(t, string(out), "voxtral-mini-transcribe")
	assert.Contains(t, string(out), sentinel)
}

const speechTestModel = "whisper-1"

func Test_SpeechConf_RedactsBaseURLUserinfo(t *testing.T) {
	// URL userinfo becomes a Basic auth header on outgoing requests, so it
	// is a credential and must not survive a config dump.
	c := SpeechConf{BaseURL: "https://user:stt-url-secret@stt.lan/v1", Model: speechTestModel}

	out, err := json.Marshal(c)
	require.NoError(t, err)

	assert.NotContains(t, string(out), "stt-url-secret")
	assert.Contains(t, string(out), "stt.lan")
}

func Test_SpeechConf_Enabled(t *testing.T) {
	assert.False(t, SpeechConf{}.Enabled())
	assert.False(t, SpeechConf{BaseURL: "https://api.mistral.ai/v1"}.Enabled())
	assert.False(t, SpeechConf{Model: speechTestModel}.Enabled())
	// An API key is intentionally optional (private-network servers).
	assert.True(t, SpeechConf{BaseURL: "https://api.mistral.ai/v1", Model: speechTestModel}.Enabled())
	// Invalid base URLs count as disabled so /v1/status never advertises a
	// feature every request of which would 502.
	assert.False(t, SpeechConf{BaseURL: "api.mistral.ai/v1", Model: speechTestModel}.Enabled())
	assert.False(t, SpeechConf{BaseURL: "https://x.example/v1?api-version=1", Model: speechTestModel}.Enabled())
}

func Test_SpeechConf_EndpointURL(t *testing.T) {
	for base, want := range map[string]string{
		"https://api.mistral.ai/v1":  "https://api.mistral.ai/v1/audio/transcriptions",
		"https://api.mistral.ai/v1/": "https://api.mistral.ai/v1/audio/transcriptions",
		"http://stt.lan:8000/v1//":   "http://stt.lan:8000/v1/audio/transcriptions",
		"https://api.openai.com":     "https://api.openai.com/audio/transcriptions",
	} {
		got, err := SpeechConf{BaseURL: base}.EndpointURL()
		require.NoError(t, err, base)
		assert.Equal(t, want, got, base)
	}

	for _, base := range []string{
		"ftp://example.com/v1",
		"file:///etc/passwd",
		"/relative/only",
		"api.mistral.ai/v1",
		"https://x.example/openai?api-version=2024-06-01",
		"https://x.example/v1#frag",
		"not a url at all\x7f",
	} {
		_, err := SpeechConf{BaseURL: base}.EndpointURL()
		assert.Error(t, err, base)
	}
}

func Test_OTelConfig_RedactsHeaders(t *testing.T) {
	c := OTelConfig{Headers: "Authorization=Bearer hunter2,X-Other=val"}

	out, err := json.Marshal(c)
	require.NoError(t, err)

	assert.NotContains(t, string(out), "hunter2")
	assert.Contains(t, string(out), sentinel)
}

func Test_Config_FullMarshalRedactsAllSecrets(t *testing.T) {
	c := &Config{
		Auth:    AuthConfig{APIKeyPepper: "pepper-secret"},
		OIDC:    OIDCConf{ClientSecret: "oidc-secret"},
		Mailer:  MailerConf{Password: "mailer-secret"},
		Storage: Storage{ConnString: "s3://k:s3secret@b/p"},
		Database: Database{
			Password:         "db-secret",
			PubSubConnString: "postgres://u:pubsecret@h/d",
		},
		Barcode: BarcodeAPIConf{TokenBarcodespider: "bs-secret"},
		Otel:    OTelConfig{Headers: "Authorization=Bearer otel-secret"},
		Speech:  SpeechConf{APIKey: "stt-secret"},
	}

	out, err := json.MarshalIndent(c, "", "  ")
	require.NoError(t, err)

	for _, secret := range []string{
		"pepper-secret",
		"oidc-secret",
		"mailer-secret",
		"s3secret",
		"db-secret",
		"pubsecret",
		"bs-secret",
		"otel-secret",
		"stt-secret",
	} {
		assert.NotContainsf(t, string(out), secret, "expected %q to be redacted in output", secret)
	}
}
