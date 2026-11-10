package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/evals"
)

// specCacheSize bounds how many decoded runs stay in memory. Only runs with work in
// flight are read, so this is far more than an instance has active at once.
const specCacheSize = 256

// errBadRun marks a run whose copied target or evaluators cannot be decoded. Reading
// it again will not help, so a job of such a run is dropped.
var errBadRun = errors.New("the run cannot be read")

// runSpec is what a run copied when it started, decoded once: its target and its
// evaluators. Every job of a run needs them, and the row they are stored on grows
// with the prompts the user wrote, so jobs share one decoded copy and read only the
// run's status from the store. It is never changed after it is built.
type runSpec struct {
	target      Target
	definitions []evals.Definition
	// prompt is the conversation of a prompt or model target with its placeholders
	// found, or promptErr when a message cannot be sent.
	prompt    *compiledPrompt
	promptErr error
	// httpTarget is the HTTP target as the API evaluator the engine calls. It has an
	// identity, so the engine builds its request once for the run and holds the
	// target's concurrency across every row.
	httpTarget evals.Definition
}

// definition returns the run's evaluator with this ID, or nil.
func (sp *runSpec) definition(evaluatorID string) *evals.Definition {
	for i := range sp.definitions {
		if sp.definitions[i].ID == evaluatorID {
			return &sp.definitions[i]
		}
	}
	return nil
}

// specOf decodes a run's target and evaluators.
func specOf(run *tables.TableEvalRun) (*runSpec, error) {
	target, err := targetOf(run)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errBadRun, err)
	}
	definitions, err := definitionsOf(run)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errBadRun, err)
	}
	spec := &runSpec{target: target, definitions: definitions}
	switch target.Type {
	case TargetPrompt, TargetModel:
		spec.prompt, spec.promptErr = compilePrompt(target)
	case TargetHTTP:
		spec.httpTarget = evals.Definition{ID: "runtarget:" + run.ID, Version: 1, Type: evals.EvaluatorTypeAPI, Config: target.HTTP}
	}
	return spec, nil
}

// spec returns a run's decoded target and evaluators. updatedAt is the run's own
// timestamp, read with its status: adding evaluators to a run moves it, and the next
// job then decodes the run again.
func (s *Service) spec(ctx context.Context, runID string, updatedAt time.Time) (*runSpec, error) {
	key := runID + "@" + strconv.FormatInt(updatedAt.UnixNano(), 36)
	if cached, ok := s.specs.Get(key); ok {
		return cached, nil
	}
	return s.specs.Fill(ctx, key, func() (*runSpec, string, error) {
		run, err := s.store.GetEvalRun(ctx, runID)
		if err != nil {
			return nil, "", err
		}
		spec, err := specOf(run)
		return spec, "", err
	})
}

// ============================================================================
// Messages for prompt and model targets
// ============================================================================

// compiledText is a text with its {{name}} placeholders found: the fixed pieces, and
// the variable between each two of them.
type compiledText struct {
	literals []string // len(names)+1 pieces of fixed text
	names    []string
}

func compileText(text string) compiledText {
	if !strings.Contains(text, "{{") {
		return compiledText{literals: []string{text}}
	}
	matches := templateVariable.FindAllStringSubmatchIndex(text, -1)
	compiled := compiledText{literals: make([]string, 0, len(matches)+1), names: make([]string, 0, len(matches))}
	last := 0
	for _, match := range matches {
		compiled.literals = append(compiled.literals, text[last:match[0]])
		compiled.names = append(compiled.names, text[match[2]:match[3]])
		last = match[1]
	}
	compiled.literals = append(compiled.literals, text[last:])
	return compiled
}

// render fills the placeholders from the variables. A name the row does not have
// becomes empty text.
func (t compiledText) render(variables map[string]string) string {
	if len(t.names) == 0 {
		return t.literals[0]
	}
	size := 0
	for _, literal := range t.literals {
		size += len(literal)
	}
	for _, name := range t.names {
		size += len(variables[name])
	}
	var b strings.Builder
	b.Grow(size)
	for i, literal := range t.literals {
		b.WriteString(literal)
		if i < len(t.names) {
			b.WriteString(variables[t.names[i]])
		}
	}
	return b.String()
}

