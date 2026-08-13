package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/hay-kot/httpkit/errchain"
	"github.com/rs/zerolog/log"
	"github.com/sysadminsmedia/homebox/backend/internal/core/services"
	"github.com/sysadminsmedia/homebox/backend/internal/data/repo"
	"github.com/sysadminsmedia/homebox/backend/internal/sys/config"
	"github.com/sysadminsmedia/homebox/backend/internal/sys/validate"
	"github.com/sysadminsmedia/homebox/backend/internal/web/adapters"
)

const (
	// maxTranscriptLength bounds the text handed to the intent model. Voice
	// commands are a sentence or two; anything longer is dictation, not a
	// command, and would only inflate the provider bill.
	maxTranscriptLength = 1000

	// maxLocationCandidates bounds how many alternatives an ambiguous
	// location resolution reports back for the user to choose between.
	maxLocationCandidates = 8

	// locationPathSeparator matches the breadcrumb style of the web UI.
	locationPathSeparator = " › "
)

// VoiceCommandAction names the intent parsed from a spoken command. Only
// create is actionable today; search is recognised so the UI can route the
// user to search instead of silently drafting an item.
const (
	VoiceActionCreate  = "create_item"
	VoiceActionSearch  = "search_items"
	VoiceActionUnknown = "unknown"
)

type (
	// VoiceCommandRequest carries the transcript to interpret.
	VoiceCommandRequest struct {
		Transcript string `json:"transcript" validate:"required,max=1000"`
	}

	// VoiceLocationMatch is a resolved (or candidate) location.
	VoiceLocationMatch struct {
		ID   uuid.UUID `json:"id"`
		Name string    `json:"name"`
		Path string    `json:"path"`
	}

	// VoiceCommandResult is a *proposal*, never a committed change. The
	// client prefills its create form with this and the user confirms —
	// same trust model as the barcode product import.
	VoiceCommandResult struct {
		Action      string  `json:"action"`
		Name        string  `json:"name,omitempty"`
		Quantity    float64 `json:"quantity,omitempty"`
		Description string  `json:"description,omitempty"`
		Query       string  `json:"query,omitempty"`

		// Location is set when the spoken location resolved to exactly one
		// place. Otherwise LocationCandidates lists what it could have been
		// so the UI can ask, and LocationQuery echoes what was heard.
		Location           *VoiceLocationMatch  `json:"location,omitempty"`
		LocationCandidates []VoiceLocationMatch `json:"locationCandidates,omitempty"`
		LocationQuery      string               `json:"locationQuery,omitempty"`

		Transcript string `json:"transcript"`
	}
)

// HandleVoiceCommand godoc
//
//	@Summary	Interpret a spoken command into a proposed action
//	@Tags		Actions
//	@Accept		json
//	@Produce	json
//	@Param		payload	body		VoiceCommandRequest	true	"Transcript"
//	@Success	200		{object}	VoiceCommandResult
//	@Failure	422		{object}	validate.ErrorResponse
//	@Router		/v1/actions/voice-command [POST]
//	@Security	Bearer
func (ctrl *V1Controller) HandleVoiceCommand(conf config.SpeechConf) errchain.HandlerFunc {
	fn := func(r *http.Request, body VoiceCommandRequest) (VoiceCommandResult, error) {
		transcript := strings.TrimSpace(body.Transcript)
		if transcript == "" {
			return VoiceCommandResult{}, validate.NewFieldErrors(
				validate.FieldError{Field: "transcript", Error: "transcript is required"})
		}
		if len(transcript) > maxTranscriptLength {
			transcript = transcript[:maxTranscriptLength]
		}

		auth := services.NewContext(r.Context())

		call, err := parseVoiceIntent(r.Context(), conf, transcript)
		if err != nil {
			if r.Context().Err() != nil {
				return VoiceCommandResult{}, nil
			}
			if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
				log.Warn().Err(err).Msg("voice command intent model timed out")
				return VoiceCommandResult{}, validate.NewRequestError(
					errors.New("intent model timed out"), http.StatusGatewayTimeout)
			}
			log.Err(err).Msg("voice command intent request failed")
			return VoiceCommandResult{}, validate.NewRequestError(
				errors.New("intent model request failed"), http.StatusBadGateway)
		}

		result := VoiceCommandResult{Action: VoiceActionUnknown, Transcript: transcript}

		switch call.Name {
		case VoiceActionSearch:
			result.Action = VoiceActionSearch
			result.Query = call.Args.Query
			if result.Query == "" {
				result.Query = transcript
			}
		case VoiceActionCreate:
			result.Action = VoiceActionCreate
			result.Name = call.Args.Name
			result.Description = call.Args.Description
			result.Quantity = call.Args.Quantity
			if result.Quantity <= 0 {
				result.Quantity = 1
			}
			if result.Name == "" {
				// A create with no name is not usable as a draft; treat it
				// as unrecognised rather than opening an empty form.
				result.Action = VoiceActionUnknown
				break
			}
			if loc := strings.TrimSpace(call.Args.Location); loc != "" {
				result.LocationQuery = loc
				matches, lerr := ctrl.resolveVoiceLocation(auth, auth.GID, loc)
				if lerr != nil {
					return VoiceCommandResult{}, lerr
				}
				if len(matches) == 1 {
					result.Location = &matches[0]
				} else {
					result.LocationCandidates = matches
				}
			}
		}

		return result, nil
	}

	return adapters.Action(fn, http.StatusOK)
}

