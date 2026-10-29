package warp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

// Management tools.
//
// Beside its own Q&A tools, Warp reaches Bifrost's /api through the gateway's
// management MCP (the server behind /v1/mcp), in process and as the person
// asking, so /api's auth and permissions decide what each call may do. Reads run
// when the model calls them. A write never runs on the model's say-so: the turn
// ends with a confirmation card naming the call, and the call runs only when the
// person presses Confirm on that card. The pending call is filed with the card in
// conversation history, so the Confirm is checked against the server's own
// record of what was asked, never against anything the request body says.

const (
	// ConfirmQuestionKind marks a question that asks the person to confirm a
	// management write.
	ConfirmQuestionKind = "confirm"
	// confirmHintPrefix starts the hint a confirmation card's Confirm option sends.
	confirmHintPrefix = "confirm:"
	// cancelHint is what the card's Cancel option sends.
	cancelHint = "cancel"
	// PendingCallTTL is how long a confirmation card stays good. An old card is
	// one the person may no longer remember the context of.
	PendingCallTTL = 10 * time.Minute
	// maxConfirmArgumentChars bounds the arguments a confirmation card shows. A
	// write whose arguments are longer is refused, never shown cut short.
	maxConfirmArgumentChars = 1500
)

// ErrNoPendingCall is the outcome of a Confirm that matches no pending call: the
// card was already answered, expired, or belongs to another thread.
var ErrNoPendingCall = errors.New("no pending call matches this confirmation")

// ManagementTool is one /api operation Warp can call through the management MCP.
type ManagementTool struct {
	Name        string
	Description string
	InputSchema string // JSON schema of the arguments
	Write       bool   // runs only after the person confirms it
}

// ManagementCatalog is the set of management tools one spec load offers Warp.
// A load builds a new catalog, so the declarations parsed from it are cached on
// it rather than re-parsed every turn.
type ManagementCatalog struct {
	tools    []ManagementTool
	byName   map[string]int
	once     sync.Once
	declared []schemas.ResponsesTool
	err      error
}

// NewManagementCatalog builds a catalog. A tool whose name Warp already uses for
// one of its own tools is dropped: Warp's own tool wins. So is a tool whose
// schema does not parse, which would otherwise fail every turn's declarations.
func NewManagementCatalog(tools []ManagementTool) *ManagementCatalog {
	own := map[string]bool{}
	for _, tool := range buildToolsFor(&SemanticSearcher{}, true) {
		own[tool.name] = true
	}
	catalog := &ManagementCatalog{byName: make(map[string]int, len(tools))}
	for _, tool := range tools {
		if own[tool.Name] || tool.Name == "" {
			continue
		}
		if _, dup := catalog.byName[tool.Name]; dup {
			continue
		}
		var parameters schemas.ToolFunctionParameters
		if err := sonic.UnmarshalString(tool.InputSchema, &parameters); err != nil {
			continue
		}
		catalog.byName[tool.Name] = len(catalog.tools)
		catalog.tools = append(catalog.tools, tool)
	}
	return catalog
}

// Len is how many tools the catalog offers.
func (c *ManagementCatalog) Len() int {
	if c == nil {
		return 0
	}
	return len(c.tools)
}

// Tool returns the tool with this name.
func (c *ManagementCatalog) Tool(name string) (ManagementTool, bool) {
	if c == nil {
		return ManagementTool{}, false
	}
	i, ok := c.byName[name]
	if !ok {
		return ManagementTool{}, false
	}
	return c.tools[i], true
}

// hasWrites reports whether any tool in the catalog is a write.
func (c *ManagementCatalog) hasWrites() bool {
	if c == nil {
		return false
	}
	for _, tool := range c.tools {
		if tool.Write {
			return true
		}
	}
	return false
}

// declarations returns the model-facing declarations, parsed once per catalog.
func (c *ManagementCatalog) declarations() ([]schemas.ResponsesTool, error) {
	if c == nil {
		return nil, nil
	}
	c.once.Do(func() {
		c.declared = make([]schemas.ResponsesTool, 0, len(c.tools))
		for _, tool := range c.tools {
			var parameters schemas.ToolFunctionParameters
			if err := sonic.UnmarshalString(tool.InputSchema, &parameters); err != nil {
				c.err = fmt.Errorf("management tool %s has an invalid schema: %w", tool.Name, err)
				return
			}
			description := tool.Description
			if tool.Write {
				description = strings.TrimSpace(description + " Runs only after the person confirms it: calling this shows them a confirmation card and ends your turn.")
			}
			c.declared = append(c.declared, schemas.ResponsesTool{
				Type:                  schemas.ResponsesToolTypeFunction,
				Name:                  new(tool.Name),
				Description:           new(description),
				ResponsesToolFunction: &schemas.ResponsesToolFunction{Parameters: &parameters},
			})
		}
	})
	return c.declared, c.err
}

