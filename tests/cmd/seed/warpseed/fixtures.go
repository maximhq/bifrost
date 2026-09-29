package main

import (
	"math/rand"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

const (
	successCount  = 450
	incidentCount = 30
	incidentModel = "claude-3-5-sonnet-20241022"
)

// windowDays bounds how far back success/scattered-failure timestamps are
// spread; set from the -days flag. The incident cluster's own offset is
// separately clamped to stay inside this window (see main.go).
var windowDays = 7

type user struct {
	id, name string
}

type team struct {
	id, name string
}

type customer struct {
	id, name string
}

var users = []user{
	{"user-alex-rivera", "Alex Rivera"},
	{"user-priya-nair", "Priya Nair"},
	{"user-jamal-owens", "Jamal Owens"},
	{"user-mei-chen", "Mei Chen"},
	{"user-sofia-castro", "Sofia Castro"},
	{"user-tom-becker", "Tom Becker"},
	{"user-nina-petrov", "Nina Petrov"},
	{"user-devon-clark", "Devon Clark"},
	{"user-ravi-kumar", "Ravi Kumar"},
	{"user-lena-hoffman", "Lena Hoffman"},
}

var teams = []team{
	{"team-support", "Support"},
	{"team-platform", "Platform Engineering"},
	{"team-growth", "Growth"},
}

var customers = []customer{
	{"customer-acme", "Acme Corp"},
	{"customer-globex", "Globex"},
	{"customer-initech", "Initech"},
}

var apps = []string{
	"support-widget", "internal-copilot", "docs-search-bot",
	"sales-assistant", "qa-automation", "billing-bot",
}

// The splits below are deliberately lopsided. A uniform split gives "which team
// uses the most tokens" or "top 5 users" an answer that changes with the rng
// seed, so an e2e check could only assert that some name came back. With these
// weights the leader is the same on every seed: Platform Engineering, Alex
// Rivera, support-widget and Acme Corp each hold a margin no 500-row draw closes.
var (
	userWeights     = []int{30, 20, 14, 10, 8, 6, 5, 3, 2, 2} // parallel to users
	teamWeights     = []int{30, 50, 20}                       // Support, Platform Engineering, Growth
	customerWeights = []int{50, 30, 20}                       // Acme Corp, Globex, Initech
	appWeights      = []int{40, 20, 15, 12, 8, 5}             // parallel to apps
)

type providerKey struct {
	provider string
	keyID    string
	keyName  string
	models   []string
}

var openaiKey = providerKey{
	provider: "openai",
	keyID:    "4181b1ce-34ee-4522-98dc-7666a25961cf",
	keyName:  "test-key",
	models:   []string{"gpt-4o", "gpt-4o-mini", "gpt-4.1", "gpt-4.1-mini"},
}

var anthropicKey = providerKey{
	provider: "anthropic",
	keyID:    "1fe24e49-7f72-4ba3-b2db-22ac9e6c0b0b",
	keyName:  "keyyy",
	models:   []string{"claude-3-5-sonnet-20241022", "claude-3-5-haiku-20241022", "claude-3-opus-20240229"},
}

// per-1M-token pricing in USD, approximate public list prices.
var modelPricing = map[string][2]float64{
	"gpt-4o":                     {2.50, 10.00},
	"gpt-4o-mini":                {0.15, 0.60},
	"gpt-4.1":                    {2.00, 8.00},
	"gpt-4.1-mini":               {0.40, 1.60},
	"claude-3-5-sonnet-20241022": {3.00, 15.00},
	"claude-3-5-haiku-20241022":  {0.80, 4.00},
	"claude-3-opus-20240229":     {15.00, 75.00},
}

// weightedIndex draws an index with probability proportional to its weight.
func weightedIndex(r *rand.Rand, weights []int) int {
	total := 0
	for _, w := range weights {
		total += w
	}
	n := r.Intn(total)
	for i, w := range weights {
		if n < w {
			return i
		}
		n -= w
	}
	return len(weights) - 1
}

func pickUser(r *rand.Rand) user         { return users[weightedIndex(r, userWeights)] }
func pickTeam(r *rand.Rand) team         { return teams[weightedIndex(r, teamWeights)] }
func pickCustomer(r *rand.Rand) customer { return customers[weightedIndex(r, customerWeights)] }
func pickApp(r *rand.Rand) string        { return apps[weightedIndex(r, appWeights)] }

func pickProviderModel(r *rand.Rand) (providerKey, string) {
	if r.Intn(2) == 0 {
		return openaiKey, openaiKey.models[r.Intn(len(openaiKey.models))]
	}
	return anthropicKey, anthropicKey.models[r.Intn(len(anthropicKey.models))]
}

func costFor(model string, promptTokens, completionTokens int) *schemas.BifrostCost {
	prices, ok := modelPricing[model]
	if !ok {
		prices = [2]float64{1.0, 3.0}
	}
	input := float64(promptTokens) / 1_000_000 * prices[0]
	output := float64(completionTokens) / 1_000_000 * prices[1]
	return &schemas.BifrostCost{
		InputCost:  input,
		OutputCost: output,
		TotalCost:  input + output,
	}
}

func msg(role schemas.ChatMessageRole, text string) schemas.ChatMessage {
	t := text
	return schemas.ChatMessage{Role: role, Content: &schemas.ChatMessageContent{ContentStr: &t}}
}

func strPtr(s string) *string { return &s }

// baseLog fills the fields every seeded row shares, leaving content/status to the caller.
func baseLog(id string, ts time.Time, pk providerKey, model string, u user, tm team, c customer, app string) logstore.Log {
	// Object is the request type, as the gateway's logging plugin writes it - not
	// the OpenAI response object "chat.completion". Seeded under that spelling,
	// every row was invisible to the objects filter the tools document, so a
	// question scoped to chat traffic came back as "no records".
	return logstore.Log{
		ID:              id,
		Timestamp:       ts,
		Object:          string(schemas.ChatCompletionRequest),
		Provider:        pk.provider,
		Model:           model,
		SelectedKeyID:   pk.keyID,
		SelectedKeyName: pk.keyName,
		UserID:          strPtr(u.id),
		UserName:        strPtr(u.name),
		TeamID:          strPtr(tm.id),
		TeamName:        strPtr(tm.name),
		CustomerID:      strPtr(c.id),
		CustomerName:    strPtr(c.name),
		App:             strPtr(app),
		UserAgent:       strPtr(app + "/1.0"),
		CreatedAt:       ts,
		Stream:          false,
	}
}

func newID() string { return uuid.NewString() }
