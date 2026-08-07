# Voice input for HomeBox — research & design notes

*Research date: 2026-08-07. Covers two approaches: (A) a native voice input
mode built on a top-end speech model (Voxtral or Whisper), and (B) an MCP
server so HomeBox can be driven from any voice-capable MCP client.*

---

## Implementation status (updated 2026-08-07)

Both work streams from this document have first implementations up as PRs:

| PR | Branch | What shipped |
|---|---|---|
| [#1](https://github.com/dsp/homebox/pull/1) | `claude/voice-01-backend-transcribe` | `HBOX_SPEECH_*` config + `POST /api/v1/actions/transcribe` proxy (OpenAI-compatible providers: Mistral Voxtral, OpenAI, Groq), rate-limited, `speechToText` in `/v1/status` |
| [#2](https://github.com/dsp/homebox/pull/2) | `claude/voice-02-frontend-dictation` (stacked on #1) | Push-to-talk dictation: mic button drafts the create form (name → description) and fills item search |
| [#3](https://github.com/dsp/homebox/pull/3) | `claude/mcp-01-server` | `mcp/` — **standalone** MCP server module (see divergence note below), stateless 2026-07-28 streamable HTTP, HomeBox API-key auth, 7 tools |

Two decisions superseded this document's original recommendations:

1. **Hosted STT API only for v1** — no self-hosted model (§2.1 already
   updated). The pluggable interface keeps local servers a config-only
   future option.
2. **The MCP server shipped standalone, not embedded.** §3.2 below
   recommends embedding in the HomeBox binary; the built version is instead
   a self-contained module under `mcp/` that talks to the REST API and
   forwards the caller's `hbox_` API key per request. That trades in-process
   repo access for zero coupling: no new dependency in the main binary, no
   upstream buy-in needed, works against any running instance. Embedding
   remains an option later if upstream wants it in-tree.

See also the new §3.7 on where the agent loop lives in each approach.

---

## TL;DR / Recommendation

Do **both, in this order**:

1. **MCP server first.** It is dramatically less code, requires zero audio
   handling, reuses the API-key auth this fork already has
   (`backend/app/api/handlers/v1/v1_ctrl_api_keys.go`, `middleware.go`), and is
   immediately usable from Claude voice mode, ChatGPT voice, Voice Mode for
   Claude Desktop/Code, and Home Assistant Assist. The voice+NLU problem —
   the genuinely hard part — is entirely the client's job. The official Go MCP
   SDK is stable (v1.x, streamable HTTP transport), so it can be embedded
   directly in the existing Go binary behind a config flag.

2. **Then a native "voice quick-capture" mode** in the web UI, using a
   **hosted STT API** (decision: no self-hosted model for now), integrated
   as a *pluggable OpenAI-compatible provider* rather than hard-wiring one
   vendor. The same config block covers Mistral's hosted Voxtral (cheapest,
   $0.003/min — the default recommendation), OpenAI
   (`gpt-4o-transcribe`/`whisper-1`), and Groq (hosted Whisper turbo).
   Because every provider speaks the same interface, a local STT container
   remains a zero-code future option for self-hosters, but is explicitly
   out of scope for v1.

The two features compose: the intent-parsing layer needed for native voice
("add a DeWalt drill to the garage shelf" → `EntityCreate`) is exactly the
tool surface the MCP server defines. Building the MCP tools first gives the
native mode a tested action vocabulary to target.

---

## 1. What the codebase gives us today

Facts that shape the design (all paths relative to repo root):

### API & auth
- REST API v1 under `/api/v1`, routed in `backend/app/api/routes.go`. All
  inventory routes go through `userMW` = `mwAuthToken` → `mwTenant` →
  `mwRoles` (`backend/app/api/middleware.go`).
- **User API keys already exist** in this fork: `hbox_…` static tokens that
  authenticate as the owning user (`v1_ctrl_api_keys.go`;
  `middleware.go:243-286` hashes the token, resolves the user, marks the
  context `IsAPIKeyAuth`, touches `last_used`). This is a ready-made
  credential for an MCP server or any headless client — no new auth system
  needed.
- Swagger spec is served dynamically at `/swagger/doc.json` with the real
  host substituted — useful for generating an MCP tool surface or for
  OpenAPI-to-MCP bridges.

### Data model (what a voice command must produce)
- The core write is `EntityCreate` (`backend/internal/data/repo/repo_entities.go`):
  `name` (required), `quantity`, `description`, `parentId` (location),
  `entityTypeId`, `modelNumber`, `manufacturer`, `tagIds`. Note the
  precedent: `modelNumber`/`manufacturer` were added specifically so the
  **barcode product-search import flow** could prefill them — voice capture
  is the same shape of feature (external signal → prefilled create).
- Richer edits (purchase price/date, warranty, serial, custom fields) go
  through `EntityUpdate`/`EntityPatch` — voice v1 should *not* try to reach
  all of these; create + quantity + move covers the high-frequency actions.
- Locations are entities too (entity types with `isLocation`), queried as a
  tree via `GET /entities/tree` — voice commands will name locations
  fuzzily ("garage shelf"), so server-side fuzzy resolution against the
  tree is a required piece either way.

### Precedents to copy
- **External-API proxy pattern**: `v1_ctrl_product_search.go` proxies
  barcode lookups to third-party APIs with a server-side token from
  `BarcodeAPIConf` (`backend/internal/sys/config/conf.go`, env prefix
  `HBOX`, secrets redacted in `MarshalJSON`). A transcription proxy should
  be built exactly the same way: a `SpeechConf` block, key stays
  server-side, one new authenticated endpoint.
- **Media capture in the frontend**: `frontend/components/App/ScannerModal.vue`
  and `composables/use-barcode-detector.ts` already use
  `navigator.mediaDevices` (camera). Microphone capture via
  `MediaRecorder` follows the same permission/UX patterns.
- **Create UX**: `frontend/components/Entity/CreateModal.vue` already hosts
  auxiliary capture affordances in `#header-actions` (barcode scan / barcode
  input buttons). A mic button slots in beside them naturally, and the
  modal's prefill mechanics (templates, barcode import) are the model for
  "voice prefills the form, user confirms".

---

## 2. Option A — Native voice input mode

### 2.1 Which speech API (state of the art, mid-2026)

**Decision: hosted API only for v1 — we won't host the model ourselves.**
That narrows the comparison to the hosted offerings:

| Provider / model | Cost | Notes |
|---|---|---|
| **Mistral — Voxtral Mini Transcribe V2** | **$0.003/min** | Best price/perf of any hosted STT; ~4% WER on FLEURS; strong multilingual — the recommended default |
| **Mistral — Voxtral Realtime** | $0.006/min | Streaming, sub-200 ms latency; only needed if we ever do live dictation-as-you-speak rather than push-to-talk clips |
| **OpenAI — `gpt-4o-transcribe` / `whisper-1`** | $0.006/min | Top accuracy in third-party tests; `gpt-4o-mini-transcribe` ≈ $0.003/min |
| **Groq — hosted `whisper-large-v3-turbo`** | ≈ $0.04/**hour** | Cheapest hosted option, ~228× real-time; slightly behind the above on hard audio |
| Web Speech API (browser) | free | Zero-dependency fallback only: Chrome routes audio through Google, quality/language coverage is inconsistent, Firefox support is poor |

At these prices cost is a non-issue for this use case: a heavy user doing
fifty 10-second voice captures a day is ~8.5 min/day → about **2–5 cents a
month** on any of the $0.003–0.006/min tiers.

**Conclusion:** don't pick a vendor — pick the *interface*. Mistral, OpenAI,
and Groq all speak the **OpenAI `/v1/audio/transcriptions` multipart API**,
so one configurable base URL + model + key covers every option (and, later,
a self-hosted server exposing the same API, with no code change — Speaches,
faster-whisper-server, whisper.cpp server, vLLM serving Voxtral):

```yaml
# conf.go — new block, same shape as BarcodeAPIConf
speech:
  enabled: true
  base_url: https://api.mistral.ai/v1     # or https://api.openai.com/v1, https://api.groq.com/openai/v1
  model: voxtral-mini-transcribe-v2       # or gpt-4o-mini-transcribe, whisper-1, whisper-large-v3-turbo
  api_key: ${HBOX_SPEECH_API_KEY}         # redacted in config dump, like token_barcodespider
  language_hint: ""                       # optional; all engines auto-detect
```

Documented default: **Voxtral Mini Transcribe V2** — cheapest, accurate, and
excellent multilingual coverage (relevant given HomeBox's Weblate
translation investment). OpenAI and Groq are the drop-in alternates.

### 2.2 Backend: one proxy endpoint

Mirror the barcode proxy. Do **not** let the browser call the STT vendor
directly — the API key must stay server-side, and routing through the
backend keeps the provider swappable without touching the frontend.

```
POST /api/v1/actions/transcribe        (userMW, multipart audio ≤ ~15 MB, ~60 s cap)
  → forwards to {speech.base_url}/audio/transcriptions
  → { "text": "add two dewalt drill batteries to the garage shelf", "language": "en" }
```

Implementation notes:
- New handler `v1_ctrl_speech.go` following `v1_ctrl_product_search.go`
  (timeout, scheme allow-listing, error mapping); register in `routes.go`
  next to the other `/actions/*` routes, gated on `speech.enabled`.
- Reuse `WithMaxUploadSize` plumbing for the size cap; reject non-audio MIME.
- Rate-limit like `notifierTestLimiter` to keep a shared instance from
  running up a hosted-API bill.
- `GET /api/v1/status` (or a small `/api/v1/speech/config`) should expose
  `speechEnabled` so the UI only renders mic buttons when the server can
  actually transcribe.

### 2.3 Frontend: two tiers of voice UX

**Tier 1 — dictation (cheap, ship first).** A mic toggle on text inputs
(name, description, notes, search box). Push-to-talk → `MediaRecorder`
(`audio/webm;codecs=opus`, mono, 16 kHz is plenty for STT) → POST to
`/actions/transcribe` → insert text at cursor. No NLU, no ambiguity, works
in every locale the model supports. Components touched:
`Entity/CreateModal.vue`, the search input, plus a small
`composables/use-voice-input.ts` wrapping recorder + upload + state
(idle/recording/transcribing/error).

**Tier 2 — voice quick-capture (the actual feature).** A global
"hold-to-speak" action (toolbar button / mobile FAB / hotkey):

```
speak → transcript → intent parse → prefilled EntityCreate modal → user confirms
```

The intent-parse step is the design decision:

| Approach | How | Verdict |
|---|---|---|
| Heuristic grammar | regex/chrono-style parse of "add|create N X to/in Y", fuzzy-match Y against `GET /entities/tree`, X against templates/types | Zero extra dependencies; works for the 80% command shape; breaks on free-form phrasing and non-English word order |
| LLM function-call on the transcript | send transcript + location/tag/type context to a small LLM with one `create_entity` tool schema | Robust and multilingual, and cheap to add given the hosted-API decision: Mistral serves both transcription and small chat models behind the same key, so no second provider is required |
| **Voxtral audio-native function calling** | Voxtral Small/Mini (the audio-LLM, not the transcribe model) can emit function calls **directly from audio**, one round-trip, no separate NLU | Elegant, and a real Voxtral differentiator over Whisper — but locks the feature to one vendor family; keep as an optimization |

Recommendation: ship Tier 2 with the heuristic parser feeding the modal
(user always confirms before create — same trust model as barcode import),
and add an optional `speech.intent_model` LLM pass later. Never auto-commit
a create from voice without showing the parsed result.

**Mobile note:** iOS Safari's `MediaRecorder` yields `audio/mp4`; the proxy
should pass content-type through rather than assuming webm. Both Whisper and
Voxtral endpoints accept mp4/m4a/webm/wav.

### 2.4 Effort estimate

- Tier 1: ~2–4 days (config block + proxy handler + composable + mic button
  on 2–3 inputs + i18n strings + tests following `v1_ctrl_product_search_test.go`).
- Tier 2: +3–5 days for the grammar + tree fuzzy-match + quick-capture modal
  flow; LLM intent pass is another increment on top.

---

## 3. Option B — MCP server

### 3.1 Why this is the higher-leverage move

The voice+MCP client ecosystem matured through 2025–26: **Claude voice mode**
(mobile apps; now runs on Opus/Sonnet/Haiku and can use connected tools),
**ChatGPT voice + connectors/MCP**, **OpenAI Realtime API** (server-side MCP
tool support — a voice agent platform can point at your MCP URL), **Voice
Mode** (`mbailey/voicemode`, adds STT/TTS to Claude Desktop/Code with local
Whisper/Kokoro options), and **Home Assistant Assist** (MCP client
integration — voice satellites around the house driving HomeBox is a very
natural smart-home pairing). Every one of these already solves wake-word,
capture, STT, NLU, confirmation dialogue, and multi-turn repair ("no, the
*other* garage shelf"). HomeBox only has to expose *tools*.

### 3.2 Architecture: embed it in the existing binary

Three options considered:

1. **Embedded streamable-HTTP server in the HomeBox binary** ✅ recommended
   — official Go SDK `github.com/modelcontextprotocol/go-sdk` (stable v1.x,
   maintained with Google; streamable HTTP + stdio transports; spec-complete
   for 2025-11-25, with 2026-07-28 support landing). Mount at `/mcp` in
   `mountRoutes`, gated on `HBOX_MCP_ENABLED`. Tools call the existing
   `services`/`repo` layer *in-process* — same code path as the REST
   handlers, so tenant scoping and validation come for free.
2. Standalone sidecar (separate container speaking to the REST API) — right
   answer only if upstream doesn't want the dependency in-tree; doubles the
   deployment story for self-hosters and duplicates model types.
3. Local stdio bridge (`npx mcp-remote`-style) — not something to build;
   users can already bridge any remote HTTP server to stdio-only clients.

### 3.3 Auth

Reuse the existing `hbox_` API keys as **Bearer tokens on the `/mcp`
endpoint**, resolved by the same logic as `mwAuthToken`'s API-key branch
(`middleware.go:243+`), then `mwTenant` context. That works today for:
Claude Code / Claude Desktop (custom headers in config), Home Assistant,
Voice Mode, LibreChat, any self-hosted client.

Caveat to document honestly: **claude.ai / ChatGPT web "custom connector"
UIs and OpenAI Realtime expect OAuth 2.1** (dynamic client registration) for
remote servers with auth; static-header support there is inconsistent. Phase
1 ships Bearer-only (perfect for the self-hosted/LAN audience and desktop
clients); an OAuth AS is a significant chunk of work and should be a
separate, later decision — or punted to a reverse-proxy layer
(oauth2-proxy / MCP gateways) that self-hosters already run.

### 3.4 Tool surface (voice-first design)

Voice clients degrade with big tool lists and giant JSON schemas. Keep it to
~8 coarse, forgiving tools; resolve fuzziness server-side; return terse,
speakable text.

```
search_items(query, location?, tags?, limit=10)     → GET /entities equivalent (existing search)
get_item(query_or_id)                               → best match + details incl. full location path (/entities/{id}/path)
create_item(name, location, quantity?, description?, tags?)
                                                    → fuzzy-resolves location name against the tree; creates via EntityCreate;
                                                      returns "Created 'DeWalt drill' in Garage › Shelf B" (+ id)
update_quantity(item, delta_or_absolute)            → "use two batteries" / "there are five left" → EntityPatch.quantity
move_item(item, new_location)                       → EntityPatch.parentId with fuzzy location resolve
list_locations(parent?)                             → flattened tree with paths, for disambiguation turns
where_is(item)                                      → sugar over search+path; the #1 voice query for an inventory app
record_maintenance(item, note, cost?, date?)        → POST /entities/{id}/maintenance
```

Design rules that matter for voice:
- **Fuzzy match + confirm, never guess-commit:** if `location` matches
  multiple tree nodes, return the candidates ("Which one: Garage › Shelf A
  or Basement › Shelf?") instead of creating; the voice client will relay
  the question naturally.
- **Names in, names out:** callers pass names, tools resolve to UUIDs
  internally and never require the model to juggle IDs (return them, but
  accept names everywhere).
- **`readonly` config option** (`HBOX_MCP_READONLY=true`) for cautious users
  — registers only the query tools.
- Descriptions written for the model: include 2–3 example utterances per
  tool; this measurably improves voice-agent tool selection.

### 3.5 Sketch

```go
// backend/app/api/mcp.go (new)
import "github.com/modelcontextprotocol/go-sdk/mcp"

func (a *app) mountMCP(r *chi.Mux) {
    srv := mcp.NewServer(&mcp.Implementation{Name: "homebox", Version: version}, nil)

    mcp.AddTool(srv, &mcp.Tool{
        Name:        "create_item",
        Description: "Add an item to the inventory. Example: name='DeWalt drill', location='garage shelf', quantity=1.",
    }, a.mcpCreateItem) // typed handler: struct in → resolves location via repo tree → svc create

    // … remaining tools …

    handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
    r.Route("/mcp", func(r chi.Router) {
        r.Use(a.mwMCPAuth) // hbox_ API key → user+tenant ctx (reuse middleware.go API-key branch)
        r.Handle("/*", handler)
    })
}
```

Handlers reuse `a.services` / `a.repos` exactly as `V1Controller` does, so
group scoping, validation, and the event bus all behave identically to the
web UI.

### 3.6 Effort estimate

~3–5 days for the embedded server with the 8 tools, auth middleware reuse,
config flag, and integration tests (the SDK's in-memory transport makes tool
tests cheap). Docs page ("connect HomeBox to Claude / Home Assistant /
ChatGPT") is half the value — budget a day for it.

### 3.7 Where does the agent loop live?

"Voice changes my inventory" implies an agent loop somewhere: something has
to hear intent, decide on tool calls, execute them, and handle follow-ups.
The three tiers place that loop very differently, and only one of them puts
it in HomeBox:

| Tier | Agent loop / tool calls | Confirmation step |
|---|---|---|
| **Dictation (PRs #1+#2, shipped)** | **None.** Deterministic pipeline: mic → STT → text prefills the form or search box. No LLM, no tool calls. | The user reviews the drafted form and clicks Create |
| **MCP (PR #3, shipped)** | **In the client.** Claude voice mode / ChatGPT / Home Assistant runs the loop: speech → its LLM picks `create_item` / `where_is` / `move_item` → our server executes → the model speaks the result. HomeBox implements *tools*, never the loop | The client's conversation ("which shelf — A or B?"); ambiguity errors from our tools feed exactly that |
| **In-app voice commands (not built)** | **Would be ours to build**: transcript → one LLM function call against the same action vocabulary as the MCP tools → *proposed* action | The proposal prefills the create/edit UI; the user's confirm click replaces multi-turn repair |

Design notes for the unbuilt third tier, if/when wanted:

- It is a **single-shot function call, not a full loop**. The UI confirm
  step does the job that multi-turn repair does in a voice assistant, so
  there's no need for conversation state, retries, or an executor loop in
  the backend — one `POST /actions/voice-command` that returns a proposed
  `create_item`-shaped payload (plus resolved location candidates) is
  enough.
- **No second provider needed**: Mistral serves both transcription
  (Voxtral) and small chat models with function calling behind the same
  API key and OpenAI-compatible interface, so the existing `speech:` config
  block extends with one `intent_model` field.
- **Reuse the MCP action vocabulary.** The MCP tools' input schemas and
  fuzzy location resolution are precisely the "grammar" the LLM should
  target — keeping the two surfaces identical means one tested behavior
  for "add X to Y" everywhere.
- Voxtral Small/Mini (the audio LLM, not the transcribe model) can emit
  function calls **directly from audio**, collapsing STT + intent into one
  round trip — a later optimization, at the cost of vendor coupling.

---

## 4. How the options compare

| | Native voice mode | MCP server |
|---|---|---|
| Audio/STT code in HomeBox | Yes (proxy + recorder UX) | **None** |
| NLU / multi-turn repair | Ours to build (hard part) | Client's job (already great) |
| Works on phone while standing at a shelf | Web UI mic button | Claude/ChatGPT voice apps, HA voice satellites |
| Offline/LAN-only capable | Not in v1 (hosted STT API by decision; local server possible later, no code change) | Yes with local MCP clients (HA, Voice Mode + local Whisper) |
| New external dependency | Hosted STT API (pluggable: Mistral/OpenAI/Groq) | Go SDK only |
| Benefits beyond voice | Dictation | **Whole agent ecosystem**: automations, bulk edits, "what did I buy last year", Claude Code scripting |
| Est. effort | ~1 wk (T1+T2) | ~0.5–1 wk |

Sequencing again: **MCP server → Tier-1 dictation → Tier-2 quick-capture**,
sharing the fuzzy-location-resolver and action vocabulary between MCP tools
and the native intent parser.

---

## 5. Open questions for upstream / next steps

1. Embed MCP in-tree vs. sidecar repo — needs a maintainer call
   (recommend in-tree behind a default-off flag).
2. OAuth for claude.ai/ChatGPT web connectors — defer; Bearer covers the
   self-hosted audience now.
3. Should the speech proxy also accept an optional `intent=true` flag that
   runs the parse server-side (shared with MCP `create_item` resolution)?
   Keeps the frontend dumb and the grammar in one place — leaning yes.
4. Demo-mode guardrails: disable `create_item`/writes on `a.conf.Demo`
   instances, same as password change is disabled today.

## Sources

- [Mistral — Voxtral](https://mistral.ai/news/voxtral/) · [Voxtral Transcribe 2](https://mistral.ai/news/voxtral-transcribe-2/)
- [VentureBeat — Voxtral Transcribe 2 open-source, on-device](https://venturebeat.com/technology/mistral-drops-voxtral-transcribe-2-an-open-source-speech-model-that-runs-on)
- [Simon Willison — Voxtral transcribes at the speed of sound](https://simonwillison.net/2026/Feb/4/voxtral-2/)
- [OpenRouter — Voxtral Mini Transcribe pricing](https://openrouter.ai/mistralai/voxtral-mini-transcribe)
- [Northflank — Best open-source STT 2026 benchmarks](https://northflank.com/blog/best-open-source-speech-to-text-stt-model-in-2026-benchmarks)
- [TokenMix — GPT-4o-Transcribe vs Whisper 2026](https://tokenmix.ai/blog/gpt-4o-transcribe-vs-whisper-review-2026)
- [Deepgram — Best STT APIs 2026](https://deepgram.com/learn/best-speech-to-text-apis-2026)
- [modelcontextprotocol/go-sdk v1.0.0 release](https://github.com/modelcontextprotocol/go-sdk/releases/tag/v1.0.0) · [releases](https://github.com/modelcontextprotocol/go-sdk/releases)
- [MCP blog — SDK betas for 2026-07-28 spec](https://blog.modelcontextprotocol.io/posts/sdk-betas-2026-07-28/)
- [TechCrunch — Claude voice mode update](https://techcrunch.com/2026/07/23/anthropic-updates-claude-voice-mode-with-more-capable-models/)
- [Voice Mode MCP server](https://mcp.so/server/voice-mode/mbailey)
