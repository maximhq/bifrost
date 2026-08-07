package anthropic

// Round-trip tests: AnthropicMessageRequest ──► Bifrost ──► AnthropicMessageRequest
//
// Coverage matrix:
//   A) Only a top-level system field (no mid-conv messages)
//   B) Top-level system + mid-conv system, supported provider+model  (Anthropic + Opus 4.8)
//   C) Top-level system + mid-conv system, unsupported provider      (Bedrock)
//   D) Top-level system + mid-conv system, unsupported model         (Opus 4.7)
//   E) No top-level system, mid-conv only, supported
//   F) No top-level system, mid-conv only, unsupported
//   G) Multiple mid-conv system messages, supported
//   H) Multiple mid-conv system messages, unsupported
//
// "Supported" means provider=Anthropic + model=claude-opus-4-8 (SupportsMidConversationSystem=true).

import (
	"context"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// --- helpers ----------------------------------------------------------------

// textBlocks normalises an AnthropicContent to a slice of plain strings,
// regardless of whether the content is stored as ContentStr or ContentBlocks.
func textBlocks(c *AnthropicContent) []string {
	if c == nil {
		return nil
	}
	if c.ContentStr != nil {
		return []string{*c.ContentStr}
	}
	var out []string
	for _, b := range c.ContentBlocks {
		if b.Text != nil {
			out = append(out, *b.Text)
		}
	}
	return out
}

func anthMsg(role AnthropicMessageRole, text string) AnthropicMessage {
	return AnthropicMessage{
		Role:    role,
		Content: AnthropicContent{ContentStr: &text},
	}
}

func systemStr(s string) *AnthropicContent {
	return &AnthropicContent{ContentStr: &s}
}

// roundTrip runs the full conversion pipeline:
//
//	ConvertAnthropicMessagesToBifrostMessages  (Anthropic → Bifrost)
//	ConvertBifrostMessagesToAnthropicMessages  (Bifrost   → Anthropic)
func roundTrip(t *testing.T, messages []AnthropicMessage, system *AnthropicContent, provider schemas.ModelProvider, model string) ([]AnthropicMessage, *AnthropicContent) {
	t.Helper()
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	bifrost := ConvertAnthropicMessagesToBifrostMessages(ctx, messages, system, false, false)
	outMsgs, outSystem := ConvertBifrostMessagesToAnthropicMessages(ctx, bifrost, true, provider, model)
	return outMsgs, outSystem
}

// roleSeq returns the role sequence of a message slice as a readable string.
func roleSeq(msgs []AnthropicMessage) string {
	var roles []string
	for _, m := range msgs {
		roles = append(roles, string(m.Role))
	}
	return strings.Join(roles, ",")
}

// --- A: only top-level system -----------------------------------------------

func TestRoundTrip_A_TopLevelSystemOnly(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider schemas.ModelProvider
		model    string
	}{
		{"anthropic-opus48", schemas.Anthropic, "claude-opus-4-8"},
		{"bedrock-opus48", schemas.Bedrock, "global.anthropic.claude-opus-4-8"},
		{"anthropic-opus47", schemas.Anthropic, "claude-opus-4-7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := []AnthropicMessage{
				anthMsg(AnthropicMessageRoleUser, "Hello"),
				anthMsg(AnthropicMessageRoleAssistant, "Hi there!"),
				anthMsg(AnthropicMessageRoleUser, "How are you?"),
			}
			system := systemStr("You are a helpful assistant.")

			outMsgs, outSystem := roundTrip(t, messages, system, tc.provider, tc.model)

			// System field preserved.
			got := textBlocks(outSystem)
			if len(got) != 1 || got[0] != "You are a helpful assistant." {
				t.Errorf("system = %v, want [\"You are a helpful assistant.\"]", got)
			}
			// No role:"system" in messages.
			for i, m := range outMsgs {
				if m.Role == AnthropicMessageRoleSystem {
					t.Errorf("msg[%d] unexpectedly has role:system", i)
				}
			}
			// Message count and role sequence preserved.
			if want := "user,assistant,user"; roleSeq(outMsgs) != want {
				t.Errorf("role seq = %q, want %q", roleSeq(outMsgs), want)
			}
		})
	}
}

// inlinedText renders the <system-reminder> envelope a mid-conversation system message takes when
// it is inlined into a user turn (see inlineMidConversationSystem).
func inlinedText(text string) string {
	return "<system-reminder>\n" + text + "\n</system-reminder>\n"
}

