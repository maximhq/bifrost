//go:build !tinygo && !wasm

package starlark

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	codemcp "github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

// fakeDecisions answers decision requests with answer and records each request.
type fakeDecisions struct {
	mu       sync.Mutex
	requests []*schemas.BifrostDecisionRequest
	answer   func(ctx *schemas.BifrostContext, req *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError)
}

func (f *fakeDecisions) decide(ctx *schemas.BifrostContext, req *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	return f.answer(ctx, req)
}

func (f *fakeDecisions) calls() []*schemas.BifrostDecisionRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*schemas.BifrostDecisionRequest(nil), f.requests...)
}

// decisionAnswers builds a response with a choice distribution over ids and,
// when sufficient is set, a predicate answer.
func decisionAnswers(probabilities map[string]float64, sufficient *float64) *schemas.BifrostDecisionResponse {
	choice := schemas.DecisionAnswer{Type: schemas.DecisionTypeChoice, Name: schemas.Ptr(discoveryDecisionRelevantQuestion)}
	best := -1.0
	for id, p := range probabilities {
		choice.Probabilities = append(choice.Probabilities, schemas.DecisionProbability{Value: schemas.DecisionScalar{Str: schemas.Ptr(id)}, Probability: p})
		if p > best {
			best, choice.Choice = p, &schemas.DecisionScalar{Str: schemas.Ptr(id)}
		}
	}
	resp := &schemas.BifrostDecisionResponse{Model: "ranker", Answers: []schemas.DecisionAnswer{choice}}
	if sufficient != nil {
		resp.Answers = append(resp.Answers, schemas.DecisionAnswer{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr(discoveryDecisionSufficientQuestion), Probability: sufficient})
	}
	return resp
}

// useDecisionRanker installs a decision-model ranker answered by fake.
func useDecisionRanker(t *testing.T, mode *StarlarkCodeMode, config schemas.MCPCodeModeDiscoveryRanker, fake *fakeDecisions) *decisionDiscoveryRanker {
	t.Helper()
	mode.UpdateConfig(&codemcp.CodeModeConfig{DiscoveryRanker: &config})
	ref := mode.discoveryRanker.Load()
	if ref == nil {
		t.Fatalf("ranker %+v was not installed", config)
	}
	ranker, ok := (*ref).(*decisionDiscoveryRanker)
	if !ok {
		t.Fatalf("installed ranker is %T, not the decision ranker", *ref)
	}
	if fake != nil {
		mode.SetDecisionRequestFunc(fake.decide)
	}
	return ranker
}

func TestListToolFilesDecisionRanker(t *testing.T) {
	fake := &fakeDecisions{answer: func(ctx *schemas.BifrostContext, _ *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("decision request has no deadline")
		}
		return decisionAnswers(map[string]float64{
			"servers/slack/send_message.pyi":    0.7,
			"servers/slack/search_messages.pyi": 0.2,
			"servers/github/create_issue.pyi":   0.1,
			"servers/github/search_repos.pyi":   0,
		}, schemas.Ptr(0.9)), nil
	}}
	mode := newDiscoveryCodeMode(schemas.CodeModeBindingLevelTool)
	useDecisionRanker(t, mode, schemas.MCPCodeModeDiscoveryRanker{
		DecisionModel: "typesafe/system-one",
		Fallbacks:     []string{"openai/gpt-5-nano", "my-custom/org/model"},
	}, fake)

	out := listToolFiles(t, mode, `{"query":"tell the team the deploy finished"}`)
	paths := shortlistPaths(out)
	want := []string{"servers/slack/send_message.pyi", "servers/slack/search_messages.pyi", "servers/github/create_issue.pyi"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("expected probability order %v, got %v\n%s", want, paths, out)
	}
	if !strings.Contains(out, "(discovery ranker)") || !strings.Contains(out, "relevance=0.70") || strings.Contains(out, "may not cover") {
		t.Fatalf("unexpected ranked output:\n%s", out)
	}

	calls := fake.calls()
	if len(calls) != 1 {
		t.Fatalf("expected one decision request, got %d", len(calls))
	}
	req := calls[0]
	if req.Provider != "typesafe" || req.Model != "system-one" {
		t.Fatalf("unexpected model %s/%s", req.Provider, req.Model)
	}
	wantFallbacks := []schemas.Fallback{{Provider: "openai", Model: "gpt-5-nano"}, {Provider: "my-custom", Model: "org/model"}}
	if len(req.Fallbacks) != len(wantFallbacks) || req.Fallbacks[0] != wantFallbacks[0] || req.Fallbacks[1] != wantFallbacks[1] {
		t.Fatalf("unexpected fallbacks %+v", req.Fallbacks)
	}
	if req.Input.Text == nil || *req.Input.Text != "tell the team the deploy finished" {
		t.Fatalf("the query must be the decision input, got %+v", req.Input)
	}
	if len(req.Questions) != 2 || req.Questions[0].Type != schemas.DecisionTypeChoice || req.Questions[1].Type != schemas.DecisionTypePredicate {
		t.Fatalf("expected a choice and a predicate question, got %+v", req.Questions)
	}
	// The choices are exactly the tools the caller can list, in candidate order.
	var ids []string
	for _, choice := range req.Questions[0].Choices {
		id, err := choice.Key()
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	catalogue, _ := mode.discoveryCatalogue(t.Context())
	var visible []string
	for _, item := range scoreDiscovery(catalogue, "tell the team the deploy finished") {
		visible = append(visible, item.candidate.ID)
	}
	if strings.Join(ids, ",") != strings.Join(visible, ",") {
		t.Fatalf("choices %v are not the visible candidates %v", ids, visible)
	}
	for _, choice := range req.Questions[0].Choices {
		if key, _ := choice.Key(); key == "servers/github/create_issue.pyi" {
			if text := *choice.Description.Text; text != "github/create_issue: Open a new issue in a repository (parameters: repo, title, body)" {
				t.Fatalf("unexpected choice description %q", text)
			}
		}
	}
}

