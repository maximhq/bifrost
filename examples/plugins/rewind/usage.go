package main

import (
	"encoding/json"
	"strings"
)

// List prices per million tokens, used to put a dollar figure on each step.
// Free-tier calls are priced at list so runs can be compared.
var prices = map[string][2]float64{
	"gemini-3.5-flash-lite": {0.10, 0.40},
	"gemini-3.5-flash":      {0.30, 2.50},
	"gemini-2.5-flash-lite": {0.10, 0.40},
	"gemini-2.5-flash":      {0.30, 2.50},
	"gemini-2.5-pro":        {1.25, 10.00},
	"llama-3.3-70b":         {0.59, 0.79},
}

// One basic Tavily search is one credit; pay-as-you-go is $0.008 a credit.
const tavilySearchUSD = 0.008

func fillUsage(s *Step, body []byte) {
	if s.Kind == "relay" {
		if s.Status >= 200 && s.Status < 300 {
			s.CostUSD = tavilySearchUSD
		}
		return
	}
	var r struct {
		Model string `json:"model"`
		Usage struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
			Input      int `json:"input_tokens"`
			Output     int `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &r) != nil {
		return
	}
	s.Tokens = Tokens{Prompt: r.Usage.Prompt + r.Usage.Input, Completion: r.Usage.Completion + r.Usage.Output}
	if r.Model != "" {
		s.ServedModel = r.Model
	}
	s.CostUSD = priceFor(s.ServedModel, s.Model, s.Tokens)
}

func priceFor(served, requested string, t Tokens) float64 {
	name := served
	if name == "" {
		name = requested
	}
	best := ""
	for k := range prices {
		if strings.Contains(name, k) && len(k) > len(best) {
			best = k
		}
	}
	if best == "" {
		return 0
	}
	p := prices[best]
	return (float64(t.Prompt)*p[0] + float64(t.Completion)*p[1]) / 1e6
}
