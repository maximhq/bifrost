package warp

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/require"
)

// themeTestChat groups topics the way a model would, by what each topic's
// name is about: themeOf names a topic's theme, or "" to leave it out. It
// answers in the JSON the grouping asks for and names clusters like
// topicTestNaming otherwise, so one fake serves a whole run.
func themeTestChat(themeOf func(label string) string) ChatFunc {
	numbered := regexp.MustCompile(`(?m)^(\d+)\. (.+)$`)
	return func(ctx context.Context, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		text := responsesInputText(req.Input)
		if !strings.Contains(text, "Topics:") {
			return topicTestNaming(ctx, req)
		}
		groups := map[string][]string{}
		var order []string
		for _, match := range numbered.FindAllStringSubmatch(text, -1) {
			theme := themeOf(match[2])
			if theme == "" {
				continue
			}
			if _, ok := groups[theme]; !ok {
				order = append(order, theme)
			}
			groups[theme] = append(groups[theme], match[1])
		}
		var parts []string
		for _, theme := range order {
			parts = append(parts, fmt.Sprintf(`{"name":%q,"topics":[%s]}`, theme, strings.Join(groups[theme], ",")))
		}
		return TextTurn("```json\n{\"themes\":[" + strings.Join(parts, ",") + "]}\n```"), nil
	}
}

// themeTestTopics writes five active topics, one merged into the first, and
// returns the store they are in.
func themeTestTopics(t *testing.T, service *Service, reader *semanticLogReader) logstore.WarpTopicStore {
	t.Helper()
	store := reader.topics
	signature := embeddingConfigSignature(configFromRow(assignTestRow()))
	var rows []logstore.WarpTopic
	for index, label := range []string{"Refund requests", "Invoice questions", "Password resets", "Login failures", "Weather"} {
		rows = append(rows, logstore.WarpTopic{ID: fmt.Sprintf("t-%d", index), Label: label, Centroid: logstore.EncodeWarpVector([]float32{1, 0, 0}), EmbeddingSignature: signature})
	}
	rows = append(rows, logstore.WarpTopic{ID: "t-merged", Label: "Refunds", Centroid: logstore.EncodeWarpVector([]float32{1, 0, 0}), EmbeddingSignature: signature, Status: logstore.WarpTopicStatusMerged, MergedInto: "t-0"})
	require.NoError(t, store.UpsertWarpTopics(context.Background(), rows))
	return store
}

func topicsByID(t *testing.T, store logstore.WarpTopicStore, kinds ...string) map[string]logstore.WarpTopic {
	t.Helper()
	rows, err := store.ListWarpTopics(context.Background(), logstore.WarpTopicFilter{Kinds: kinds})
	require.NoError(t, err)
	byID := map[string]logstore.WarpTopic{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	return byID
}

func money(label string) string {
	switch label {
	case "Refund requests", "Invoice questions":
		return "Billing"
	case "Password resets", "Login failures":
		return "Account Access"
	}
	return ""
}

// The model is shown the topic names and answers with named groups. Each
// group of at least two becomes a theme row; each topic names its theme; a
// merged topic takes its survivor's theme; a topic in no group stays alone.
// Asked again, the same names keep the same rows.
func TestWarpGroupTopicsMakesThemesFromTheModelsAnswer(t *testing.T) {
	service, reader := discoveryTestService(t, themeTestChat(money))
	store := themeTestTopics(t, service, reader)
	ctx := context.Background()

	done, err := service.GroupTopics(ctx)
	require.NoError(t, err)
	require.Equal(t, TopicThemes{Themes: 2, Grouped: 4}, done)

	themes := topicsByID(t, store, logstore.WarpTopicKindTheme)
	require.Len(t, themes, 2)
	byLabel := map[string]logstore.WarpTopic{}
	for _, theme := range themes {
		require.Equal(t, logstore.WarpTopicStatusActive, theme.Status)
		require.Empty(t, theme.Centroid, "a theme has no centre")
		require.Equal(t, embeddingConfigSignature(configFromRow(assignTestRow())), theme.EmbeddingSignature)
		byLabel[theme.Label] = theme
	}
	require.Contains(t, byLabel, "Billing")
	require.Contains(t, byLabel, "Account Access")

	topics := topicsByID(t, store, logstore.WarpTopicKindTopic)
	require.Equal(t, byLabel["Billing"].ID, topics["t-0"].ParentID)
	require.Equal(t, byLabel["Billing"].ID, topics["t-1"].ParentID)
	require.Equal(t, byLabel["Account Access"].ID, topics["t-2"].ParentID)
	require.Equal(t, byLabel["Account Access"].ID, topics["t-3"].ParentID)
	require.Empty(t, topics["t-4"].ParentID, "left out of every group")
	require.Equal(t, byLabel["Billing"].ID, topics["t-merged"].ParentID, "a merged topic follows its survivor")
	require.NotEmpty(t, topics["t-0"].Centroid, "written back whole")

	done, err = service.GroupTopics(ctx)
	require.NoError(t, err)
	require.Equal(t, TopicThemes{Themes: 2, Grouped: 4}, done)
	again := topicsByID(t, store, logstore.WarpTopicKindTheme)
	require.Len(t, again, 2)
	require.Contains(t, again, byLabel["Billing"].ID, "the same name is the same row")
}

// A theme the model names again keeps its row, one it stops naming is
// retired, and when the topics drop below what is worth grouping every
// theme goes.
func TestWarpGroupTopicsKeepsNamesAndRetiresEmptyThemes(t *testing.T) {
	themeOf := money
	service, reader := discoveryTestService(t, themeTestChat(func(label string) string { return themeOf(label) }))
	store := themeTestTopics(t, service, reader)
	ctx := context.Background()
	_, err := service.GroupTopics(ctx)
	require.NoError(t, err)
	before := topicsByID(t, store, logstore.WarpTopicKindTheme)
	var billing, access string
	for id, theme := range before {
		switch theme.Label {
		case "Billing":
			billing = id
		case "Account Access":
			access = id
		}
	}

	themeOf = func(label string) string {
		if theme := money(label); theme == "Account Access" {
			return "security" // a new name, in the model's own casing
		} else {
			return theme
		}
	}
	done, err := service.GroupTopics(ctx)
	require.NoError(t, err)
	require.Equal(t, TopicThemes{Themes: 2, Grouped: 4, Retired: 1}, done)
	after := topicsByID(t, store, logstore.WarpTopicKindTheme)
	require.Len(t, after, 3)
	require.Equal(t, logstore.WarpTopicStatusActive, after[billing].Status, "named again, kept")
	require.Equal(t, logstore.WarpTopicStatusRetired, after[access].Status, "no longer named, retired")
	topics := topicsByID(t, store, logstore.WarpTopicKindTopic)
	require.Equal(t, "security", after[topics["t-2"].ParentID].Label)

	// Two of the five retire: three topics are too few to group.
	for _, id := range []string{"t-2", "t-3"} {
		topic := topics[id]
		topic.Status = logstore.WarpTopicStatusRetired
		require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{topic}))
	}
	done, err = service.GroupTopics(ctx)
	require.NoError(t, err)
	require.Equal(t, TopicThemes{Retired: 2}, done)
	for _, theme := range topicsByID(t, store, logstore.WarpTopicKindTheme) {
		require.Equal(t, logstore.WarpTopicStatusRetired, theme.Status, theme.Label)
	}
	for id, topic := range topicsByID(t, store, logstore.WarpTopicKindTopic) {
		if topic.Status == logstore.WarpTopicStatusActive {
			require.Empty(t, topic.ParentID, id)
		}
	}
}