func TestListToolFilesDecisionRankerInsufficientHint(t *testing.T) {
	fake := &fakeDecisions{answer: func(*schemas.BifrostContext, *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
		return decisionAnswers(map[string]float64{"servers/github/search_repos.pyi": 1}, schemas.Ptr(0.1)), nil
	}}
	mode := newDiscoveryCodeMode(schemas.CodeModeBindingLevelTool)
	useDecisionRanker(t, mode, schemas.MCPCodeModeDiscoveryRanker{DecisionModel: "openai/gpt-5-nano"}, fake)
	out := listToolFiles(t, mode, `{"query":"find code"}`)
	if paths := shortlistPaths(out); len(paths) != 1 || paths[0] != "servers/github/search_repos.pyi" {
		t.Fatalf("unexpected shortlist %v\n%s", paths, out)
	}
	if !strings.Contains(out, "may not cover the request") {
		t.Fatalf("expected a hint to widen the search:\n%s", out)
	}
}

func TestListToolFilesDecisionRankerWithoutProbabilities(t *testing.T) {
	for name, tc := range map[string]struct {
		confidence *float64
		want       string
	}{
		"confidence":    {schemas.Ptr(0.8), "relevance=0.80"},
		"no confidence": {nil, "relevance=1.00"},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeDecisions{answer: func(*schemas.BifrostContext, *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
				return &schemas.BifrostDecisionResponse{Answers: []schemas.DecisionAnswer{{
					Type:       schemas.DecisionTypeChoice,
					Name:       schemas.Ptr(discoveryDecisionRelevantQuestion),
					Choice:     &schemas.DecisionScalar{Str: schemas.Ptr("servers/jira/create_issue.pyi")},
					Confidence: tc.confidence,
				}}}, nil
			}}
			mode := newDiscoveryCodeMode(schemas.CodeModeBindingLevelTool)
			useDecisionRanker(t, mode, schemas.MCPCodeModeDiscoveryRanker{DecisionModel: "openai/gpt-5-nano"}, fake)
			out := listToolFiles(t, mode, `{"query":"open a ticket"}`)
			if paths := shortlistPaths(out); len(paths) != 1 || paths[0] != "servers/jira/create_issue.pyi" {
				t.Fatalf("expected only the chosen tool, got %v\n%s", paths, out)
			}
			if !strings.Contains(out, "(discovery ranker)") || !strings.Contains(out, tc.want) || strings.Contains(out, "may not cover") {
				t.Fatalf("unexpected ranked output:\n%s", out)
			}
		})
	}
}

