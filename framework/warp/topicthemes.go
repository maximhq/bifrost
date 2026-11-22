package warp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

// Themes group topics. A deployment with a hundred topics answers "what do
// people ask about" with a hundred rows, which nobody reads; a dozen themes
// over them is the answer a person wants first. A theme is a topic row of
// kind theme with no centre: nothing is filed under it, and a topic names its
// theme in parent_id. Themes are made by the model from the topic names
// alone, which is enough: a theme is about what the names have in common,
// not about the requests.
//
// Themes are rebuilt whenever the topics change - after a bootstrap run,
// after discovery makes new ones, after upkeep retires or merges - from the
// whole set, so a topic is never left in a theme that no longer fits it. The
// model is shown the themes that exist and asked to keep their names where
// they still fit, so a theme's id and name survive a rebuild that does not
// change what it covers.

const (
	// topicsThemeMinTopics is the fewest topics worth grouping. Below it the
	// topics are the themes.
	topicsThemeMinTopics = 4
	// topicsThemeMinSize is the fewest topics a theme may hold. A theme of
	// one topic is that topic.
	topicsThemeMinSize = 2
	// topicsThemeMaxTopics bounds the names one grouping call is shown.
	topicsThemeMaxTopics = 300
)

// TopicThemes is what one grouping did.
type TopicThemes struct {
	// Themes is how many themes are active afterwards, and Grouped how many
	// topics are in one. Retired counts themes that ended with no topic.
	Themes, Grouped, Retired int
}

// GroupTopics puts the active topics into themes, keeping the themes that
// still fit and retiring the ones that no longer hold a topic.
//
// A model that cannot answer leaves the topics' themes as they were, apart
// from the tidying that needs no model: a theme with no active topic is
// retired, and a merged topic takes the theme of the topic it merged into.
func (s *Service) GroupTopics(ctx context.Context) (TopicThemes, error) {
	var done TopicThemes
	store := s.topicStore()
	if store == nil {
		return done, ErrUnavailable
	}
	config, err := s.Config(ctx)
	if err != nil {
		return done, err
	}
	signature := embeddingConfigSignature(config)
	topics, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{Kinds: []string{logstore.WarpTopicKindTopic}, EmbeddingSignature: signature})
	if err != nil {
		return done, fmt.Errorf("read topics: %w", err)
	}
	themes, err := store.ListWarpTopics(ctx, logstore.WarpTopicFilter{Kinds: []string{logstore.WarpTopicKindTheme}, Statuses: []string{logstore.WarpTopicStatusActive}})
	if err != nil {
		return done, fmt.Errorf("read themes: %w", err)
	}
	var active, merged []logstore.WarpTopic
	for _, topic := range topics {
		switch topic.Status {
		case logstore.WarpTopicStatusActive:
			active = append(active, topic)
		case logstore.WarpTopicStatusMerged:
			merged = append(merged, topic)
		}
	}
	sort.Slice(active, func(i, j int) bool { return active[i].ID < active[j].ID })

	var answer *themeAnswer
	var askErr error
	if len(active) >= topicsThemeMinTopics {
		chatCtx := withBackgroundWork(ctx)
		answer, askErr = askForThemes(chatCtx, s.chatFuncFor(chatCtx, config, ""), config, active, themes)
	} else {
		// Too few to group: every topic stands alone, which the empty answer
		// reconciles to.
		answer = &themeAnswer{}
	}
	plan := planThemes(active, merged, themes, answer, signature, time.Now().UTC())
	if err := store.UpsertWarpTopics(ctx, plan.writes); err != nil {
		return done, fmt.Errorf("write themes: %w", err)
	}
	done = plan.done
	if askErr != nil {
		return done, fmt.Errorf("group topics: %w", askErr)
	}
	return done, nil
}

// themeAnswer is what the model is asked for: named groups of topic numbers,
// the numbers being the positions in the list it was shown.
type themeAnswer struct {
	Themes []struct {
		Name   string `json:"name"`
		Topics []int  `json:"topics"`
	} `json:"themes"`
}

const topicThemeInstructions = "You group topics for a dashboard. Given a numbered list of topic names from one deployment's traffic, " +
	"put the topics that belong to the same broad subject into themes: a theme name of one to four words in title case that a product manager would recognise, " +
	"and the numbers of its topics. A theme holds at least two topics. Leave out a topic that fits no theme; never put a topic in two themes. " +
	"Where an existing theme still fits, keep its exact name. Aim for between three and twelve themes. " +
	"Reply with JSON only, of the form {\"themes\":[{\"name\":\"Billing\",\"topics\":[1,4]}]}, with no explanation."