// ManagementTools is the gateway's management MCP as Warp reaches it.
type ManagementTools interface {
	// Catalog is the tools Warp may call, nil until the gateway's spec loads.
	Catalog() *ManagementCatalog
	// Call runs one tool as the caller recorded on ctx. failed reports a call
	// /api refused or could not serve; result then says why.
	Call(ctx context.Context, name string, arguments map[string]any) (result string, failed bool, err error)
}

// confirmationQuestion builds the card a write ends the turn with. The token is
// random and never shown to the model, so only the person's click can send it.
// The card shows the arguments whole, so what the person confirms is all of what
// runs.
func confirmationQuestion(tool ManagementTool, arguments string) (*Question, error) {
	shown := strings.TrimSpace(arguments)
	if shown == "" {
		shown = "{}"
	}
	if n := utf8.RuneCountInString(shown); n > maxConfirmArgumentChars {
		return nil, fmt.Errorf("the arguments are too long to show on a confirmation card (%d characters, limit %d). Ask the person to make this change in the dashboard", n, maxConfirmArgumentChars)
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("generating confirmation token: %w", err)
	}
	token := hex.EncodeToString(raw)
	return &Question{
		Question: fmt.Sprintf("Run %s with %s?", tool.Name, shown),
		Options: []QuestionOpt{
			{Label: "Confirm", Hint: confirmHintPrefix + token},
			{Label: "Cancel", Hint: cancelHint},
		},
		Kind:    ConfirmQuestionKind,
		pending: &schemas.WarpPendingCall{Token: token, Tool: tool.Name, Arguments: arguments},
	}, nil
}

// confirmationToken returns the token a Confirm answer carries, or "".
func confirmationToken(content string) string {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, confirmHintPrefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(content, confirmHintPrefix))
}

// pendingCard is a confirmation card with an unclaimed call: the call, and the
// stored message it is filed on, as read, which a claim swaps against.
type pendingCard struct {
	call         *schemas.WarpPendingCall
	messageID    string
	questionJSON string
	question     schemas.WarpStoredQuestion
}

// pendingCallFor finds the call a Confirm answers. It must be on the newest
// message of the caller's own thread, so a card answered once, or overtaken by
// a later turn, cannot be confirmed again, younger than PendingCallTTL, and not
// yet claimed (see claimPendingCall).
func pendingCallFor(ctx context.Context, store logstore.WarpConversationStore, ownerID, conversationID, token string, now time.Time) (*pendingCard, error) {
	if store == nil || token == "" || conversationID == "" {
		return nil, ErrNoPendingCall
	}
	row, err := store.GetWarpConversation(ctx, ownerID, conversationID)
	if errors.Is(err, logstore.ErrWarpConversationNotFound) {
		return nil, ErrNoPendingCall
	}
	if err != nil {
		return nil, err
	}
	if len(row.Messages) == 0 {
		return nil, ErrNoPendingCall
	}
	last := row.Messages[len(row.Messages)-1]
	if last.Role != "assistant" || last.QuestionJSON == "" || now.Sub(last.CreatedAt) > PendingCallTTL {
		return nil, ErrNoPendingCall
	}
	var question schemas.WarpStoredQuestion
	if err := sonic.UnmarshalString(last.QuestionJSON, &question); err != nil {
		return nil, ErrNoPendingCall
	}
	if question.Kind != ConfirmQuestionKind || question.Pending == nil || question.Pending.Token != token {
		return nil, ErrNoPendingCall
	}
	return &pendingCard{call: question.Pending, messageID: last.ID, questionJSON: last.QuestionJSON, question: question}, nil
}

// claimPendingCall consumes a card in the conversation store by filing it again
// without its pending call, swapped against the card as read. It reports false
// when another Confirm, on any instance or before a restart, claimed it first.
// The call runs only after this returns true, so a card runs its call at most
// once whatever happens to the process.
func claimPendingCall(ctx context.Context, store logstore.WarpConversationStore, ownerID, conversationID string, card *pendingCard) (bool, error) {
	claimed := card.question
	claimed.Pending = nil
	replacement, err := sonic.MarshalString(claimed)
	if err != nil {
		return false, fmt.Errorf("encoding the claimed card: %w", err)
	}
	return store.SwapWarpQuestion(ctx, ownerID, conversationID, card.messageID, card.questionJSON, replacement)
}