func TestListToolFilesDecisionRankerFailuresFallBack(t *testing.T) {
	type answerFunc = func(*schemas.BifrostContext, *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError)
	cases := map[string]answerFunc{
		"provider error": func(*schemas.BifrostContext, *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
			return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "boom"}}
		},
		"no response": func(*schemas.BifrostContext, *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
			return nil, nil
		},
		"no choice answer": func(*schemas.BifrostContext, *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
			return &schemas.BifrostDecisionResponse{Answers: []schemas.DecisionAnswer{{Type: schemas.DecisionTypeRefusal, Name: schemas.Ptr(discoveryDecisionRelevantQuestion)}}}, nil
		},
		"unknown choice": func(*schemas.BifrostContext, *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
			return decisionAnswers(map[string]float64{"servers/billing/create_invoice.pyi": 1}, nil), nil
		},
		"probability above 1": func(*schemas.BifrostContext, *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
			return decisionAnswers(map[string]float64{"servers/github/create_issue.pyi": 1.5}, nil), nil
		},
		"sufficiency above 1": func(*schemas.BifrostContext, *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
			return decisionAnswers(map[string]float64{"servers/github/create_issue.pyi": 1}, schemas.Ptr(2.0)), nil
		},
		"nothing relevant": func(*schemas.BifrostContext, *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
			return decisionAnswers(map[string]float64{"servers/github/create_issue.pyi": 0}, nil), nil
		},
	}
	for name, answer := range cases {
		t.Run(name, func(t *testing.T) {
			fake := &fakeDecisions{answer: answer}
			mode := newDiscoveryCodeMode(schemas.CodeModeBindingLevelTool)
			useDecisionRanker(t, mode, schemas.MCPCodeModeDiscoveryRanker{DecisionModel: "openai/gpt-5-nano"}, fake)
			assertLexicalFallback(t, listToolFiles(t, mode, `{"query":"create an issue"}`))
			if len(fake.calls()) != 1 {
				t.Fatalf("expected one decision request, got %d", len(fake.calls()))
			}
		})
	}

	t.Run("decision API not set", func(t *testing.T) {
		mode := newDiscoveryCodeMode(schemas.CodeModeBindingLevelTool)
		useDecisionRanker(t, mode, schemas.MCPCodeModeDiscoveryRanker{DecisionModel: "openai/gpt-5-nano"}, nil)
		assertLexicalFallback(t, listToolFiles(t, mode, `{"query":"create an issue"}`))
	})

	t.Run("timeout", func(t *testing.T) {
		fake := &fakeDecisions{answer: func(ctx *schemas.BifrostContext, _ *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
			<-ctx.Done()
			return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: ctx.Err().Error()}}
		}}
		mode := newDiscoveryCodeMode(schemas.CodeModeBindingLevelTool)
		useDecisionRanker(t, mode, schemas.MCPCodeModeDiscoveryRanker{DecisionModel: "openai/gpt-5-nano"}, fake).timeout = 50 * time.Millisecond
		start := time.Now()
		out := listToolFiles(t, mode, `{"query":"create an issue"}`)
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("ranking was not bounded by its timeout: %v", elapsed)
		}
		assertLexicalFallback(t, out)
	})
}

func TestListToolFilesDecisionRankerCapsChoices(t *testing.T) {
	mode := newDiscoveryCodeMode(schemas.CodeModeBindingLevelTool)
	manager := mode.clientManager.(*testClientManager)
	for i := range 40 {
		manager.tools["github"] = append(manager.tools["github"], discoveryTestTool(fmt.Sprintf("report_%02d", i), "Build a report"))
	}
	fake := &fakeDecisions{answer: func(_ *schemas.BifrostContext, req *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
		id, _ := req.Questions[0].Choices[0].Key()
		return decisionAnswers(map[string]float64{id: 1}, nil), nil
	}}
	useDecisionRanker(t, mode, schemas.MCPCodeModeDiscoveryRanker{DecisionModel: "openai/gpt-5-nano"}, fake)
	out := listToolFiles(t, mode, `{"query":"report"}`)
	if !strings.Contains(out, "26 of 46 tools visible to this caller were ranked") {
		t.Fatalf("unexpected header:\n%s", out)
	}
	calls := fake.calls()
	if len(calls) != 1 || len(calls[0].Questions[0].Choices) != discoveryMaxDecisionChoices {
		t.Fatalf("expected %d choices", discoveryMaxDecisionChoices)
	}
	servers := map[string]bool{}
	for _, choice := range calls[0].Questions[0].Choices {
		id, _ := choice.Key()
		servers[strings.Split(id, "/")[1]] = true
	}
	if !servers["slack"] || !servers["jira"] {
		t.Fatalf("every visible server must stay a candidate, got %v", servers)
	}
}