// A model that does not answer in the form asked leaves every topic's theme
// as it was, and the grouping still tidies what needs no model: a theme no
// active topic is in is retired, and a merged topic takes its survivor's
// theme.
func TestWarpGroupTopicsSurvivesAModelThatCannotAnswer(t *testing.T) {
	service, reader := discoveryTestService(t, func(context.Context, *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
		return TextTurn("I would group these by subject."), nil
	})
	store := themeTestTopics(t, service, reader)
	ctx := context.Background()
	signature := embeddingConfigSignature(configFromRow(assignTestRow()))
	require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{
		{ID: "theme-billing", Kind: logstore.WarpTopicKindTheme, Label: "Billing", EmbeddingSignature: signature},
		{ID: "theme-empty", Kind: logstore.WarpTopicKindTheme, Label: "Nothing Left", EmbeddingSignature: signature},
	}))
	topics := topicsByID(t, store, logstore.WarpTopicKindTopic)
	for _, id := range []string{"t-0", "t-1"} {
		topic := topics[id]
		topic.ParentID = "theme-billing"
		require.NoError(t, store.UpsertWarpTopics(ctx, []logstore.WarpTopic{topic}))
	}

	done, err := service.GroupTopics(ctx)
	require.ErrorContains(t, err, "not the JSON asked for")
	require.Equal(t, TopicThemes{Themes: 1, Grouped: 2, Retired: 1}, done)
	themes := topicsByID(t, store, logstore.WarpTopicKindTheme)
	require.Equal(t, logstore.WarpTopicStatusActive, themes["theme-billing"].Status)
	require.Equal(t, logstore.WarpTopicStatusRetired, themes["theme-empty"].Status)
	topics = topicsByID(t, store, logstore.WarpTopicKindTopic)
	require.Equal(t, "theme-billing", topics["t-0"].ParentID, "kept as it was")
	require.Equal(t, "theme-billing", topics["t-merged"].ParentID, "a merged topic follows its survivor even so")
	require.Empty(t, topics["t-2"].ParentID)
}

// The answer is taken as a model writes it: numbers off the list, a topic
// named twice, a group of one and a nameless group are ignored rather than
// trusted.
func TestPlanThemesIgnoresWhatTheModelGotWrong(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	active := []logstore.WarpTopic{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}, {ID: "c", Label: "C"}, {ID: "d", Label: "D"}, {ID: "e", Label: "E"}}
	answer, err := parseThemeAnswer(`Sure: {"themes":[{"name":"  First  ","topics":[1,2,0,99,2]},{"name":"Second","topics":[2,3,4]},{"name":"","topics":[3,4]},{"name":"Lonely","topics":[5]}]}`)
	require.NoError(t, err)
	plan := planThemes(active, nil, nil, answer, "sig", now)
	require.Equal(t, TopicThemes{Themes: 2, Grouped: 4}, plan.done)
	var themes []string
	parents := map[string]string{}
	for _, row := range plan.writes {
		if row.Kind == logstore.WarpTopicKindTheme {
			themes = append(themes, row.Label)
			require.Equal(t, now, row.CreatedAt)
		} else {
			parents[row.ID] = row.ParentID
		}
	}
	require.ElementsMatch(t, []string{"First", "Second"}, themes)
	require.Equal(t, parents["a"], parents["b"], "First holds a and b")
	require.NotEqual(t, parents["a"], parents["c"], "b was already placed, so Second holds c and d")
	require.Equal(t, parents["c"], parents["d"])
	require.NotEmpty(t, parents["d"])
	_, lonely := parents["e"]
	require.False(t, lonely, "Lonely is a group of one, which is no theme")

	_, err = parseThemeAnswer("no json here")
	require.Error(t, err)
}