// assertInlined asserts `text` reached the messages array wrapped in a user turn and did NOT get
// hoisted into the top-level system block.
func assertInlined(t *testing.T, outMsgs []AnthropicMessage, outSystem *AnthropicContent, text string) {
	t.Helper()
	want := inlinedText(text)
	var found bool
	for _, m := range outMsgs {
		if m.Role != AnthropicMessageRoleUser {
			continue
		}
		for _, got := range textBlocks(&m.Content) {
			if got == want {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("expected %q inlined as a <system-reminder> user turn; roles = %q", text, roleSeq(outMsgs))
	}
	for _, got := range textBlocks(outSystem) {
		if strings.Contains(got, text) {
			t.Errorf("%q was hoisted into top-level system; that collapses the cached prefix", text)
		}
	}
}

// --- B: top-level system + mid-conv, supported model, ILLEGAL placement -----

// The [user, assistant, system, user] shape here is Claude Code's real traffic shape, and it
// violates both of Anthropic's placement clauses: the system turn follows an assistant rather
// than a user, and is followed by a user rather than an assistant. The API rejects it with
// "messages.2: role 'system' must follow a 'user' message". This test previously asserted the
// converter forwards it as role:"system" — i.e. it certified a request the endpoint 400s on,
// because the converter never checked placement and the round-trip never touched the API. The
// converter now falls back to inlining, which is accepted and keeps the cache anchor in
// `messages`. See TestRoundTrip_B2 for the legal placement that still forwards natively.
func TestRoundTrip_B_TopLevelAndMidConv_Supported(t *testing.T) {
	messages := []AnthropicMessage{
		anthMsg(AnthropicMessageRoleUser, "Hello"),
		anthMsg(AnthropicMessageRoleAssistant, "Hi there!"),
		anthMsg(AnthropicMessageRoleSystem, "From now on, be concise."),
		anthMsg(AnthropicMessageRoleUser, "Tell me about Go."),
	}
	system := systemStr("You are a helpful assistant.")

	outMsgs, outSystem := roundTrip(t, messages, system, schemas.Anthropic, "claude-opus-4-8")

	if got := textBlocks(outSystem); len(got) != 1 || got[0] != "You are a helpful assistant." {
		t.Errorf("system = %v, want [\"You are a helpful assistant.\"]", got)
	}
	if want := "user,assistant,user,user"; roleSeq(outMsgs) != want {
		t.Errorf("role seq = %q, want %q", roleSeq(outMsgs), want)
	}
	assertInlined(t, outMsgs, outSystem, "From now on, be concise.")
}

// --- B2: top-level system + mid-conv, supported model, LEGAL placement ------

// The native forwarding path: the system turn follows a user message and is followed by an
// assistant turn, satisfying both clauses, so it is emitted as role:"system" unchanged.
func TestRoundTrip_B2_MidConvLegalPlacement_Supported(t *testing.T) {
	messages := []AnthropicMessage{
		anthMsg(AnthropicMessageRoleUser, "Hello"),
		anthMsg(AnthropicMessageRoleSystem, "From now on, be concise."),
		anthMsg(AnthropicMessageRoleAssistant, "Understood."),
	}
	system := systemStr("You are a helpful assistant.")

	outMsgs, outSystem := roundTrip(t, messages, system, schemas.Anthropic, "claude-opus-4-8")

	if got := textBlocks(outSystem); len(got) != 1 || got[0] != "You are a helpful assistant." {
		t.Errorf("system = %v, want [\"You are a helpful assistant.\"]", got)
	}
	if want := "user,system,assistant"; roleSeq(outMsgs) != want {
		t.Fatalf("role seq = %q, want %q", roleSeq(outMsgs), want)
	}
	if got := textBlocks(&outMsgs[1].Content); len(got) == 0 || got[0] != "From now on, be concise." {
		t.Errorf("mid-conv system text = %v, want [\"From now on, be concise.\"]", got)
	}
}

// --- C: top-level system + mid-conv, unsupported provider (Bedrock) ---------

func TestRoundTrip_C_TopLevelAndMidConv_Bedrock(t *testing.T) {
	messages := []AnthropicMessage{
		anthMsg(AnthropicMessageRoleUser, "Hello"),
		anthMsg(AnthropicMessageRoleAssistant, "Hi there!"),
		anthMsg(AnthropicMessageRoleSystem, "Be concise."),
		anthMsg(AnthropicMessageRoleUser, "Tell me about Go."),
	}
	system := systemStr("You are a helpful assistant.")

	outMsgs, outSystem := roundTrip(t, messages, system, schemas.Bedrock, "global.anthropic.claude-opus-4-8")

	// Only the leading system prompt stays in the top-level block. Appending the mid-conversation
	// text here (the previous behavior) renders it ahead of every message and invalidates the
	// cached prefix behind it.
	if got := textBlocks(outSystem); len(got) != 1 || got[0] != "You are a helpful assistant." {
		t.Errorf("system = %v, want exactly [\"You are a helpful assistant.\"]", got)
	}
	for i, m := range outMsgs {
		if m.Role == AnthropicMessageRoleSystem {
			t.Errorf("msg[%d] has role:system — must not appear for Bedrock", i)
		}
	}
	if want := "user,assistant,user,user"; roleSeq(outMsgs) != want {
		t.Errorf("role seq = %q, want %q", roleSeq(outMsgs), want)
	}
	assertInlined(t, outMsgs, outSystem, "Be concise.")
}

// --- D: top-level system + mid-conv, unsupported model (Opus 4.7) -----------

func TestRoundTrip_D_TopLevelAndMidConv_Opus47(t *testing.T) {
	messages := []AnthropicMessage{
		anthMsg(AnthropicMessageRoleUser, "Hello"),
		anthMsg(AnthropicMessageRoleAssistant, "Hi!"),
		anthMsg(AnthropicMessageRoleSystem, "Be concise."),
		anthMsg(AnthropicMessageRoleUser, "Continue."),
	}
	system := systemStr("Initial instruction.")

	outMsgs, outSystem := roundTrip(t, messages, system, schemas.Anthropic, "claude-opus-4-7")

	if got := textBlocks(outSystem); len(got) != 1 || got[0] != "Initial instruction." {
		t.Errorf("system = %v, want exactly [\"Initial instruction.\"]", got)
	}
	for i, m := range outMsgs {
		if m.Role == AnthropicMessageRoleSystem {
			t.Errorf("msg[%d] has role:system for Opus 4.7", i)
		}
	}
	if want := "user,assistant,user,user"; roleSeq(outMsgs) != want {
		t.Errorf("role seq = %q, want %q", roleSeq(outMsgs), want)
	}
	assertInlined(t, outMsgs, outSystem, "Be concise.")
}

// --- E: no top-level system, mid-conv only, supported -----------------------

func TestRoundTrip_E_NoTopLevelSystem_MidConvOnly_Supported(t *testing.T) {
	messages := []AnthropicMessage{
		anthMsg(AnthropicMessageRoleUser, "Hello"),
		anthMsg(AnthropicMessageRoleAssistant, "Hi!"),
		anthMsg(AnthropicMessageRoleSystem, "Only mid-conv instruction."),
		anthMsg(AnthropicMessageRoleUser, "Continue."),
	}

	outMsgs, outSystem := roundTrip(t, messages, nil, schemas.Anthropic, "claude-opus-4-8")

	// Nothing is promoted into the top-level system block: inlining leaves it empty, which is
	// what keeps the (absent) prefix from being invalidated.
	if outSystem != nil {
		t.Errorf("system = %v, want nil (no initial system was set)", textBlocks(outSystem))
	}
	if want := "user,assistant,user,user"; roleSeq(outMsgs) != want {
		t.Errorf("role seq = %q, want %q", roleSeq(outMsgs), want)
	}
	assertInlined(t, outMsgs, outSystem, "Only mid-conv instruction.")
}

// --- F: no top-level system, mid-conv only, unsupported ---------------------

func TestRoundTrip_F_NoTopLevelSystem_MidConvOnly_Bedrock(t *testing.T) {
	messages := []AnthropicMessage{
		anthMsg(AnthropicMessageRoleUser, "Hello"),
		anthMsg(AnthropicMessageRoleAssistant, "Hi!"),
		anthMsg(AnthropicMessageRoleSystem, "Mid-conv instruction."),
		anthMsg(AnthropicMessageRoleUser, "Continue."),
	}

	outMsgs, outSystem := roundTrip(t, messages, nil, schemas.Bedrock, "global.anthropic.claude-opus-4-8")

	if outSystem != nil {
		t.Errorf("system = %v, want nil — mid-conv content inlines rather than being promoted", textBlocks(outSystem))
	}
	for i, m := range outMsgs {
		if m.Role == AnthropicMessageRoleSystem {
			t.Errorf("msg[%d] has role:system for Bedrock", i)
		}
	}
	if want := "user,assistant,user,user"; roleSeq(outMsgs) != want {
		t.Errorf("role seq = %q, want %q", roleSeq(outMsgs), want)
	}
	assertInlined(t, outMsgs, outSystem, "Mid-conv instruction.")
}

// --- G: multiple mid-conv system messages, supported -----------------------

// Mixed placement in one request: Mid1 follows a user and is followed by an assistant (legal, so
// it forwards natively), while Mid2 follows an assistant (illegal, so it inlines). The two are
// decided independently — one bad placement does not force the whole request down the fallback.
func TestRoundTrip_G_MultipleMidConv_Supported(t *testing.T) {
	messages := []AnthropicMessage{
		anthMsg(AnthropicMessageRoleUser, "Q1"),
		anthMsg(AnthropicMessageRoleSystem, "Mid1."),
		anthMsg(AnthropicMessageRoleAssistant, "A1"),
		anthMsg(AnthropicMessageRoleSystem, "Mid2."),
		anthMsg(AnthropicMessageRoleUser, "Q2"),
	}
	system := systemStr("Initial.")

	outMsgs, outSystem := roundTrip(t, messages, system, schemas.Anthropic, "claude-opus-4-8")

	if got := textBlocks(outSystem); len(got) != 1 || got[0] != "Initial." {
		t.Errorf("system = %v, want [\"Initial.\"]", got)
	}
	if want := "user,system,assistant,user,user"; roleSeq(outMsgs) != want {
		t.Fatalf("role seq = %q, want %q", roleSeq(outMsgs), want)
	}
	if got := textBlocks(&outMsgs[1].Content); len(got) == 0 || got[0] != "Mid1." {
		t.Errorf("mid1 (legal placement, forwarded) = %v, want [\"Mid1.\"]", got)
	}
	assertInlined(t, outMsgs, outSystem, "Mid2.")
}

// --- H: multiple mid-conv system messages, unsupported ----------------------

func TestRoundTrip_H_MultipleMidConv_Bedrock(t *testing.T) {
	messages := []AnthropicMessage{
		anthMsg(AnthropicMessageRoleUser, "Q1"),
		anthMsg(AnthropicMessageRoleSystem, "Mid1."),
		anthMsg(AnthropicMessageRoleAssistant, "A1"),
		anthMsg(AnthropicMessageRoleSystem, "Mid2."),
		anthMsg(AnthropicMessageRoleUser, "Q2"),
	}
	system := systemStr("Initial.")

	outMsgs, outSystem := roundTrip(t, messages, system, schemas.Bedrock, "global.anthropic.claude-opus-4-8")

	// Only the leading prompt stays hoisted; both reminders inline. Previously all three texts
	// were concatenated into `system`, which is what pinned the cacheable prefix at the floor.
	if got := textBlocks(outSystem); len(got) != 1 || got[0] != "Initial." {
		t.Errorf("system = %v, want exactly [\"Initial.\"]", got)
	}
	for i, m := range outMsgs {
		if m.Role == AnthropicMessageRoleSystem {
			t.Errorf("msg[%d] has role:system — must not appear for Bedrock", i)
		}
	}
	if want := "user,user,assistant,user,user"; roleSeq(outMsgs) != want {
		t.Errorf("role seq = %q, want %q", roleSeq(outMsgs), want)
	}
	assertInlined(t, outMsgs, outSystem, "Mid1.")
	assertInlined(t, outMsgs, outSystem, "Mid2.")
}

// --- content-blocks variant: system sent as ContentBlocks not ContentStr ---

// Placement here satisfies the follows-a-user clause but not the trailing clause (a user follows),
// so even on a supporting model it takes the inline fallback rather than a 400.
func TestRoundTrip_ContentBlocks_Supported(t *testing.T) {
	initialText := "Initial (blocks)."
	midText := "Mid (blocks)."
	system := &AnthropicContent{
		ContentBlocks: []AnthropicContentBlock{
			{Type: AnthropicContentBlockTypeText, Text: &initialText},
		},
	}
	messages := []AnthropicMessage{
		anthMsg(AnthropicMessageRoleUser, "Hello"),
		{
			Role: AnthropicMessageRoleSystem,
			Content: AnthropicContent{
				ContentBlocks: []AnthropicContentBlock{
					{Type: AnthropicContentBlockTypeText, Text: &midText},
				},
			},
		},
		anthMsg(AnthropicMessageRoleUser, "Continue."),
	}

	outMsgs, outSystem := roundTrip(t, messages, system, schemas.Anthropic, "claude-opus-4-8")

	if got := textBlocks(outSystem); len(got) != 1 || got[0] != initialText {
		t.Errorf("system = %v, want [%q]", got, initialText)
	}
	if want := "user,user,user"; roleSeq(outMsgs) != want {
		t.Errorf("role seq = %q, want %q", roleSeq(outMsgs), want)
	}
	assertInlined(t, outMsgs, outSystem, midText)
}

func TestRoundTrip_ContentBlocks_Bedrock(t *testing.T) {
	initialText := "Initial (blocks)."
	midText := "Mid (blocks)."
	system := &AnthropicContent{
		ContentBlocks: []AnthropicContentBlock{
			{Type: AnthropicContentBlockTypeText, Text: &initialText},
		},
	}
	messages := []AnthropicMessage{
		anthMsg(AnthropicMessageRoleUser, "Hello"),
		{
			Role: AnthropicMessageRoleSystem,
			Content: AnthropicContent{
				ContentBlocks: []AnthropicContentBlock{
					{Type: AnthropicContentBlockTypeText, Text: &midText},
				},
			},
		},
		anthMsg(AnthropicMessageRoleUser, "Continue."),
	}

	outMsgs, outSystem := roundTrip(t, messages, system, schemas.Bedrock, "global.anthropic.claude-opus-4-8")

	if got := textBlocks(outSystem); len(got) != 1 || got[0] != initialText {
		t.Errorf("system = %v, want exactly [%q]", got, initialText)
	}
	for i, m := range outMsgs {
		if m.Role == AnthropicMessageRoleSystem {
			t.Errorf("msg[%d] has role:system for Bedrock", i)
		}
	}
	assertInlined(t, outMsgs, outSystem, midText)
}

// --- container_upload -------------------------------------------------------

// findContainerUploadBlocks collects every container_upload block across a
// message slice.
func findContainerUploadBlocks(msgs []AnthropicMessage) []AnthropicContentBlock {
	var out []AnthropicContentBlock
	for _, m := range msgs {
		for _, b := range m.Content.ContentBlocks {
			if b.Type == AnthropicContentBlockTypeContainerUpload {
				out = append(out, b)
			}
		}
	}
	return out
}

// container_upload (file staged into the code-execution container) must survive
// Anthropic → Bifrost → Anthropic, carrying file_id and cache_control.
func TestRoundTrip_ContainerUpload(t *testing.T) {
	fileID := "file_011CcpBQA2BV1gthmNPSYzkh"
	messages := []AnthropicMessage{
		{
			Role: AnthropicMessageRoleUser,
			Content: AnthropicContent{
				ContentBlocks: []AnthropicContentBlock{
					{Type: AnthropicContentBlockTypeText, Text: schemas.Ptr("analyse and share model names")},
					{
						Type:         AnthropicContentBlockTypeContainerUpload,
						FileID:       &fileID,
						CacheControl: &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral},
					},
				},
			},
		},
	}

	outMsgs, _ := roundTrip(t, messages, nil, schemas.Anthropic, "claude-sonnet-4-6")

	blocks := findContainerUploadBlocks(outMsgs)
	if len(blocks) != 1 {
		t.Fatalf("container_upload blocks = %d, want 1", len(blocks))
	}
	if blocks[0].FileID == nil || *blocks[0].FileID != fileID {
		t.Errorf("file_id = %v, want %q", blocks[0].FileID, fileID)
	}
	if blocks[0].CacheControl == nil || blocks[0].CacheControl.Type != schemas.CacheControlTypeEphemeral {
		t.Errorf("cache_control = %v, want ephemeral", blocks[0].CacheControl)
	}
}

// The grouped converter (used for Bedrock-routed Anthropic models) must also
// emit a neutral container_upload block rather than dropping it.
func TestRoundTrip_ContainerUpload_Grouped(t *testing.T) {
	fileID := "file_grouped_123"
	role := schemas.ResponsesInputMessageRoleUser
	msgs := convertAnthropicContentBlocksToResponsesMessagesGrouped(
		[]AnthropicContentBlock{{Type: AnthropicContentBlockTypeContainerUpload, FileID: &fileID}},
		&role, false,
	)

	var found *schemas.ResponsesMessageContentBlock
	for i := range msgs {
		if msgs[i].Content == nil {
			continue
		}
		for j := range msgs[i].Content.ContentBlocks {
			if msgs[i].Content.ContentBlocks[j].Type == schemas.ResponsesInputMessageContentBlockTypeContainer {
				found = &msgs[i].Content.ContentBlocks[j]
			}
		}
	}
	if found == nil {
		t.Fatalf("grouped converter dropped container_upload block")
	}
	if found.FileID == nil || *found.FileID != fileID {
		t.Errorf("file_id = %v, want %q", found.FileID, fileID)
	}
}