// confirmedCallMessages are the function_call and function_call_output items a
// confirmed write adds to the conversation, so the model reads its result as a
// tool result, the same as any call it made itself.
func confirmedCallMessages(callID string, call *schemas.WarpPendingCall, result string) []schemas.ResponsesMessage {
	itemType := schemas.ResponsesMessageTypeFunctionCall
	name, arguments := call.Tool, call.Arguments
	return []schemas.ResponsesMessage{
		{
			Type: &itemType,
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: &callID, Name: &name, Arguments: &arguments,
			},
		},
		toolResultMessage(callID, result),
	}
}

// boundManagementResult enforces the tool result budget on a management call's
// response, which is already text.
func boundManagementResult(result string) string {
	if len(result) <= MaxToolResultBytes {
		return result
	}
	return fmt.Sprintf(`{"error":"result too large (%d bytes, limit %d). Ask for fewer rows, for example with a limit or a filter, then try again.","truncated":true}`,
		len(result), MaxToolResultBytes)
}

// confirmTurn resolves the person's Confirm and returns the conversation the
// model sees. The click becomes plain words, and a matching pending call is
// handed to the agent, which runs it before its first step.
func (s *Service) confirmTurn(ctx context.Context, turn *Turn, agent *Agent) []schemas.ResponsesMessage {
	text := "I pressed Confirm, but changes from Warp are not available right now, so nothing was run."
	if agent.management != nil {
		owner := OwnerFromContext(ctx)
		card, err := pendingCallFor(ctx, s.conversations, owner, turn.ConversationID, turn.confirmToken, time.Now())
		switch {
		case err == nil && !s.claimConfirmation(card.call.Token, time.Now()):
			text = "I pressed Confirm again, but that change was already confirmed, so it was not run a second time."
		case err == nil:
			tool, ok := agent.managementCatalog.Tool(card.call.Tool)
			if !ok || !tool.Write {
				text = "I pressed Confirm, but that action is no longer available, so nothing was run."
				break
			}
			claimed, claimErr := claimPendingCall(ctx, s.conversations, owner, turn.ConversationID, card)
			switch {
			case claimErr != nil:
				// Nothing ran and nothing was claimed, so the card stays good for another try.
				s.releaseConfirmation(card.call.Token)
				s.warnf("failed to claim the pending warp change: %v", claimErr)
				text = "I pressed Confirm, but the confirmation could not be checked, so nothing was run."
			case !claimed:
				text = "I pressed Confirm again, but that change was already confirmed, so it was not run a second time."
			default:
				agent.confirmed = card.call
				text = "Confirmed."
			}
		case errors.Is(err, ErrNoPendingCall):
			text = "I pressed Confirm, but that confirmation had already been answered or had expired, so nothing was run."
		default:
			s.warnf("failed to read the pending warp change: %v", err)
			text = "I pressed Confirm, but the confirmation could not be checked, so nothing was run."
		}
	}
	return withFinalText(turn.messages, text)
}

// claimConfirmation records that a token has been used on this instance, and
// reports false if it already was. It only spares a second click on the same
// instance a store round trip: the claim that decides is claimPendingCall's, in
// the conversation store. Entries are dropped once older than PendingCallTTL,
// when the card has expired anyway, so the set holds at most the confirmations
// of the last few minutes.
func (s *Service) claimConfirmation(token string, now time.Time) bool {
	s.usedConfirmations.Range(func(key, value any) bool {
		if used, _ := value.(time.Time); now.Sub(used) > PendingCallTTL {
			s.usedConfirmations.Delete(key)
		}
		return true
	})
	_, used := s.usedConfirmations.LoadOrStore(token, now)
	return !used
}

// releaseConfirmation forgets a token whose durable claim failed, so the person
// can press Confirm again.
func (s *Service) releaseConfirmation(token string) {
	s.usedConfirmations.Delete(token)
}

// withFinalText returns messages with the last one's text replaced, leaving the
// caller's slice untouched.
func withFinalText(messages []schemas.ResponsesMessage, text string) []schemas.ResponsesMessage {
	if len(messages) == 0 {
		return messages
	}
	out := slices.Clone(messages)
	last := out[len(out)-1]
	last.Content = &schemas.ResponsesMessageContent{ContentStr: &text}
	out[len(out)-1] = last
	return out
}

// withoutConfirmationTokens returns the history with every Confirm the person
// pressed written as "Confirm". The client replays what it sent, the token, and
// the model has no use for it.
func withoutConfirmationTokens(messages []ChatMessage) []ChatMessage {
	var out []ChatMessage
	for i, message := range messages {
		if message.Role != "user" || confirmationToken(message.Content) == "" {
			continue
		}
		if out == nil {
			out = slices.Clone(messages)
		}
		out[i].Content = "Confirm"
	}
	if out == nil {
		return messages
	}
	return out
}
