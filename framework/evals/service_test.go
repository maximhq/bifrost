package evals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/queue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// memoryServiceStore is an in-memory stand-in for the config store's evaluator tables.
type memoryServiceStore struct {
	mu                  sync.Mutex
	evaluators          map[string]*tables.TableEvaluator
	versions            map[string][]tables.TableEvaluatorVersion
	executions          map[string]*tables.TableEvalExecution
	lastList            configstore.EvaluatorListParams // what the last listing was asked for
	failVersion         bool                            // version writes fail, as a database error would
	holdComplete        chan struct{}                   // completion writes wait until it is closed or their ctx ends
	hangGet             bool                            // execution reads wait until their context ends
	failGet             bool                            // execution reads fail, as a database error would
	completeHadDeadline []bool                          // per completion write: whether its context had a deadline
	completions         int                             // completion writes that stored a result
	starts              int                             // StartEvalExecution calls: one per run
	failComplete        bool                            // completion writes fail, as a database error would
}

func newMemoryServiceStore() *memoryServiceStore {
	return &memoryServiceStore{
		evaluators: map[string]*tables.TableEvaluator{},
		versions:   map[string][]tables.TableEvaluatorVersion{},
		executions: map[string]*tables.TableEvalExecution{},
	}
}

func (m *memoryServiceStore) ListEvaluators(_ context.Context, params configstore.EvaluatorListParams) ([]tables.TableEvaluator, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastList = params
	var out []tables.TableEvaluator
	for _, e := range m.evaluators {
		if len(params.Types) == 0 || slices.Contains(params.Types, e.Type) {
			out = append(out, *e)
		}
	}
	return out, int64(len(out)), nil
}

func (m *memoryServiceStore) GetEvaluator(_ context.Context, id string) (*tables.TableEvaluator, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.evaluators[id]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	copied := *e
	return &copied, nil
}

func (m *memoryServiceStore) CreateEvaluator(_ context.Context, evaluator *tables.TableEvaluator, version *tables.TableEvaluatorVersion) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.evaluators {
		if e.Name == evaluator.Name {
			return configstore.ErrEvaluatorNameExists
		}
	}
	evaluator.ID, evaluator.CurrentVersion = uuid.NewString(), 1
	version.ID, version.EvaluatorID, version.Version = uuid.NewString(), evaluator.ID, 1
	copied := *evaluator
	m.evaluators[evaluator.ID] = &copied
	m.versions[evaluator.ID] = []tables.TableEvaluatorVersion{*version}
	return nil
}

func (m *memoryServiceStore) UpdateEvaluator(_ context.Context, evaluator *tables.TableEvaluator) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored, ok := m.evaluators[evaluator.ID]
	if !ok {
		return configstore.ErrNotFound
	}
	stored.Name, stored.Description, stored.Tags = evaluator.Name, evaluator.Description, evaluator.Tags
	return nil
}

func (m *memoryServiceStore) DeleteEvaluator(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.evaluators[id]; !ok {
		return configstore.ErrNotFound
	}
	delete(m.evaluators, id)
	return nil
}

func (m *memoryServiceStore) CreateEvaluatorVersion(_ context.Context, version *tables.TableEvaluatorVersion) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failVersion {
		return errors.New("version write failed")
	}
	stored, ok := m.evaluators[version.EvaluatorID]
	if !ok {
		return configstore.ErrNotFound
	}
	version.ID, version.Version = uuid.NewString(), len(m.versions[version.EvaluatorID])+1
	m.versions[version.EvaluatorID] = append(m.versions[version.EvaluatorID], *version)
	stored.CurrentVersion = version.Version
	return nil
}

func (m *memoryServiceStore) UpdateEvaluatorAndAddVersion(ctx context.Context, evaluator *tables.TableEvaluator, version *tables.TableEvaluatorVersion) error {
	m.mu.Lock()
	fail := m.failVersion
	m.mu.Unlock()
	if fail {
		return errors.New("version write failed")
	}
	if err := m.UpdateEvaluator(ctx, evaluator); err != nil {
		return err
	}
	return m.CreateEvaluatorVersion(ctx, version)
}

func (m *memoryServiceStore) GetEvaluatorVersion(_ context.Context, evaluatorID string, number int) (*tables.TableEvaluatorVersion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	versions := m.versions[evaluatorID]
	if number < 1 || number > len(versions) {
		return nil, configstore.ErrNotFound
	}
	copied := versions[number-1]
	return &copied, nil
}

func (m *memoryServiceStore) ListEvaluatorVersions(_ context.Context, evaluatorID string) ([]tables.TableEvaluatorVersion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	versions := m.versions[evaluatorID]
	out := make([]tables.TableEvaluatorVersion, 0, len(versions))
	for i := len(versions) - 1; i >= 0; i-- {
		out = append(out, versions[i])
	}
	return out, nil
}

func (m *memoryServiceStore) GetEvaluatorVersionsAt(_ context.Context, versionByEvaluator map[string]int) (map[string]tables.TableEvaluatorVersion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]tables.TableEvaluatorVersion{}
	for id, number := range versionByEvaluator {
		if versions := m.versions[id]; number >= 1 && number <= len(versions) {
			out[id] = versions[number-1]
		}
	}
	return out, nil
}

func (m *memoryServiceStore) CreateEvalExecution(_ context.Context, execution *tables.TableEvalExecution) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	execution.ID, execution.Status, execution.CreatedAt = uuid.NewString(), tables.EvalExecutionQueued, time.Now()
	copied := *execution
	m.executions[execution.ID] = &copied
	return nil
}