func TestDecisionRankerConfigUpdates(t *testing.T) {
	mode := newDiscoveryCodeMode(schemas.CodeModeBindingLevelTool)
	ranker := useDecisionRanker(t, mode, schemas.MCPCodeModeDiscoveryRanker{DecisionModel: "openai/gpt-5-nano", Fallbacks: []string{"anthropic/claude-haiku"}, TimeoutSeconds: 3}, nil)
	if ranker.provider != "openai" || ranker.model != "gpt-5-nano" || ranker.timeout != 3*time.Second || len(ranker.fallbacks) != 1 {
		t.Fatalf("unexpected ranker %+v", ranker)
	}
	// Both a URL and a decision model is invalid and keeps the current ranker.
	mode.UpdateConfig(&codemcp.CodeModeConfig{DiscoveryRanker: &schemas.MCPCodeModeDiscoveryRanker{URL: "http://127.0.0.1:1/rank", DecisionModel: "openai/gpt-5-nano"}})
	if ref := mode.discoveryRanker.Load(); ref == nil || *ref != toolRanker(ranker) {
		t.Fatal("an invalid update replaced the ranker")
	}
	useRanker(t, mode, schemas.MCPCodeModeDiscoveryRanker{URL: "http://127.0.0.1:1/rank"})
	httpRankerOf(t, mode)
	mode.UpdateConfig(&codemcp.CodeModeConfig{DiscoveryRanker: &schemas.MCPCodeModeDiscoveryRanker{}})
	if mode.discoveryRanker.Load() != nil {
		t.Fatal("an empty config must remove the ranker")
	}
}

func TestListToolFilesDecisionRankerRecordsCalls(t *testing.T) {
	served := schemas.RoutingInfo{Provider: "openai", Model: "gpt-5-nano"}
	usage := &schemas.BifrostLLMUsage{PromptTokens: 412, CompletionTokens: 9, TotalTokens: 421}
	failure := &schemas.BifrostError{Error: &schemas.ErrorField{Message: "boom"}, ExtraFields: schemas.BifrostErrorExtraFields{RoutingInfo: served, BilledUsage: usage}}
	for name, tc := range map[string]struct {
		answer func(*schemas.BifrostContext, *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError)
		want   schemas.MCPDiscoveryRankerCall
	}{
		// A fallback answered, so the row names it rather than the configured model.
		"answered by a fallback": {
			answer: func(*schemas.BifrostContext, *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
				resp := decisionAnswers(map[string]float64{"servers/github/create_issue.pyi": 1}, nil)
				resp.Usage, resp.ExtraFields.RoutingInfo = usage, served
				return resp, nil
			},
			want: schemas.MCPDiscoveryRankerCall{Provider: "openai", Model: "gpt-5-nano", Usage: usage},
		},
		// A failed call is still recorded, with what the provider billed for it.
		"failed after billing": {
			answer: func(*schemas.BifrostContext, *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
				return nil, failure
			},
			want: schemas.MCPDiscoveryRankerCall{Provider: "openai", Model: "gpt-5-nano", Usage: usage, Error: failure},
		},
		// Nothing says who served it, so the configured model is recorded.
		"no routing info": {
			answer: func(*schemas.BifrostContext, *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
				return decisionAnswers(map[string]float64{"servers/github/create_issue.pyi": 1}, nil), nil
			},
			want: schemas.MCPDiscoveryRankerCall{Provider: "typesafe", Model: "system-one"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			mode := newDiscoveryCodeMode(schemas.CodeModeBindingLevelTool)
			useDecisionRanker(t, mode, schemas.MCPCodeModeDiscoveryRanker{DecisionModel: "typesafe/system-one", Fallbacks: []string{"openai/gpt-5-nano"}}, &fakeDecisions{answer: tc.answer})
			ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
			if _, err := mode.handleListToolFiles(ctx, schemas.ChatAssistantMessageToolCall{
				ID:       schemas.Ptr("call-1"),
				Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr(codemcp.ToolTypeListToolFiles), Arguments: `{"query":"create an issue"}`},
			}); err != nil {
				t.Fatal(err)
			}
			calls := schemas.TakeMCPDiscoveryRankerCallsFromContext(ctx)
			if len(calls) != 1 {
				t.Fatalf("expected one recorded decision call, got %d", len(calls))
			}
			got := calls[0]
			if got.Provider != tc.want.Provider || got.Model != tc.want.Model || got.Usage != tc.want.Usage || got.Error != tc.want.Error {
				t.Fatalf("recorded %+v, want %+v", got, tc.want)
			}
			if got.StartedAt.IsZero() || got.Latency <= 0 {
				t.Fatalf("the call's start and latency were not recorded: %+v", got)
			}
			if again := schemas.TakeMCPDiscoveryRankerCallsFromContext(ctx); len(again) != 0 {
				t.Fatalf("taking the calls must clear them, got %d more", len(again))
			}
		})
	}
}