// askForThemes asks the model to group the topics by name. Nil when the
// answer is empty: that is the model saying nothing groups, which is an
// answer.
func askForThemes(ctx context.Context, chat ChatFunc, config *schemas.WarpConfig, topics, existing []logstore.WarpTopic) (*themeAnswer, error) {
	if chat == nil {
		return nil, fmt.Errorf("no model to group topics with")
	}
	var prompt strings.Builder
	if len(existing) > 0 {
		prompt.WriteString("Existing themes:\n")
		for _, theme := range existing {
			prompt.WriteString("- ")
			prompt.WriteString(theme.Label)
			prompt.WriteString("\n")
		}
		prompt.WriteString("\n")
	}
	prompt.WriteString("Topics:\n")
	for index, topic := range topics {
		if index == topicsThemeMaxTopics {
			break
		}
		fmt.Fprintf(&prompt, "%d. %s\n", index+1, topic.Label)
	}
	prompt.WriteString("\nThemes as JSON:")
	itemType := schemas.ResponsesMessageTypeMessage
	role := schemas.ResponsesInputMessageRoleUser
	content := prompt.String()
	instructions := topicThemeInstructions
	provider, model := requestTarget(config)
	response, bifrostErr := chat(ctx, &schemas.BifrostResponsesRequest{
		Provider: provider,
		Model:    model,
		Input: []schemas.ResponsesMessage{{
			Type:    &itemType,
			Role:    &role,
			Content: &schemas.ResponsesMessageContent{ContentStr: &content},
		}},
		Params: &schemas.ResponsesParameters{Instructions: &instructions},
	})
	if bifrostErr != nil {
		return nil, fmt.Errorf("%s", errorMessage(bifrostErr))
	}
	if response == nil {
		return nil, fmt.Errorf("the model returned no output")
	}
	return parseThemeAnswer(responsesText(response.Output))
}

// parseThemeAnswer reads the model's JSON, fenced or not.
func parseThemeAnswer(raw string) (*themeAnswer, error) {
	text := strings.TrimSpace(raw)
	if start := strings.Index(text, "{"); start >= 0 {
		if end := strings.LastIndex(text, "}"); end > start {
			text = text[start : end+1]
		}
	}
	var answer themeAnswer
	if err := sonic.UnmarshalString(text, &answer); err != nil {
		return nil, fmt.Errorf("the model's answer is not the JSON asked for: %w", err)
	}
	return &answer, nil
}

// themePlan is what reconciling an answer with the stored rows writes.
type themePlan struct {
	writes []logstore.WarpTopic
	done   TopicThemes
}

// planThemes turns the model's answer into rows: a theme per group of at
// least two topics, reusing an existing theme's id where the name matches,
// every topic's parent set to its theme or to nothing, every merged topic
// given its survivor's theme, and every theme left without a topic retired.
//
// An answer of nil - the model could not be asked or did not answer - keeps
// each active topic's theme as it is and does only the tidying.
func planThemes(active, merged, themes []logstore.WarpTopic, answer *themeAnswer, signature string, now time.Time) themePlan {
	var plan themePlan
	byName := make(map[string]logstore.WarpTopic, len(themes))
	for _, theme := range themes {
		byName[themeKey(theme.Label)] = theme
	}
	parentOf := make(map[string]string, len(active))
	for _, topic := range active {
		parentOf[topic.ID] = topic.ParentID
	}
	kept := make(map[string]struct{}, len(themes))
	if answer != nil {
		for _, topic := range active {
			parentOf[topic.ID] = ""
		}
		placed := make(map[int]struct{})
		for _, group := range answer.Themes {
			label := cleanTopicLabel(group.Name)
			var members []string
			for _, number := range group.Topics {
				if number < 1 || number > len(active) {
					continue
				}
				if _, taken := placed[number]; taken {
					continue
				}
				members = append(members, active[number-1].ID)
			}
			if label == "" || len(members) < topicsThemeMinSize {
				continue
			}
			theme, ok := byName[themeKey(label)]
			if !ok {
				theme = logstore.WarpTopic{
					ID: uuid.NewString(), Kind: logstore.WarpTopicKindTheme, Label: label,
					EmbeddingSignature: signature, Status: logstore.WarpTopicStatusActive, CreatedAt: now,
				}
				byName[themeKey(label)] = theme
				plan.writes = append(plan.writes, theme)
			}
			if _, already := kept[theme.ID]; already {
				continue
			}
			kept[theme.ID] = struct{}{}
			for _, id := range members {
				parentOf[id] = theme.ID
			}
			for _, number := range group.Topics {
				placed[number] = struct{}{}
			}
		}
	}
	// Tidying that holds whether or not the model answered.
	inUse := make(map[string]int, len(themes))
	for _, topic := range active {
		if parentOf[topic.ID] != "" {
			inUse[parentOf[topic.ID]]++
		}
	}
	live := make(map[string]struct{}, len(themes))
	for _, theme := range themes {
		live[theme.ID] = struct{}{}
	}
	for id := range kept {
		live[id] = struct{}{}
	}
	for _, topic := range active {
		parent := parentOf[topic.ID]
		if _, ok := live[parent]; parent != "" && !ok {
			parent = ""
		}
		if parent != topic.ParentID {
			topic.ParentID = parent
			plan.writes = append(plan.writes, topic)
		}
		parentOf[topic.ID] = parent
		if parent != "" {
			plan.done.Grouped++
		}
	}
	themeCount := make(map[string]int)
	for _, parent := range parentOf {
		if parent != "" {
			themeCount[parent]++
		}
	}
	for _, theme := range themes {
		if themeCount[theme.ID] == 0 {
			theme.Status = logstore.WarpTopicStatusRetired
			plan.writes = append(plan.writes, theme)
			plan.done.Retired++
		}
	}
	plan.done.Themes = len(themeCount)
	// A merged topic's requests count toward its survivor, so it is in the
	// survivor's theme; the logs filter by theme relies on that.
	for _, topic := range merged {
		parent, ok := parentOf[topic.MergedInto]
		if !ok {
			continue
		}
		if parent != topic.ParentID {
			topic.ParentID = parent
			plan.writes = append(plan.writes, topic)
		}
	}
	return plan
}

// themeKey is how two theme names are the same name.
func themeKey(label string) string {
	return strings.ToLower(strings.Join(strings.Fields(label), " "))
}