func (t compiledText) uses(name string) bool {
	for _, used := range t.names {
		if used == name {
			return true
		}
	}
	return false
}

// promptMessage is one message of a prompt version: its stored form, and the
// placeholders of each text in it.
type promptMessage struct {
	raw    json.RawMessage
	text   *compiledText   // the message's text content, when it is a plain string
	blocks []*compiledText // one per content block; nil where a block has no text
}

// compiledPrompt is the conversation a prompt or model target sends, worked out once
// for the run. Each row only fills in its variables.
type compiledPrompt struct {
	messages    []promptMessage
	system      string // the system message of a model target
	usesInput   bool   // a message reads {{input}}, so the input is not added again
	usesHistory bool
}

// compilePrompt checks a target's messages and finds their placeholders.
func compilePrompt(target Target) (*compiledPrompt, error) {
	prompt := &compiledPrompt{messages: make([]promptMessage, 0, len(target.Messages))}
	note := func(text string) *compiledText {
		compiled := compileText(text)
		prompt.usesInput = prompt.usesInput || compiled.uses(VariableInput)
		prompt.usesHistory = prompt.usesHistory || compiled.uses(VariableHistory)
		return &compiled
	}
	for _, raw := range target.Messages {
		var message schemas.ChatMessage
		if err := json.Unmarshal(raw, &message); err != nil || message.Role == "" {
			return nil, errors.New("a message of the prompt version is not in a form the run can send")
		}
		compiled := promptMessage{raw: raw}
		if message.Content != nil {
			if message.Content.ContentStr != nil {
				compiled.text = note(*message.Content.ContentStr)
			}
			if len(message.Content.ContentBlocks) > 0 {
				compiled.blocks = make([]*compiledText, len(message.Content.ContentBlocks))
				for i := range message.Content.ContentBlocks {
					if text := message.Content.ContentBlocks[i].Text; text != nil {
						compiled.blocks[i] = note(*text)
					}
				}
			}
		}
		prompt.messages = append(prompt.messages, compiled)
	}
	if target.Type == TargetModel {
		prompt.system = target.System
	}
	return prompt, nil
}

// render builds the conversation sent to the model for one row: the prompt's
// messages with their variables filled in (or the system message of a model target),
// then the row's conversation history, then the row's input as the user message. The
// input is left out when the prompt already reads {{input}}.
//
// Each message is decoded again from its stored form, so a row's request shares
// nothing with another row's that a provider could change under it.
func (p *compiledPrompt) render(variables map[string]string) ([]schemas.ChatMessage, error) {
	messages := make([]schemas.ChatMessage, 0, len(p.messages)+2)
	for i := range p.messages {
		compiled := &p.messages[i]
		var message schemas.ChatMessage
		if err := json.Unmarshal(compiled.raw, &message); err != nil {
			return nil, errors.New("a message of the prompt version is not in a form the run can send")
		}
		if message.Content != nil {
			if compiled.text != nil && message.Content.ContentStr != nil {
				message.Content.ContentStr = new(compiled.text.render(variables))
			}
			for j := range message.Content.ContentBlocks {
				if j < len(compiled.blocks) && compiled.blocks[j] != nil && message.Content.ContentBlocks[j].Text != nil {
					message.Content.ContentBlocks[j].Text = new(compiled.blocks[j].render(variables))
				}
			}
		}
		messages = append(messages, message)
	}
	if p.system != "" {
		messages = append(messages, textMessage(schemas.ChatMessageRoleSystem, p.system))
	}
	if history := variables[VariableHistory]; history != "" && !p.usesHistory {
		var turns []schemas.ChatMessage
		if err := json.Unmarshal([]byte(history), &turns); err == nil {
			messages = append(messages, turns...)
		}
	}
	if !p.usesInput {
		if variables[VariableInput] == "" && len(messages) == 0 {
			return nil, errors.New("the row has no input to send")
		}
		if variables[VariableInput] != "" {
			messages = append(messages, textMessage(schemas.ChatMessageRoleUser, variables[VariableInput]))
		}
	}
	if len(messages) == 0 {
		return nil, errors.New("there is nothing to send to the model for this row")
	}
	return messages, nil
}
