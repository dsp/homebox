package v1

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sysadminsmedia/homebox/backend/internal/sys/config"
)

func speechTestConf(baseURL string) config.SpeechConf {
	return config.SpeechConf{
		BaseURL: baseURL,
		Model:   "voxtral-mini-transcribe",
		APIKey:  "test-key",
		Timeout: 5 * time.Second,
	}
}

func TestTranscribeAudioForwardsRequest(t *testing.T) {
	var gotPath, gotAuth, gotModel, gotLanguage, gotFilename, gotFileContentType, gotFileBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")

		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("provider failed to parse multipart form: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		gotModel = r.FormValue("model")
		gotLanguage = r.FormValue("language")

		file, header, err := r.FormFile("file")
		if err != nil {
			t.Errorf("provider missing file part: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer func() { _ = file.Close() }()
		gotFilename = header.Filename
		gotFileContentType = header.Header.Get("Content-Type")

		buf := new(strings.Builder)
		if _, err := io.Copy(buf, file); err != nil {
			t.Errorf("provider failed to read file: %v", err)
		}
		gotFileBody = buf.String()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(SpeechTranscription{Text: "two spare batteries"})
	}))
	defer srv.Close()

	conf := speechTestConf(srv.URL)
	conf.Language = "en"

	result, err := transcribeAudio(context.Background(), conf, strings.NewReader("opus-bytes"), "clip.webm", "audio/webm")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Text != "two spare batteries" {
		t.Errorf("expected transcription text, got %q", result.Text)
	}
	if gotPath != "/audio/transcriptions" {
		t.Errorf("expected provider path /audio/transcriptions, got %q", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("expected Authorization header to carry API key, got %q", gotAuth)
	}
	if gotModel != "voxtral-mini-transcribe" {
		t.Errorf("expected model form field, got %q", gotModel)
	}
	if gotLanguage != "en" {
		t.Errorf("expected language form field, got %q", gotLanguage)
	}
	if gotFilename != "clip.webm" {
		t.Errorf("expected filename to be forwarded, got %q", gotFilename)
	}
	if gotFileContentType != "audio/webm" {
		t.Errorf("expected file content type to be forwarded, got %q", gotFileContentType)
	}
	if gotFileBody != "opus-bytes" {
		t.Errorf("expected audio bytes to be forwarded, got %q", gotFileBody)
	}
}

func TestTranscribeAudioTrimsTrailingSlashAndOmitsEmptyAuth(t *testing.T) {
	var gotPath, gotAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"ok"}`))
	}))
	defer srv.Close()

	conf := speechTestConf(srv.URL + "/v1/")
	conf.APIKey = ""

	if _, err := transcribeAudio(context.Background(), conf, strings.NewReader("x"), "", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/v1/audio/transcriptions" {
		t.Errorf("expected trailing slash trimmed, got path %q", gotPath)
	}
	if gotAuth != "" {
		t.Errorf("expected no Authorization header without an API key, got %q", gotAuth)
	}
}

func TestTranscribeAudioProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"invalid model"}`, http.StatusUnprocessableEntity)
	}))
	defer srv.Close()

	_, err := transcribeAudio(context.Background(), speechTestConf(srv.URL), strings.NewReader("x"), "clip.webm", "audio/webm")
	if err == nil {
		t.Fatal("expected error for non-200 provider response")
	}
	if !strings.Contains(err.Error(), "422") {
		t.Errorf("expected provider status in error, got %v", err)
	}
}

func TestTranscribeAudioSanitizesFilename(t *testing.T) {
	var gotFilename string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("provider failed to parse multipart form: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Errorf("provider missing file part: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer func() { _ = file.Close() }()
		gotFilename = header.Filename
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"ok"}`))
	}))
	defer srv.Close()

	// An RFC 2231 filename* can smuggle CRLF through percent-decoding; the
	// proxy must strip it before the name reaches the outgoing part header,
	// on the no-content-type (CreateFormFile) branch as well.
	_, err := transcribeAudio(context.Background(), speechTestConf(srv.URL),
		strings.NewReader("x"), "clip\r\nX-Injected: yes.webm", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.ContainsAny(gotFilename, "\r\n") {
		t.Errorf("expected CR/LF stripped from forwarded filename, got %q", gotFilename)
	}
	if gotFilename != "clipX-Injected: yes.webm" {
		t.Errorf("unexpected sanitized filename %q", gotFilename)
	}
}
