package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"

	"github.com/hay-kot/httpkit/errchain"
	"github.com/hay-kot/httpkit/server"
	"github.com/rs/zerolog/log"
	"github.com/sysadminsmedia/homebox/backend/internal/sys/config"
	"github.com/sysadminsmedia/homebox/backend/internal/sys/validate"
)

const (
	// maxSpeechUploadBytes caps a single voice clip upload. Voice captures
	// are short push-to-talk clips; 15 MB fits several minutes of 48 kHz
	// Opus while keeping the proxy from buffering unbounded bodies.
	maxSpeechUploadBytes = 15 << 20

	// speechProviderErrorBodyLimit bounds how much of a provider error
	// response is read for logging.
	speechProviderErrorBodyLimit = 4 << 10
)

// SpeechTranscription is the transcription proxy response.
type SpeechTranscription struct {
	Text string `json:"text"`
}

// HandleSpeechTranscribe godoc
//
//	@Summary	Transcribe an audio clip to text
//	@Tags		Actions
//	@Accept		multipart/form-data
//	@Produce	json
//	@Param		file	formData	file	true	"Audio clip to transcribe"
//	@Success	200		{object}	SpeechTranscription
//	@Failure	422		{object}	validate.ErrorResponse
//	@Router		/v1/actions/transcribe [POST]
//	@Security	Bearer
func (ctrl *V1Controller) HandleSpeechTranscribe(conf config.SpeechConf) errchain.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		r.Body = http.MaxBytesReader(w, r.Body, maxSpeechUploadBytes)

		if err := r.ParseMultipartForm(ctrl.maxParseMemory << 20); err != nil {
			log.Err(err).Msg("failed to parse transcription multipart form")
			return multipartFormError(err)
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			if errors.Is(err, http.ErrMissingFile) {
				return server.JSON(w, http.StatusUnprocessableEntity,
					validate.NewFieldErrors().Append("file", "file is required"))
			}
			return validate.NewRequestError(err, http.StatusInternalServerError)
		}
		defer func() { _ = file.Close() }()

		result, err := transcribeAudio(r.Context(), conf, file, header.Filename, header.Header.Get("Content-Type"))
		if err != nil {
			log.Err(err).Msg("transcription provider request failed")
			return validate.NewRequestError(errors.New("transcription provider request failed"), http.StatusBadGateway)
		}

		return server.JSON(w, http.StatusOK, result)
	}
}

// transcribeAudio forwards an audio clip to the configured OpenAI-compatible
// `/audio/transcriptions` endpoint and returns the transcription. The
// provider API key never leaves the server.
func transcribeAudio(ctx context.Context, conf config.SpeechConf, audio io.Reader, filename, contentType string) (SpeechTranscription, error) {
	endpoint, err := speechEndpointURL(conf.BaseURL)
	if err != nil {
		return SpeechTranscription{}, err
	}

	if filename == "" {
		filename = "audio.webm"
	}

	body := &bytes.Buffer{}
	form := multipart.NewWriter(body)

	if err := form.WriteField("model", conf.Model); err != nil {
		return SpeechTranscription{}, err
	}
	if conf.Language != "" {
		if err := form.WriteField("language", conf.Language); err != nil {
			return SpeechTranscription{}, err
		}
	}

	filePart, err := createSpeechFilePart(form, filename, contentType)
	if err != nil {
		return SpeechTranscription{}, err
	}
	if _, err := io.Copy(filePart, audio); err != nil {
		return SpeechTranscription{}, err
	}
	if err := form.Close(); err != nil {
		return SpeechTranscription{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return SpeechTranscription{}, err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	if conf.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+conf.APIKey)
	}

	client := &http.Client{Timeout: conf.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		return SpeechTranscription{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, speechProviderErrorBodyLimit))
		log.Debug().Int("status", resp.StatusCode).Str("body", string(detail)).Msg("transcription provider returned non-200")
		return SpeechTranscription{}, fmt.Errorf("transcription provider returned status %d", resp.StatusCode)
	}

	var result SpeechTranscription
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return SpeechTranscription{}, fmt.Errorf("decode transcription provider response: %w", err)
	}

	return result, nil
}

// speechEndpointURL validates the configured base URL and appends the
// OpenAI-compatible transcription path.
func speechEndpointURL(baseURL string) (string, error) {
	u, err := url.Parse(strings.TrimSuffix(baseURL, "/"))
	if err != nil {
		return "", fmt.Errorf("invalid speech base_url: %w", err)
	}
	// Plain http stays allowed on purpose: an OpenAI-compatible server on a
	// private network (or a local gateway) commonly has no TLS.
	if u.Scheme != "http" && u.Scheme != schemeHTTPS {
		return "", fmt.Errorf("speech base_url must use http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return "", errors.New("speech base_url is missing a host")
	}
	return u.String() + "/audio/transcriptions", nil
}

// createSpeechFilePart adds the audio file part, preserving the client's
// content type when known — some providers use it to pick a decoder (iOS
// Safari records audio/mp4, most other browsers audio/webm).
func createSpeechFilePart(form *multipart.Writer, filename, contentType string) (io.Writer, error) {
	if contentType == "" {
		return form.CreateFormFile("file", filename)
	}

	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	header.Set("Content-Type", contentType)
	return form.CreatePart(header)
}