func (m *memoryServiceStore) GetEvalExecution(ctx context.Context, id string) (*tables.TableEvalExecution, error) {
	m.mu.Lock()
	hang, fail := m.hangGet, m.failGet
	m.mu.Unlock()
	if fail {
		return nil, errors.New("the store is unavailable")
	}
	if hang {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	execution, ok := m.executions[id]
	if !ok {
		return nil, configstore.ErrNotFound
	}
	copied := *execution
	return &copied, nil
}

func (m *memoryServiceStore) StartEvalExecution(_ context.Context, id, keyID, _ string, _, _ time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	execution, ok := m.executions[id]
	if !ok {
		return false, configstore.ErrNotFound
	}
	m.starts++
	if execution.Status != tables.EvalExecutionCompleted {
		now := time.Now()
		execution.Status, execution.KeyID, execution.StartedAt = tables.EvalExecutionRunning, keyID, &now
	}
	return true, nil
}

func (m *memoryServiceStore) RouteEvalExecution(_ context.Context, id, fromKey, toKey string, routedAt time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	execution, ok := m.executions[id]
	if !ok || execution.Status != tables.EvalExecutionQueued || execution.KeyID != fromKey {
		return false, nil
	}
	execution.KeyID, execution.RoutedAt = toKey, new(routedAt)
	return true, nil
}

func (m *memoryServiceStore) ListUnfinishedEvalExecutions(context.Context, time.Time, string, int) ([]tables.TableEvalExecution, error) {
	return nil, nil
}

func (m *memoryServiceStore) ListQueuedEvalExecutions(context.Context, time.Time, time.Time, []string, time.Time, string, int) ([]tables.TableEvalExecution, error) {
	return nil, nil
}

func (m *memoryServiceStore) DeleteCompletedEvalExecutions(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func (m *memoryServiceStore) CompleteEvalExecution(ctx context.Context, id string, result json.RawMessage) error {
	_, err := m.completeWhere(ctx, id, result, func(*tables.TableEvalExecution) bool { return true })
	return err
}

// CompleteQueuedEvalExecution completes an execution only while it is queued on keyID, as
// the config store does.
func (m *memoryServiceStore) CompleteQueuedEvalExecution(ctx context.Context, id, keyID string, result json.RawMessage) (bool, error) {
	return m.completeWhere(ctx, id, result, func(execution *tables.TableEvalExecution) bool {
		return execution.Status == tables.EvalExecutionQueued && execution.KeyID == keyID
	})
}

// completeWhere stores a result on an execution that matches, and reports whether it did.
func (m *memoryServiceStore) completeWhere(ctx context.Context, id string, result json.RawMessage, matches func(*tables.TableEvalExecution) bool) (bool, error) {
	m.mu.Lock()
	_, hasDeadline := ctx.Deadline()
	m.completeHadDeadline = append(m.completeHadDeadline, hasDeadline)
	hold := m.holdComplete
	m.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failComplete {
		return false, errors.New("the store is unavailable")
	}
	execution, ok := m.executions[id]
	if !ok {
		return false, configstore.ErrNotFound
	}
	if execution.Status == tables.EvalExecutionCompleted || !matches(execution) {
		return false, nil
	}
	now := time.Now()
	execution.Status, execution.Result, execution.CompletedAt = tables.EvalExecutionCompleted, result, &now
	m.completions++
	return true, nil
}

type serviceHarness struct {
	service *Service
	store   *memoryServiceStore
	judge   *scriptedJudge
}

// newServiceHarness wires a service the way the server does: every evaluator type
// registered, an in-memory queue, and the given eval keys.
func newServiceHarness(t *testing.T, keys []EvalKey) *serviceHarness {
	t.Helper()
	return newServiceHarnessOn(t, keys, nil)
}

// newServiceHarnessOn is newServiceHarness with the queue passed through wrap, when set.
func newServiceHarnessOn(t *testing.T, keys []EvalKey, wrap func(queue.Queue) queue.Queue) *serviceHarness {
	t.Helper()
	judge := &scriptedJudge{}
	engine := NewEngine(EngineOptions{})
	RegisterAIEvaluator(engine, judge.call)
	RegisterBuiltinAIEvaluators(engine, judge.call)
	RegisterProgrammaticEvaluator(engine, ProgrammaticOptions{})
	RegisterAPIEvaluator(engine, APIOptions{})
	RegisterStatisticalEvaluators(engine, nil)

	q, err := queue.NewQueue(context.Background(), &queue.Config{Enabled: true, Type: queue.QueueTypeMemory}, queue.Dependencies{}, nil)
	require.NoError(t, err)
	store := newMemoryServiceStore()
	var runnerQueue queue.Queue = q
	if wrap != nil {
		runnerQueue = wrap(q)
	}
	runner := NewRunner(runnerQueue, engine, func(context.Context) ([]EvalKey, error) { return keys, nil }, NewExecutionJobStore(store), RunnerOptions{PollInterval: 5 * time.Millisecond})
	require.NoError(t, runner.Start(context.Background()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Close(ctx)
		_ = q.Close(ctx)
	})
	return &serviceHarness{service: NewService(store, engine, runner), store: store, judge: judge}
}

const lengthCheckConfig = `{"code":"def validate(output, minimum = \"3\"):\n    return len(output) >= int(minimum)\n","response_type":"yes_no"}`

func TestService_CreateAndGet(t *testing.T) {
	h := newServiceHarness(t, nil)
	ctx := context.Background()

	created, err := h.service.Create(ctx, EvaluatorInput{
		Name: " Long enough ", Description: "checks length", Type: EvaluatorTypeProgrammatic,
		Tags: []string{"format"}, Config: json.RawMessage(lengthCheckConfig), CreatedBy: "ada",
	})
	require.NoError(t, err)
	assert.Equal(t, "Long enough", created.Name, "the name is trimmed")
	assert.Equal(t, 1, created.Version)
	assert.Equal(t, []string{"output"}, created.Variables, "the function's parameters are its variables")
	assert.Equal(t, []string{"minimum"}, created.OptionalVariables)
	assert.True(t, created.Executable)
	assert.Equal(t, "Yes", gjson.GetBytes(created.Config, "pass_criteria.entry_level.value").String(), "the stored config carries the defaults")

	got, err := h.service.Get(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, created.Config, got.Config)

	_, err = h.service.Get(ctx, "missing")
	assert.ErrorIs(t, err, configstore.ErrNotFound)
}

func TestService_CreateRejectsBadInput(t *testing.T) {
	h := newServiceHarness(t, nil)
	ctx := context.Background()
	cases := []struct {
		name    string
		in      EvaluatorInput
		wantErr string
	}{
		{"no name", EvaluatorInput{Type: EvaluatorTypeProgrammatic, Config: json.RawMessage(lengthCheckConfig)}, "name is required"},
		{"unknown type", EvaluatorInput{Name: "x", Type: "vibes"}, "type must be one of"},
		{"invalid config", EvaluatorInput{Name: "x", Type: EvaluatorTypeAI, Config: json.RawMessage(`{"grading_style":"yes_no"}`)}, "instructions"},
		{"code that does not parse", EvaluatorInput{Name: "x", Type: EvaluatorTypeProgrammatic, Config: json.RawMessage(`{"code":"def validate(output)\n  return True","response_type":"yes_no"}`)}, "syntax"},
		{"code without validate", EvaluatorInput{Name: "x", Type: EvaluatorTypeProgrammatic, Config: json.RawMessage(`{"code":"x = 1\n","response_type":"yes_no"}`)}, "validate"},
		{"custom statistical", EvaluatorInput{Name: "x", Type: EvaluatorTypeStatistical, Config: json.RawMessage(`{}`)}, "catalog"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.service.Create(ctx, tc.in)
			require.Error(t, err)
			assert.True(t, IsInvalid(err), "a caller mistake is reported as one: %v", err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}

	_, err := h.service.Create(ctx, EvaluatorInput{Name: "dup", Type: EvaluatorTypeProgrammatic, Config: json.RawMessage(lengthCheckConfig)})
	require.NoError(t, err)
	_, err = h.service.Create(ctx, EvaluatorInput{Name: "dup", Type: EvaluatorTypeProgrammatic, Config: json.RawMessage(lengthCheckConfig)})
	assert.ErrorIs(t, err, configstore.ErrEvaluatorNameExists)
}

// A tag with a NUL character is refused on create, install and update: Postgres reads the
// tags back as JSON to filter by them, and one such tag would break every tag-filtered list.
func TestService_RefusesATagWithANulCharacter(t *testing.T) {
	h := newServiceHarness(t, nil)
	ctx := context.Background()
	bad := []string{"ok", "a\x00b"}

	_, err := h.service.Create(ctx, EvaluatorInput{Name: "nul tag", Type: EvaluatorTypeProgrammatic, Tags: bad, Config: json.RawMessage(lengthCheckConfig)})
	require.Error(t, err)
	assert.True(t, IsInvalid(err), "create reports a caller mistake: %v", err)
	assert.Contains(t, err.Error(), `"a\x00b"`, "the error names the tag")

	_, err = h.service.Install(ctx, BuiltinBLEU, InstallInput{Tags: bad})
	require.Error(t, err)
	assert.True(t, IsInvalid(err), "install reports a caller mistake: %v", err)

	created, err := h.service.Create(ctx, EvaluatorInput{Name: "clean tags", Type: EvaluatorTypeProgrammatic, Tags: []string{"old"}, Config: json.RawMessage(lengthCheckConfig)})
	require.NoError(t, err)
	_, err = h.service.Update(ctx, created.ID, EvaluatorInput{Name: "clean tags", Tags: bad})
	require.Error(t, err)
	assert.True(t, IsInvalid(err), "update reports a caller mistake: %v", err)
	assert.Contains(t, err.Error(), `"a\x00b"`, "the error names the tag")
	got, err := h.service.Get(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"old"}, got.Tags, "the refused update stores nothing")
}

// A save that is refused changes nothing: the name, description and tags are not
// stored when the config that came with them is invalid.
func TestService_UpdateRefusedLeavesTheEvaluatorAsItWas(t *testing.T) {
	h := newServiceHarness(t, nil)
	ctx := context.Background()
	created, err := h.service.Create(ctx, EvaluatorInput{Name: "before", Type: EvaluatorTypeProgrammatic, Tags: []string{"old"},
		Config: json.RawMessage(`{"code":"def validate(output):\n    return True\n","response_type":"yes_no"}`)})
	require.NoError(t, err)

	_, err = h.service.Update(ctx, created.ID, EvaluatorInput{Name: "after", Description: "changed", Tags: []string{"new"},
		Config: json.RawMessage(`{"code":"","response_type":"yes_no"}`)})
	require.True(t, IsInvalid(err))

	got, err := h.service.Get(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "before", got.Name)
	assert.Equal(t, []string{"old"}, got.Tags)
	assert.Empty(t, got.Description)
}

// Every built-in AI evaluator scores with a number, so a saved built-in cannot switch to
// another grading style: the save is refused instead of storing a version that fails
// every run.
func TestService_BuiltinAIKeepsTheNumberGradingStyle(t *testing.T) {
	h := newServiceHarness(t, nil)
	ctx := context.Background()
	installed, err := h.service.Install(ctx, BuiltinToxicity, InstallInput{Provider: "openai", Model: "gpt-4o"})
	require.NoError(t, err)
	require.Equal(t, string(GradingStyleNumber), gjson.GetBytes(installed.Config, "grading_style").String())

	changed, err := sjson.SetBytes(installed.Config, "grading_style", string(GradingStyleYesNo))
	require.NoError(t, err)
	_, err = h.service.Update(ctx, installed.ID, EvaluatorInput{Name: installed.Name, Config: changed})
	require.Error(t, err)
	assert.True(t, IsInvalid(err), "the save reports a caller mistake: %v", err)

	got, err := h.service.Get(ctx, installed.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, got.CurrentVersion, "no version is stored")
	assert.False(t, h.service.Validate(EvaluatorTypeAI, BuiltinToxicity, changed).Valid, "validate gives the same answer")
}

// A rename that comes with a new config is one write: when the new version cannot be stored,
// the old name stays.
func TestService_UpdateWithANewVersionIsOneWrite(t *testing.T) {
	h := newServiceHarness(t, nil)
	ctx := context.Background()
	created, err := h.service.Create(ctx, EvaluatorInput{Name: "before", Type: EvaluatorTypeProgrammatic, Config: json.RawMessage(lengthCheckConfig)})
	require.NoError(t, err)
	h.store.mu.Lock()
	h.store.failVersion = true
	h.store.mu.Unlock()
	_, err = h.service.Update(ctx, created.ID, EvaluatorInput{Name: "after",
		Config: json.RawMessage(`{"code":"def validate(output):\n    return True\n","response_type":"yes_no"}`)})
	require.Error(t, err)
	got, err := h.service.Get(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "before", got.Name, "the rename does not land without its version")
}

func TestService_UpdateVersionsOnlyOnConfigChange(t *testing.T) {
	h := newServiceHarness(t, nil)
	ctx := context.Background()
	created, err := h.service.Create(ctx, EvaluatorInput{Name: "length", Type: EvaluatorTypeProgrammatic, Config: json.RawMessage(lengthCheckConfig)})
	require.NoError(t, err)

	renamed, err := h.service.Update(ctx, created.ID, EvaluatorInput{Name: "length check", Description: "edited", Tags: []string{"a"}})
	require.NoError(t, err)
	assert.Equal(t, "length check", renamed.Name)
	assert.Equal(t, 1, renamed.Version, "a metadata edit is not a new version")

	// The same config sent back, as an editor does on save, changes nothing.
	same, err := h.service.Update(ctx, created.ID, EvaluatorInput{Name: "length check", Config: renamed.Config})
	require.NoError(t, err)
	assert.Equal(t, 1, same.Version)

	changed, err := h.service.Update(ctx, created.ID, EvaluatorInput{
		Name:          "length check",
		Config:        json.RawMessage(`{"code":"def validate(output):\n    return len(output) > 10\n","response_type":"yes_no"}`),
		CommitMessage: "raise the bar",
	})
	require.NoError(t, err)
	assert.Equal(t, 2, changed.Version)
	assert.Empty(t, changed.OptionalVariables, "the view reflects the new code")

	versions, err := h.service.Versions(ctx, created.ID)
	require.NoError(t, err)
	require.Len(t, versions, 2)
	assert.Equal(t, "raise the bar", versions[0].CommitMessage)

	old, err := h.service.Version(ctx, created.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, []string{"minimum"}, old.OptionalVariables, "an old version still reads as it was")

	_, err = h.service.Update(ctx, created.ID, EvaluatorInput{Type: EvaluatorTypeAI})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be changed")

	_, err = h.service.Update(ctx, created.ID, EvaluatorInput{Config: json.RawMessage(`{"code":"","response_type":"yes_no"}`)})
	assert.True(t, IsInvalid(err))
}

// An API evaluator's secret is never shown, and survives an edit that sends the
// placeholder back.
func TestService_APISecretsSurviveAnEdit(t *testing.T) {
	withEncryption(t)
	h := newServiceHarness(t, nil)
	ctx := context.Background()

	created, err := h.service.Create(ctx, EvaluatorInput{Name: "scorer", Type: EvaluatorTypeAPI, Config: json.RawMessage(
		`{"method":"POST","url":"https://scorer.example.com/a","headers":[{"key":"Authorization","value":"Bearer secret-token"}],"output_mapping":{"score":"score"}}`)})
	require.NoError(t, err)
	assert.NotContains(t, string(created.Config), "secret-token")
	assert.Equal(t, RedactedSecret, gjson.GetBytes(created.Config, "headers.0.value").String())

	stored, err := h.store.GetEvaluatorVersion(ctx, created.ID, 1)
	require.NoError(t, err)
	assert.NotContains(t, string(stored.Config), "secret-token", "the database holds the sealed value")

	// The editor changes the URL and sends the redacted config back.
	edited := strings.Replace(string(created.Config), "scorer.example.com/a", "scorer.example.com/b", 1)
	updated, err := h.service.Update(ctx, created.ID, EvaluatorInput{Name: "scorer", Config: json.RawMessage(edited)})
	require.NoError(t, err)
	assert.Equal(t, 2, updated.Version)

	next, err := h.store.GetEvaluatorVersion(ctx, created.ID, 2)
	require.NoError(t, err)
	assert.Equal(t, gjson.GetBytes(stored.Config, "headers.0.value").String(), gjson.GetBytes(next.Config, "headers.0.value").String(), "the sealed secret carried over unchanged")

	versions, err := h.service.Versions(ctx, created.ID)
	require.NoError(t, err)
	for _, version := range versions {
		assert.Equal(t, RedactedSecret, gjson.GetBytes(version.Config, "headers.0.value").String(), "version history is redacted too")
	}
}

// A listing carries what a list shows about each evaluator, without its whole config.
func TestService_ListSummarizesEachEvaluator(t *testing.T) {
	h := newServiceHarness(t, nil)
	ctx := context.Background()

	_, err := h.service.Create(ctx, EvaluatorInput{Name: "judge", Type: EvaluatorTypeAI, Config: json.RawMessage(
		`{"instructions":"Does {{output}} answer {{input}}?","grading_style":"yes_no","provider":"openai","model":"gpt-4o-mini"}`)})
	require.NoError(t, err)
	_, err = h.service.Create(ctx, EvaluatorInput{Name: "scorer", Type: EvaluatorTypeAPI, Config: json.RawMessage(
		`{"method":"POST","url":"https://scorer.example.com/v1/score?tenant=acme","headers":[{"key":"Authorization","value":"Bearer secret"}],"payload":"{\"t\":\"{{output}}\"}","output_mapping":{"score":"score"}}`)})
	require.NoError(t, err)
	_, err = h.service.Install(ctx, BuiltinToxicity, InstallInput{Provider: "anthropic", Model: "claude-sonnet-5-5"})
	require.NoError(t, err)
	_, err = h.service.Install(ctx, BuiltinBLEU, InstallInput{})
	require.NoError(t, err)

	summaries, total, err := h.service.List(ctx, configstore.EvaluatorListParams{})
	require.NoError(t, err)
	require.EqualValues(t, 4, total)
	byName := map[string]EvaluatorSummary{}
	for _, summary := range summaries {
		byName[summary.Name] = summary
	}

	judge := byName["judge"]
	assert.ElementsMatch(t, []string{"input", "output"}, judge.Variables)
	assert.Equal(t, "openai", judge.Provider)
	assert.Equal(t, "gpt-4o-mini", judge.Model)
	require.NotNil(t, judge.PassCriterion)
	assert.Equal(t, OperatorEqual, judge.PassCriterion.Operator)
	assert.Equal(t, "Yes", judge.PassCriterion.Value)

	scorer := byName["scorer"]
	assert.Equal(t, "POST scorer.example.com", scorer.Endpoint, "only the method and host: no path, query or secret")
	assert.Equal(t, []string{"output"}, scorer.Variables)

	toxicity := byName["Toxicity"]
	assert.True(t, toxicity.LowerIsBetter)
	assert.Equal(t, OperatorLessOrEqual, toxicity.PassCriterion.Operator)
	assert.Equal(t, "anthropic", toxicity.Provider)

	bleu := byName["BLEU"]
	assert.Empty(t, bleu.Provider, "a text metric runs in the gateway")
	assert.ElementsMatch(t, []string{"output", "expected_output"}, bleu.Variables)

	listed, err := json.Marshal(summaries)
	require.NoError(t, err)
	assert.NotContains(t, string(listed), "secret")
	assert.NotContains(t, string(listed), "instructions", "the config itself is not in a listing")
}

// Each version records the provider its model calls go to, so a listing can filter on
// "has no eval key" without reading configs.
func TestService_RecordsTheModelProviderOfEachVersion(t *testing.T) {
	h := newServiceHarness(t, []EvalKey{{ID: "k1", Name: "evals", Provider: "openai", Concurrency: 2}})
	ctx := context.Background()
	providerOf := func(id string, version int) string {
		h.store.mu.Lock()
		defer h.store.mu.Unlock()
		return h.store.versions[id][version-1].ModelProvider
	}

	judge, err := h.service.Create(ctx, EvaluatorInput{Name: "judge", Type: EvaluatorTypeAI, Config: json.RawMessage(
		`{"instructions":"Is {{output}} polite?","grading_style":"yes_no","provider":"anthropic","model":"claude-sonnet-5-5"}`)})
	require.NoError(t, err)
	assert.Equal(t, "anthropic", providerOf(judge.ID, 1))

	_, err = h.service.Update(ctx, judge.ID, EvaluatorInput{Name: "judge", Config: json.RawMessage(
		`{"instructions":"Is {{output}} polite?","grading_style":"yes_no","provider":"openai","model":"gpt-4o-mini"}`)})
	require.NoError(t, err)
	assert.Equal(t, "openai", providerOf(judge.ID, 2), "a new version records its own provider")

	code, err := h.service.Create(ctx, EvaluatorInput{Name: "code", Type: EvaluatorTypeProgrammatic, Config: json.RawMessage(lengthCheckConfig)})
	require.NoError(t, err)
	assert.Empty(t, providerOf(code.ID, 1), "an evaluator that calls no model has none")

	toxicity, err := h.service.Install(ctx, BuiltinToxicity, InstallInput{Provider: "openai", Model: "gpt-4o"})
	require.NoError(t, err)
	assert.Equal(t, "openai", providerOf(toxicity.ID, 1))

	copied, err := h.service.Duplicate(ctx, toxicity.ID, "", "")
	require.NoError(t, err)
	assert.Equal(t, "openai", providerOf(copied.ID, 1))
}

// The key status filter needs to know which providers have an eval key. The store does
// not; the service supplies them.
func TestService_ListSuppliesEvalKeyProvidersForTheStatusFilter(t *testing.T) {
	h := newServiceHarness(t, []EvalKey{
		{ID: "k1", Provider: "openai", Concurrency: 1},
		{ID: "k2", Provider: "openai", Concurrency: 1},
		{ID: "k3", Provider: "bedrock", Concurrency: 1},
	})
	ctx := context.Background()

	_, _, err := h.service.List(ctx, configstore.EvaluatorListParams{KeyStatus: configstore.EvaluatorKeyStatusMissing})
	require.NoError(t, err)
	assert.Equal(t, []string{"bedrock", "openai"}, h.store.lastList.KeyProviders, "each provider once")

	_, _, err = h.service.List(ctx, configstore.EvaluatorListParams{})
	require.NoError(t, err)
	assert.Empty(t, h.store.lastList.KeyProviders, "no key lookup without the filter")
}

// The status filter's providers are built in a new slice: a caller's KeyProviders slice
// must not be overwritten through its backing array.
func TestService_ListLeavesTheCallersProviderSliceAlone(t *testing.T) {
	h := newServiceHarness(t, []EvalKey{{ID: "k2", Provider: "p2", Concurrency: 1}})
	callers := []string{"p1"}
	_, _, err := h.service.List(context.Background(), configstore.EvaluatorListParams{KeyStatus: configstore.EvaluatorKeyStatusMissing, KeyProviders: callers})
	require.NoError(t, err)
	assert.Equal(t, "p1", callers[0], "the caller's slice is untouched")
	assert.Equal(t, []string{"p2"}, h.store.lastList.KeyProviders)
}

func TestService_Validate(t *testing.T) {
	h := newServiceHarness(t, nil)

	ok := h.service.Validate(EvaluatorTypeAI, "", json.RawMessage(`{"instructions":"Does {{output}} answer {{input}} given {{context}}?","grading_style":"yes_no","provider":"openai","model":"m","optional_variables":{"context":"docs"}}`))
	require.True(t, ok.Valid, ok.Error)
	assert.Equal(t, []string{"input", "output"}, ok.Variables)
	assert.Equal(t, []string{"context"}, ok.OptionalVariables)
	assert.NotEmpty(t, ok.Config)

	bad := h.service.Validate(EvaluatorTypeProgrammatic, "", json.RawMessage(`{"code":"def validate(output:\n","response_type":"yes_no"}`))
	assert.False(t, bad.Valid)
	assert.Contains(t, bad.Error, "syntax")
	assert.NotNil(t, bad.Variables, "the list is empty, not null, so a client can always iterate it")

	unknown := h.service.Validate("vibes", "", nil)
	assert.False(t, unknown.Valid)
}

// The editor shows a saved API evaluator with its header secrets redacted and checks
// the config as the user types. Validating that config for the saved evaluator puts the
// stored secrets back first, the way a save does, so Save is not blocked by the
// placeholder. The answer stays redacted.
func TestService_ValidateSavedRestoresRedactedSecrets(t *testing.T) {
	withEncryption(t)
	h := newServiceHarness(t, nil)
	ctx := context.Background()

	created, err := h.service.Create(ctx, EvaluatorInput{Name: "scorer", Type: EvaluatorTypeAPI, Config: json.RawMessage(
		`{"method":"POST","url":"https://scorer.example.com/a","headers":[{"key":"Authorization","value":"Bearer secret-token"}],"output_mapping":{"score":"score"}}`)})
	require.NoError(t, err)
	require.Equal(t, RedactedSecret, gjson.GetBytes(created.Config, "headers.0.value").String())

	check, err := h.service.ValidateSaved(ctx, created.ID, EvaluatorTypeAPI, "", created.Config)
	require.NoError(t, err)
	assert.True(t, check.Valid, "the redacted placeholder of a saved secret is accepted: %s", check.Error)
	assert.Equal(t, RedactedSecret, gjson.GetBytes(check.Config, "headers.0.value").String(), "the answer does not reveal the secret")

	fresh := h.service.Validate(EvaluatorTypeAPI, "", created.Config)
	assert.False(t, fresh.Valid, "without a saved evaluator there is no secret to put back")

	_, err = h.service.ValidateSaved(ctx, "missing", EvaluatorTypeAPI, "", created.Config)
	assert.Error(t, err)
}

// ValidateSaved answers what a save would accept, and a save refuses a changed type and
// checks the stored type and builtin key, so validation does the same.
func TestService_ValidateSavedRefusesAChangedType(t *testing.T) {
	h := newServiceHarness(t, nil)
	ctx := context.Background()

	judge, err := h.service.Create(ctx, EvaluatorInput{Name: "judge", Type: EvaluatorTypeAI, Config: json.RawMessage(
		`{"instructions":"Is {{output}} polite?","grading_style":"yes_no","provider":"anthropic","model":"claude-sonnet-5-5"}`)})
	require.NoError(t, err)

	check, err := h.service.ValidateSaved(ctx, judge.ID, EvaluatorTypeHuman, "", json.RawMessage(`{"evaluation_type":"yes_no"}`))
	require.NoError(t, err)
	assert.False(t, check.Valid, "a save with another type is refused, so validation is too")
	assert.Contains(t, check.Error, "type of an evaluator cannot be changed")
	_, err = h.service.Update(ctx, judge.ID, EvaluatorInput{Type: EvaluatorTypeHuman, Config: json.RawMessage(`{"evaluation_type":"yes_no"}`)})
	assert.True(t, IsInvalid(err), "the save refuses it as well")

	check, err = h.service.ValidateSaved(ctx, judge.ID, EvaluatorTypeAI, BuiltinBLEU, judge.Config)
	require.NoError(t, err)
	assert.True(t, check.Valid, "the stored builtin key is used, not the body's: %s", check.Error)
}

func TestService_InstallBuiltin(t *testing.T) {
	h := newServiceHarness(t, nil)
	ctx := context.Background()

	installed, err := h.service.Install(ctx, BuiltinFaithfulness, InstallInput{Provider: "openai", Model: "gpt-4o-mini"})
	require.NoError(t, err)
	assert.Equal(t, "Faithfulness", installed.Name)
	require.NotNil(t, installed.BuiltinKey)
	assert.Equal(t, BuiltinFaithfulness, *installed.BuiltinKey)
	assert.ElementsMatch(t, []string{"input", "output", "context"}, installed.Variables)
	assert.Equal(t, []string{"history"}, installed.OptionalVariables)

	metric, err := h.service.Install(ctx, BuiltinBLEU, InstallInput{Name: "BLEU for release notes"})
	require.NoError(t, err)
	assert.Equal(t, "BLEU for release notes", metric.Name)
	assert.Equal(t, string(EvaluatorTypeStatistical), metric.Type)

	_, err = h.service.Install(ctx, BuiltinClarity, InstallInput{})
	assert.True(t, IsInvalid(err), "a judged built-in needs a model")
	_, err = h.service.Install(ctx, "vibes", InstallInput{})
	assert.ErrorIs(t, err, configstore.ErrNotFound)

	copied, err := h.service.Duplicate(ctx, installed.ID, "", "ada")
	require.NoError(t, err)
	assert.Equal(t, "Faithfulness (copy)", copied.Name)
	assert.Equal(t, BuiltinFaithfulness, *copied.BuiltinKey, "a copy of a built-in is still that built-in")
}

func TestService_ExecuteSavedEvaluator(t *testing.T) {
	h := newServiceHarness(t, nil)
	ctx := context.Background()
	created, err := h.service.Create(ctx, EvaluatorInput{Name: "length", Type: EvaluatorTypeProgrammatic, Config: json.RawMessage(lengthCheckConfig)})
	require.NoError(t, err)

	execution, err := h.service.Execute(ctx, ExecuteInput{EvaluatorID: created.ID, Variables: map[string]string{"output": "hello"}})
	require.NoError(t, err)
	assert.Equal(t, tables.EvalExecutionCompleted, execution.Status)
	require.NotNil(t, execution.Result)
	assert.Equal(t, BoolScore(true), execution.Result.Score)
	require.NotNil(t, execution.Result.Pass)
	assert.True(t, *execution.Result.Pass)
	assert.Equal(t, created.ID, *execution.EvaluatorID)
	assert.Equal(t, 1, execution.EvaluatorVersion)
	assert.NotNil(t, execution.CompletedAt)

	again, err := h.service.Execution(ctx, execution.ID)
	require.NoError(t, err)
	assert.Equal(t, execution.Result.Score, again.Result.Score, "the result can be read back later")

	missing, err := h.service.Execute(ctx, ExecuteInput{EvaluatorID: created.ID})
	require.NoError(t, err)
	assert.Equal(t, ScoreStatusNA, missing.Result.Score.Status, "a missing variable is a result, not a failed call")
	assert.Contains(t, missing.Result.Reasoning, "output")

	_, err = h.service.Execute(ctx, ExecuteInput{EvaluatorID: "missing"})
	assert.ErrorIs(t, err, configstore.ErrNotFound)
	_, err = h.service.Execute(ctx, ExecuteInput{})
	assert.True(t, IsInvalid(err))

	// A negative version is refused, as the runs service refuses it, instead of running
	// the current version.
	h.store.mu.Lock()
	before := len(h.store.executions)
	h.store.mu.Unlock()
	_, err = h.service.Execute(ctx, ExecuteInput{EvaluatorID: created.ID, Version: -1, Variables: map[string]string{"output": "hello"}})
	assert.True(t, IsInvalid(err), "got %v", err)
	h.store.mu.Lock()
	assert.Equal(t, before, len(h.store.executions), "a refused version creates no execution")
	h.store.mu.Unlock()
}

// An editor's test panel runs a config that has not been saved.
func TestService_ExecuteDraft(t *testing.T) {
	h := newServiceHarness(t, nil)
	execution, err := h.service.Execute(context.Background(), ExecuteInput{
		Draft:     &DraftEvaluator{Type: EvaluatorTypeProgrammatic, Config: json.RawMessage(`{"code":"def validate(output):\n    return {\"score\": len(output), \"reasoning\": \"counted\"}\n","response_type":"number"}`)},
		Variables: map[string]string{"output": "four"},
	})
	require.NoError(t, err)
	require.Equal(t, tables.EvalExecutionCompleted, execution.Status)
	assert.Equal(t, NumberScore(4), execution.Result.Score)
	assert.Equal(t, "counted", execution.Result.Reasoning)
	assert.Nil(t, execution.EvaluatorID)

	_, err = h.service.Execute(context.Background(), ExecuteInput{Draft: &DraftEvaluator{Type: EvaluatorTypeProgrammatic, Config: json.RawMessage(`{"code":"oops(","response_type":"number"}`)}})
	assert.True(t, IsInvalid(err), "a broken draft is refused before it is queued")
	_, err = h.service.Execute(context.Background(), ExecuteInput{Draft: &DraftEvaluator{Type: EvaluatorTypeHuman, Config: json.RawMessage(`{"evaluation_type":"yes_no"}`)}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "human evaluator")
}

// The write that closes out an execution whose submit failed has a deadline, so a store that
// stops answering does not hold the API call open.
func TestService_ExecuteFailureWriteHasADeadline(t *testing.T) {
	h := newServiceHarness(t, nil)
	h.store.mu.Lock()
	h.store.holdComplete = make(chan struct{}) // never released: the write only ends at its deadline
	h.store.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, err := h.service.Execute(context.Background(), ExecuteInput{
			Draft:     &DraftEvaluator{Type: EvaluatorTypeAI, Config: json.RawMessage(`{"instructions":"Is {{output}} polite?","grading_style":"yes_no","provider":"openai","model":"gpt-4o-mini"}`)},
			Variables: map[string]string{"output": "thanks"},
		})
		done <- err
	}()
	select {
	case err := <-done:
		// The write never landed, so the execution stays queued and is returned to poll.
		assert.NoError(t, err)
	case <-time.After(completeWriteTimeout + 2*time.Second):
		t.Fatal("Execute waited on the failure write with no deadline")
	}
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	require.NotEmpty(t, h.store.completeHadDeadline, "the failure write was attempted")
	for _, hadDeadline := range h.store.completeHadDeadline {
		assert.True(t, hadDeadline, "the failure write carries a deadline")
	}
}

// When the submit fails and the write that would close the execution fails too, the row
// stays queued and the runner's sweep picks it up later. The caller gets its ID and the
// queued status to poll, not an error that hides an execution that will still finish.
func TestService_ExecuteReturnsAnExecutionItCouldNotClose(t *testing.T) {
	h := newServiceHarness(t, nil)
	h.store.mu.Lock()
	h.store.failComplete = true
	h.store.mu.Unlock()
	view, err := h.service.Execute(context.Background(), ExecuteInput{
		Draft:     &DraftEvaluator{Type: EvaluatorTypeAI, Config: json.RawMessage(`{"instructions":"Is {{output}} polite?","grading_style":"yes_no","provider":"openai","model":"gpt-4o-mini"}`)},
		Variables: map[string]string{"output": "thanks"},
	})
	require.NoError(t, err)
	require.NotNil(t, view)
	assert.NotEmpty(t, view.ID)
	assert.Equal(t, tables.EvalExecutionQueued, view.Status)
}

func TestService_ExecuteAIEvaluatorNeedsAnEvalKey(t *testing.T) {
	const judgeConfig = `{"instructions":"Is {{output}} polite?","grading_style":"yes_no","provider":"openai","model":"gpt-4o-mini"}`

	t.Run("no key marked for evaluations", func(t *testing.T) {
		h := newServiceHarness(t, nil)
		_, err := h.service.Execute(context.Background(), ExecuteInput{
			Draft: &DraftEvaluator{Type: EvaluatorTypeAI, Config: json.RawMessage(judgeConfig)}, Variables: map[string]string{"output": "thanks"},
		})
		require.ErrorIs(t, err, ErrNoEvalKey)
		assert.Empty(t, h.judge.requests, "the model was never called on some other key")
		for _, execution := range h.store.executions {
			assert.Equal(t, tables.EvalExecutionCompleted, execution.Status, "the execution is closed out, not left queued")
			assert.Contains(t, string(execution.Result), "enabled for evaluations")
		}
	})

	t.Run("runs on the eval key", func(t *testing.T) {
		h := newServiceHarness(t, []EvalKey{{ID: "key-evals", Provider: "openai", Concurrency: 2}})
		h.judge.replies = []JudgeResponse{{Text: `{"reasoning":"It says thanks.","score":true}`}}
		execution, err := h.service.Execute(context.Background(), ExecuteInput{
			Draft: &DraftEvaluator{Type: EvaluatorTypeAI, Config: json.RawMessage(judgeConfig)}, Variables: map[string]string{"output": "thanks"},
		})
		require.NoError(t, err)
		require.Equal(t, tables.EvalExecutionCompleted, execution.Status)
		assert.Equal(t, BoolScore(true), execution.Result.Score, execution.Result.Reasoning)
		assert.Equal(t, "key-evals", execution.KeyID)
	})
}

// With no wait, the call returns at once and the result is polled for.
func TestService_ExecuteWithoutWaiting(t *testing.T) {
	h := newServiceHarness(t, nil)
	ctx := context.Background()
	// The result cannot be written yet, so a call that waited would block here.
	release := make(chan struct{})
	h.store.mu.Lock()
	h.store.holdComplete = release
	h.store.mu.Unlock()
	began := time.Now()
	execution, err := h.service.Execute(ctx, ExecuteInput{
		Draft:     &DraftEvaluator{Type: EvaluatorTypeProgrammatic, Config: json.RawMessage(lengthCheckConfig)},
		Variables: map[string]string{"output": "hello"},
		WaitMs:    new(0),
	})
	require.NoError(t, err)
	require.NotEmpty(t, execution.ID)
	assert.Less(t, time.Since(began), time.Second, "wait_ms 0 returns at once")
	assert.NotEqual(t, tables.EvalExecutionCompleted, execution.Status, "it returns before the result")

	// The blocked write is let through, so the run that is already waiting on it stores
	// the result; no second run is needed.
	close(release)
	require.Eventually(t, func() bool {
		polled, err := h.service.Execution(ctx, execution.ID)
		return err == nil && polled.Status == tables.EvalExecutionCompleted && polled.Result != nil
	}, 3*time.Second, 10*time.Millisecond)
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	assert.Equal(t, 1, h.store.completions, "one result was stored")
	assert.Equal(t, 1, h.store.starts, "the job ran once")
}

// publishThenFailQueue stores a published message and then reports a failure, as a
// queue does when the write landed but the acknowledgement was lost.
type publishThenFailQueue struct {
	queue.Queue
}

func (q *publishThenFailQueue) Publish(ctx context.Context, messages ...*queue.Message) error {
	if err := q.Queue.Publish(ctx, messages...); err != nil {
		return err
	}
	return errors.New("the publish acknowledgement was lost")
}

// A publish that fails after the message became durable must not close the execution
// with an error: the message runs, and its result is the one that stands. The caller
// gets the execution back so it can poll it, since the job runs or the sweep
// publishes it again.
func TestService_ExecuteLeavesAnUncertainPublishQueued(t *testing.T) {
	h := newServiceHarnessOn(t, nil, func(q queue.Queue) queue.Queue { return &publishThenFailQueue{Queue: q} })
	ctx := context.Background()
	created, err := h.service.Create(ctx, EvaluatorInput{Name: "length", Type: EvaluatorTypeProgrammatic, Config: json.RawMessage(lengthCheckConfig)})
	require.NoError(t, err)
	execution, err := h.service.Execute(ctx, ExecuteInput{EvaluatorID: created.ID, Variables: map[string]string{"output": "hello"}, WaitMs: new(0)})
	require.NoError(t, err, "an uncertain publish is not a failed execution")
	require.NotNil(t, execution, "the caller gets an execution id to poll")
	require.NotEmpty(t, execution.ID)

	h.store.mu.Lock()
	var id string
	for executionID := range h.store.executions {
		id = executionID
	}
	h.store.mu.Unlock()
	assert.Equal(t, id, execution.ID)
	require.Eventually(t, func() bool {
		view, err := h.service.Execution(ctx, id)
		return err == nil && view.Status == tables.EvalExecutionCompleted
	}, 5*time.Second, 10*time.Millisecond)
	view, err := h.service.Execution(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, BoolScore(true), view.Result.Score, "the evaluator's own result is stored, not a submission error: %s", view.Result.Reasoning)

	// A caller outside this package tells the uncertain publish apart by ErrPlacementUnknown.
	err = h.service.runner.Submit(ctx, &Job{ID: "uncertain", Exec: func(context.Context) Result { return Result{Score: NAScore()} }})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPlacementUnknown, "an uncertain publish matches ErrPlacementUnknown")
	assert.NotErrorIs(t, err, ErrNoEvalKey, "an uncertain publish must not match ErrNoEvalKey")
}

// The wait bounds the whole call, store reads included: a store that stops answering
// does not hold the request open past wait_ms.
func TestService_ExecuteWaitBoundsStoreReads(t *testing.T) {
	h := newServiceHarness(t, nil)
	h.store.mu.Lock()
	h.store.hangGet = true
	h.store.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, err := h.service.Execute(context.Background(), ExecuteInput{
			Draft:     &DraftEvaluator{Type: EvaluatorTypeProgrammatic, Config: json.RawMessage(lengthCheckConfig)},
			Variables: map[string]string{"output": "hello"},
			WaitMs:    new(100),
		})
		done <- err
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Execute blocked on a store read past its wait")
	}
}

// With no wait the single status read still has a deadline: a request context has none,
// and a store that stops answering must not hold the call, which would hide the ID of an
// execution that is already queued.
func TestService_ExecuteWithNoWaitBoundsItsStatusRead(t *testing.T) {
	h := newServiceHarness(t, nil)
	h.store.mu.Lock()
	h.store.hangGet = true
	h.store.mu.Unlock()
	type outcome struct {
		view *ExecutionView
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		view, err := h.service.Execute(context.Background(), ExecuteInput{
			Draft:     &DraftEvaluator{Type: EvaluatorTypeProgrammatic, Config: json.RawMessage(lengthCheckConfig)},
			Variables: map[string]string{"output": "hello"},
			WaitMs:    new(0),
		})
		done <- outcome{view, err}
	}()
	select {
	case got := <-done:
		require.NoError(t, got.err)
		require.NotNil(t, got.view)
		h.store.mu.Lock()
		_, stored := h.store.executions[got.view.ID]
		h.store.mu.Unlock()
		assert.True(t, stored, "the returned ID is the inserted execution")
		assert.Equal(t, tables.EvalExecutionQueued, got.view.Status)
	case <-time.After(completeWriteTimeout + 2*time.Second):
		t.Fatal("Execute with no wait blocked on a store read with no deadline")
	}
}

// drainedCtx is the state a *fasthttp.RequestCtx reaches after a shutdown drains the
// server's connections: Done() is the closed server channel, while Err() is back to nil.
// A context derived from it with WithTimeout or AfterFunc panics with "missing cancel
// error" once its watcher sees the closed channel.
type drainedCtx struct{ done chan struct{} }

func newDrainedCtx() drainedCtx {
	done := make(chan struct{})
	close(done)
	return drainedCtx{done: done}
}

func (drainedCtx) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c drainedCtx) Done() <-chan struct{}     { return c.done }
func (drainedCtx) Err() error                  { return nil }
func (drainedCtx) Value(any) any               { return nil }