// resolveVoiceLocation fuzzily matches a spoken location against the group's
// location tree. Exact name/path matches win; otherwise substring matches are
// returned as candidates for the user to disambiguate. Resolution is
// server-side on purpose: the model never sees location IDs and cannot
// invent one.
func (ctrl *V1Controller) resolveVoiceLocation(ctx context.Context, gid uuid.UUID, query string) ([]VoiceLocationMatch, error) {
	tree, err := ctrl.repo.Entities.Tree(ctx, gid, repo.TreeQuery{})
	if err != nil {
		return nil, err
	}

	flat := flattenLocationTree(tree, "")
	needle := strings.ToLower(strings.TrimSpace(query))

	var exact, fuzzy []VoiceLocationMatch
	for _, loc := range flat {
		name, path := strings.ToLower(loc.Name), strings.ToLower(loc.Path)
		switch {
		case name == needle || path == needle:
			exact = append(exact, loc)
		case strings.Contains(name, needle) || strings.Contains(path, needle):
			fuzzy = append(fuzzy, loc)
		}
	}

	matches := exact
	if len(matches) == 0 {
		matches = fuzzy
	}
	if len(matches) > maxLocationCandidates {
		matches = matches[:maxLocationCandidates]
	}
	return matches, nil
}

// flattenLocationTree walks the tree into breadcrumb paths, skipping items —
// only locations can be a parent for a voice-created entry.
func flattenLocationTree(nodes []repo.TreeItem, prefix string) []VoiceLocationMatch {
	var out []VoiceLocationMatch
	for i := range nodes {
		node := nodes[i]
		if node.Type != "location" {
			continue
		}
		path := node.Name
		if prefix != "" {
			path = prefix + locationPathSeparator + node.Name
		}
		out = append(out, VoiceLocationMatch{ID: node.ID, Name: node.Name, Path: path})

		children := make([]repo.TreeItem, 0, len(node.Children))
		for _, child := range node.Children {
			if child != nil {
				children = append(children, *child)
			}
		}
		out = append(out, flattenLocationTree(children, path)...)
	}
	return out
}

// --- intent model plumbing ---------------------------------------------------

// voiceToolCall is the parsed function call returned by the intent model.
type voiceToolCall struct {
	Name string
	Args struct {
		Name        string  `json:"name"`
		Quantity    float64 `json:"quantity"`
		Location    string  `json:"location"`
		Description string  `json:"description"`
		Query       string  `json:"query"`
	}
}

const voiceSystemPrompt = `You turn a spoken sentence about a home inventory into exactly one tool call.
Use create_item when the user wants to add or record something they own.
Use search_items when the user is asking where something is or wants to find it.
Extract only what was actually said: never invent a location, and leave fields empty when unsure.
Pass the location exactly as spoken (e.g. "garage shelf") — do not guess IDs.`

// parseVoiceIntent asks the configured chat model to map the transcript onto
// one of two tools. The provider API key stays server-side.
func parseVoiceIntent(ctx context.Context, conf config.SpeechConf, transcript string) (voiceToolCall, error) {
	endpoint, err := conf.ChatEndpointURL()
	if err != nil {
		return voiceToolCall{}, err
	}

	payload := map[string]any{
		"model":       conf.IntentModel,
		"temperature": 0,
		"messages": []map[string]string{
			{"role": "system", "content": voiceSystemPrompt},
			{"role": "user", "content": transcript},
		},
		"tool_choice": "any",
		"tools":       voiceTools(),
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return voiceToolCall{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return voiceToolCall{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if conf.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+conf.APIKey)
	}

	client := &http.Client{Timeout: conf.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		return voiceToolCall{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, speechProviderErrorBodyLimit))
		log.Debug().Int("status", resp.StatusCode).Str("body", string(detail)).Msg("intent model returned non-200")
		return voiceToolCall{}, fmt.Errorf("intent model returned status %d", resp.StatusCode)
	}

	var decoded struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&decoded); err != nil {
		return voiceToolCall{}, fmt.Errorf("decode intent model response: %w", err)
	}

	if len(decoded.Choices) == 0 || len(decoded.Choices[0].Message.ToolCalls) == 0 {
		// No tool call means the model could not map the sentence onto an
		// action. That is a normal outcome, not an error.
		return voiceToolCall{Name: VoiceActionUnknown}, nil
	}

	fn := decoded.Choices[0].Message.ToolCalls[0].Function
	call := voiceToolCall{Name: fn.Name}
	if fn.Arguments != "" {
		if err := json.Unmarshal([]byte(fn.Arguments), &call.Args); err != nil {
			log.Debug().Err(err).Str("arguments", fn.Arguments).Msg("intent model returned unparsable arguments")
			return voiceToolCall{Name: VoiceActionUnknown}, nil
		}
	}
	return call, nil
}

// voiceTools mirrors the MCP server's action vocabulary so spoken commands
// behave the same in the app and through an assistant.
func voiceTools() []map[string]any {
	return []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        VoiceActionCreate,
				"description": "Add an item to the inventory. Example: 'add two spare AA batteries to the garage shelf'.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name":        map[string]any{"type": "string", "description": "Name of the item"},
						"quantity":    map[string]any{"type": "number", "description": "How many, defaults to 1"},
						"location":    map[string]any{"type": "string", "description": "Location name exactly as spoken"},
						"description": map[string]any{"type": "string", "description": "Any extra detail mentioned"},
					},
					"required": []string{"name"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        VoiceActionSearch,
				"description": "Find items already in the inventory. Example: 'where is my passport'.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query": map[string]any{"type": "string", "description": "What to search for"},
					},
					"required": []string{"query"},
				},
			},
		},
	}
}
