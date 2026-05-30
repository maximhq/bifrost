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

// --- B: top-level system + mid-conv, supported ------------------------------

func TestRoundTrip_B_TopLevelAndMidConv_Supported(t *testing.T) {
	messages := []AnthropicMessage{
		anthMsg(AnthropicMessageRoleUser, "Hello"),
		anthMsg(AnthropicMessageRoleAssistant, "Hi there!"),
		anthMsg(AnthropicMessageRoleSystem, "From now on, be concise."),
		anthMsg(AnthropicMessageRoleUser, "Tell me about Go."),
	}
	system := systemStr("You are a helpful assistant.")

	outMsgs, outSystem := roundTrip(t, messages, system, schemas.Anthropic, "claude-opus-4-8")

	// Top-level system is unchanged.
	got := textBlocks(outSystem)
	if len(got) != 1 || got[0] != "You are a helpful assistant." {
		t.Errorf("system = %v, want [\"You are a helpful assistant.\"]", got)
	}
	// Role sequence: user,assistant,system,user — mid-conv system preserved in array.
	want := "user,assistant,system,user"
	if roleSeq(outMsgs) != want {
		t.Errorf("role seq = %q, want %q", roleSeq(outMsgs), want)
	}
	// Mid-conv system carries the right text.
	if outMsgs[2].Role != AnthropicMessageRoleSystem {
		t.Fatalf("msg[2].role = %q, want system", outMsgs[2].Role)
	}
	midText := textBlocks(&outMsgs[2].Content)
	if len(midText) == 0 || midText[0] != "From now on, be concise." {
		t.Errorf("mid-conv system text = %v, want [\"From now on, be concise.\"]", midText)
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

	// System must contain BOTH initial and mid-conv text (appended, not overwritten).
	got := textBlocks(outSystem)
	if len(got) != 2 {
		t.Fatalf("system blocks = %v (len=%d), want 2", got, len(got))
	}
	if got[0] != "You are a helpful assistant." {
		t.Errorf("system[0] = %q, want \"You are a helpful assistant.\"", got[0])
	}
	if got[1] != "Be concise." {
		t.Errorf("system[1] = %q, want \"Be concise.\"", got[1])
	}
	// No role:"system" in messages array.
	for i, m := range outMsgs {
		if m.Role == AnthropicMessageRoleSystem {
			t.Errorf("msg[%d] has role:system — must not appear for Bedrock", i)
		}
	}
	// Mid-conv system removed from message sequence.
	if want := "user,assistant,user"; roleSeq(outMsgs) != want {
		t.Errorf("role seq = %q, want %q", roleSeq(outMsgs), want)
	}
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

	// Initial system preserved; mid-conv appended.
	got := textBlocks(outSystem)
	if len(got) != 2 {
		t.Fatalf("system blocks = %v (len=%d), want 2", got, len(got))
	}
	if got[0] != "Initial instruction." {
		t.Errorf("system[0] = %q, want \"Initial instruction.\"", got[0])
	}
	if got[1] != "Be concise." {
		t.Errorf("system[1] = %q, want \"Be concise.\"", got[1])
	}
	// No system role in messages.
	for i, m := range outMsgs {
		if m.Role == AnthropicMessageRoleSystem {
			t.Errorf("msg[%d] has role:system for Opus 4.7", i)
		}
	}
	if want := "user,assistant,user"; roleSeq(outMsgs) != want {
		t.Errorf("role seq = %q, want %q", roleSeq(outMsgs), want)
	}
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

	// No top-level system expected.
	if outSystem != nil {
		t.Errorf("system = %v, want nil (no initial system was set)", textBlocks(outSystem))
	}
	// Mid-conv system preserved at correct position.
	if want := "user,assistant,system,user"; roleSeq(outMsgs) != want {
		t.Errorf("role seq = %q, want %q", roleSeq(outMsgs), want)
	}
	midText := textBlocks(&outMsgs[2].Content)
	if len(midText) == 0 || midText[0] != "Only mid-conv instruction." {
		t.Errorf("mid-conv text = %v, want [\"Only mid-conv instruction.\"]", midText)
	}
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

	// Mid-conv gets promoted to top-level system (no initial to append to).
	got := textBlocks(outSystem)
	if len(got) != 1 {
		t.Fatalf("system blocks = %v (len=%d), want exactly 1 promoted block", got, len(got))
	}
	if got[0] != "Mid-conv instruction." {
		t.Errorf("system[0] = %q, want \"Mid-conv instruction.\"", got[0])
	}
	// No system role in messages.
	for i, m := range outMsgs {
		if m.Role == AnthropicMessageRoleSystem {
			t.Errorf("msg[%d] has role:system for Bedrock", i)
		}
	}
	if want := "user,assistant,user"; roleSeq(outMsgs) != want {
		t.Errorf("role seq = %q, want %q", roleSeq(outMsgs), want)
	}
}

// --- G: multiple mid-conv system messages, supported -----------------------

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

	// Top-level system untouched.
	got := textBlocks(outSystem)
	if len(got) != 1 || got[0] != "Initial." {
		t.Errorf("system = %v, want [\"Initial.\"]", got)
	}
	// Both mid-conv system messages preserved in position.
	if want := "user,system,assistant,system,user"; roleSeq(outMsgs) != want {
		t.Errorf("role seq = %q, want %q", roleSeq(outMsgs), want)
	}
	mid1 := textBlocks(&outMsgs[1].Content)
	if len(mid1) == 0 || mid1[0] != "Mid1." {
		t.Errorf("mid1 text = %v, want [\"Mid1.\"]", mid1)
	}
	mid2 := textBlocks(&outMsgs[3].Content)
	if len(mid2) == 0 || mid2[0] != "Mid2." {
		t.Errorf("mid2 text = %v, want [\"Mid2.\"]", mid2)
	}
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

	// All three system texts land in the top-level field in order.
	got := textBlocks(outSystem)
	if len(got) != 3 {
		t.Fatalf("system blocks = %v (len=%d), want 3 (Initial+Mid1+Mid2)", got, len(got))
	}
	for i, want := range []string{"Initial.", "Mid1.", "Mid2."} {
		if got[i] != want {
			t.Errorf("system[%d] = %q, want %q", i, got[i], want)
		}
	}
	// No system role remains in the messages array.
	for i, m := range outMsgs {
		if m.Role == AnthropicMessageRoleSystem {
			t.Errorf("msg[%d] has role:system — must not appear for Bedrock", i)
		}
	}
	if want := "user,assistant,user"; roleSeq(outMsgs) != want {
		t.Errorf("role seq = %q, want %q", roleSeq(outMsgs), want)
	}
}

// --- content-blocks variant: system sent as ContentBlocks not ContentStr ---

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

	// Top-level system preserved.
	got := textBlocks(outSystem)
	if len(got) != 1 || got[0] != initialText {
		t.Errorf("system = %v, want [%q]", got, initialText)
	}
	// Mid-conv system preserved in position.
	if want := "user,system,user"; roleSeq(outMsgs) != want {
		t.Errorf("role seq = %q, want %q", roleSeq(outMsgs), want)
	}
	mid := textBlocks(&outMsgs[1].Content)
	if len(mid) == 0 || mid[0] != midText {
		t.Errorf("mid-conv text = %v, want [%q]", mid, midText)
	}
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

	// Both blocks merged into top-level system.
	got := textBlocks(outSystem)
	if len(got) != 2 {
		t.Fatalf("system blocks = %v (len=%d), want 2", got, len(got))
	}
	if got[0] != initialText {
		t.Errorf("system[0] = %q, want %q", got[0], initialText)
	}
	if got[1] != midText {
		t.Errorf("system[1] = %q, want %q", got[1], midText)
	}
	for i, m := range outMsgs {
		if m.Role == AnthropicMessageRoleSystem {
			t.Errorf("msg[%d] has role:system for Bedrock", i)
		}
	}
}