// Execute is called with the handler's request context. After a shutdown drains the
// connections that context has a closed Done() and a nil Err(), and nothing derived from
// it may start a watcher, or the process panics.
func TestService_ExecuteSurvivesADrainedRequestContext(t *testing.T) {
	for _, wait := range []int{0, 50} {
		t.Run(fmt.Sprintf("wait %dms", wait), func(t *testing.T) {
			h := newServiceHarness(t, nil)
			created, err := h.service.Create(context.Background(), EvaluatorInput{Name: "length", Type: EvaluatorTypeProgrammatic, Config: json.RawMessage(lengthCheckConfig)})
			require.NoError(t, err)
			require.NotPanics(t, func() {
				_, _ = h.service.Execute(newDrainedCtx(), ExecuteInput{EvaluatorID: created.ID, Variables: map[string]string{"output": "hello"}, WaitMs: new(wait)})
			})
		})
	}
}

// A read that fails after the execution was queued must not hide the execution: the job
// is stored and will run, so the caller gets its ID to poll, with or without a wait.
func TestService_ExecuteReturnsTheQueuedExecutionWhenAReadFails(t *testing.T) {
	for _, wait := range []int{0, 200} {
		t.Run(fmt.Sprintf("wait %dms", wait), func(t *testing.T) {
			h := newServiceHarness(t, nil)
			h.store.mu.Lock()
			h.store.failGet = true
			h.store.mu.Unlock()
			view, err := h.service.Execute(context.Background(), ExecuteInput{
				Draft:     &DraftEvaluator{Type: EvaluatorTypeProgrammatic, Config: json.RawMessage(lengthCheckConfig)},
				Variables: map[string]string{"output": "hello"},
				WaitMs:    new(wait),
			})
			require.NoError(t, err)
			require.NotNil(t, view)
			assert.NotEmpty(t, view.ID)
			assert.Equal(t, tables.EvalExecutionQueued, view.Status)
			h.store.mu.Lock()
			_, stored := h.store.executions[view.ID]
			h.store.mu.Unlock()
			assert.True(t, stored, "the returned ID is the stored execution")
		})
	}
}

func TestWaitFor(t *testing.T) {
	assert.Equal(t, DefaultExecutionWait, waitFor(nil))
	assert.Equal(t, time.Duration(0), waitFor(new(0)))
	assert.Equal(t, time.Duration(0), waitFor(new(-5)))
	assert.Equal(t, 1500*time.Millisecond, waitFor(new(1500)))
	assert.Equal(t, MaxExecutionWait, waitFor(new(10_000_000)))
	assert.Equal(t, MaxExecutionWait, waitFor(new(288230376151711744)), "a huge wait must clamp, not wrap to 0")
}
