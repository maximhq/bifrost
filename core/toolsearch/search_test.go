package toolsearch

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
)

func searchNames(tools []schemas.ResponsesTool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, *tool.Name)
	}
	return names
}

func catalog() []schemas.ResponsesTool {
	return []schemas.ResponsesTool{
		functionTool("get_weather", "Get the current weather for a city", true),
		functionTool("fetchWeatherForecast", "Multi-day forecast", true),
		functionTool("get_stock_price", "Get a stock quote by ticker", true),
		functionTool("send_email", "Send an email to a recipient", true),
	}
}

func TestSearchSelectByName(t *testing.T) {
	assert.Equal(t, []string{"send_email", "get_weather"}, searchNames(search(catalog(), "select:send_email, get_weather,missing", 5)))
	assert.Equal(t, []string{"send_email"}, searchNames(search(catalog(), "select:send_email,get_weather", 1)))
}

func TestSearchRanksByBM25(t *testing.T) {
	got := searchNames(search(catalog(), "weather forecast", 5))
	assert.Equal(t, []string{"fetchWeatherForecast", "get_weather"}, got, "camelCase names are split into words")
	assert.Equal(t, []string{"get_stock_price"}, searchNames(search(catalog(), "stock ticker", 5)))
	assert.Len(t, search(catalog(), "weather", 1), 1)
}

func TestSearchFallsBackToRegex(t *testing.T) {
	assert.Equal(t, []string{"send_email"}, searchNames(search(catalog(), "^send_", 5)))
	assert.Empty(t, search(catalog(), "zzz", 5))
	assert.Empty(t, search(catalog(), "(", 5), "an invalid pattern matches nothing")
	assert.Empty(t, search(catalog(), "", 5))
}

func TestParseArguments(t *testing.T) {
	q, limit := parseArguments(`{"query":" weather ","limit":3}`)
	assert.Equal(t, "weather", q)
	assert.Equal(t, 3, limit)
	_, limit = parseArguments(`{"query":"x","limit":500}`)
	assert.Equal(t, maxLimit, limit)
	q, limit = parseArguments("")
	assert.Equal(t, "", q)
	assert.Equal(t, defaultLimit, limit)
}

func TestTokenize(t *testing.T) {
	assert.Equal(t, []string{"fetch", "weather", "report", "v7"}, tokenize("fetchWeather_report-v7"))
	assert.Equal(t, []string{"http", "server"}, tokenize("HTTP server"))
}
