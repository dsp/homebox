package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/sysadminsmedia/homebox/backend/internal/data/repo"
	"github.com/sysadminsmedia/homebox/backend/internal/sys/config"
)

// intentServer stands in for the provider's chat-completions endpoint,
// returning a canned tool call and capturing the request it received.
func intentServer(t *testing.T, toolName, arguments string) (*httptest.Server, func() map[string]any) {
	t.Helper()

	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("expected /chat/completions, got %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("failed to decode intent request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if toolName == "" {
			_, _ = fmt.Fprint(w, `{"choices":[{"message":{"tool_calls":[]}}]}`)
			return
		}
		// arguments arrives from real providers as a JSON *string* holding
		// JSON, so marshal it as one rather than inlining it.
		encodedArgs, _ := json.Marshal(arguments)
		_, _ = fmt.Fprintf(w,
			`{"choices":[{"message":{"tool_calls":[{"function":{"name":%q,"arguments":%s}}]}}]}`,
			toolName, encodedArgs)
	}))
	t.Cleanup(srv.Close)

	return srv, func() map[string]any { return captured }
}

func intentConf(baseURL string) config.SpeechConf {
	return config.SpeechConf{
		BaseURL:     baseURL,
		Model:       "voxtral-mini-transcribe",
		IntentModel: "mistral-small-latest",
		APIKey:      "test-key",
		Timeout:     5 * time.Second,
	}
}

func TestParseVoiceIntentCreateItem(t *testing.T) {
	srv, captured := intentServer(t, VoiceActionCreate,
		`{"name":"AA batteries","quantity":2,"location":"garage shelf"}`)

	call, err := parseVoiceIntent(context.Background(), intentConf(srv.URL),
		"add two AA batteries to the garage shelf")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if call.Name != VoiceActionCreate {
		t.Errorf("expected create tool call, got %q", call.Name)
	}
	if call.Args.Name != "AA batteries" {
		t.Errorf("unexpected name %q", call.Args.Name)
	}
	if call.Args.Quantity != 2 {
		t.Errorf("unexpected quantity %v", call.Args.Quantity)
	}
	if call.Args.Location != "garage shelf" {
		t.Errorf("unexpected location %q", call.Args.Location)
	}

	req := captured()
	if req["model"] != "mistral-small-latest" {
		t.Errorf("expected the intent model to be requested, got %v", req["model"])
	}
	// Both tools must be offered so the model can pick search over create.
	tools, ok := req["tools"].([]any)
	if !ok || len(tools) != 2 {
		t.Errorf("expected 2 tools offered, got %v", req["tools"])
	}
}

func TestParseVoiceIntentNoToolCallIsUnknown(t *testing.T) {
	srv, _ := intentServer(t, "", "")

	call, err := parseVoiceIntent(context.Background(), intentConf(srv.URL), "hello there")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if call.Name != VoiceActionUnknown {
		t.Errorf("expected unknown when the model makes no tool call, got %q", call.Name)
	}
}

func TestParseVoiceIntentUnparsableArgumentsIsUnknown(t *testing.T) {
	srv, _ := intentServer(t, VoiceActionCreate, `{"name": BROKEN`)

	call, err := parseVoiceIntent(context.Background(), intentConf(srv.URL), "add a thing")
	if err != nil {
		t.Fatalf("expected malformed arguments to degrade, not error: %v", err)
	}
	if call.Name != VoiceActionUnknown {
		t.Errorf("expected unknown for unparsable arguments, got %q", call.Name)
	}
}

func TestParseVoiceIntentProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"bad model"}`, http.StatusUnprocessableEntity)
	}))
	defer srv.Close()

	if _, err := parseVoiceIntent(context.Background(), intentConf(srv.URL), "add a thing"); err == nil {
		t.Fatal("expected an error for a non-200 provider response")
	}
}

func TestFlattenLocationTreeSkipsItemsAndBuildsPaths(t *testing.T) {
	shelf := repo.TreeItem{ID: uuid.New(), Name: "Shelf B", Type: "location"}
	drill := repo.TreeItem{ID: uuid.New(), Name: "Drill", Type: "item"}
	garage := repo.TreeItem{
		ID: uuid.New(), Name: "Garage", Type: "location",
		Children: []*repo.TreeItem{&shelf, &drill},
	}

	flat := flattenLocationTree([]repo.TreeItem{garage}, "")
	if len(flat) != 2 {
		t.Fatalf("expected 2 locations (items skipped), got %d", len(flat))
	}
	if flat[0].Path != "Garage" {
		t.Errorf("unexpected root path %q", flat[0].Path)
	}
	if flat[1].Path != "Garage › Shelf B" {
		t.Errorf("expected breadcrumb path, got %q", flat[1].Path)
	}
}

func TestSpeechConfIntentEndpoints(t *testing.T) {
	conf := intentConf("https://api.mistral.ai/v1")

	chat, err := conf.ChatEndpointURL()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chat != "https://api.mistral.ai/v1/chat/completions" {
		t.Errorf("unexpected chat endpoint %q", chat)
	}

	// The transcription endpoint must be unaffected by the shared helper.
	audio, err := conf.EndpointURL()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if audio != "https://api.mistral.ai/v1/audio/transcriptions" {
		t.Errorf("unexpected audio endpoint %q", audio)
	}

	// Voice commands stay off until an intent model is named.
	noIntent := conf
	noIntent.IntentModel = ""
	if noIntent.IntentEnabled() {
		t.Error("intent must be disabled without an intent model")
	}
	if !conf.IntentEnabled() {
		t.Error("intent must be enabled when speech works and a model is set")
	}
}
