package handlers

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/evals/datasets"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

// datasetTestLogs is a log store holding a fixed set of logs.
type datasetTestLogs struct {
	logs     map[string]*logstore.Log
	sessions map[string][]string
}

func (l *datasetTestLogs) FindByID(_ context.Context, id string) (*logstore.Log, error) {
	log, ok := l.logs[id]
	if !ok {
		return nil, fmt.Errorf("log %s not found", id)
	}
	return log, nil
}

func (l *datasetTestLogs) SearchLogs(_ context.Context, filters logstore.SearchFilters, pagination logstore.PaginationOptions) (*logstore.SearchResult, error) {
	result := &logstore.SearchResult{}
	ids := l.sessions[filters.SessionID]
	for i := pagination.Offset; i < len(ids) && i < pagination.Offset+pagination.Limit; i++ {
		result.Logs = append(result.Logs, logstore.Log{ID: ids[i]})
	}
	return result, nil
}

func datasetTestLog(id, question, answer string) *logstore.Log {
	return &logstore.Log{
		ID: id,
		InputHistoryParsed: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new(question)}},
		},
		OutputMessageParsed: &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: new(answer)}},
	}
}

// newEvalsDatasetsTestAPI serves the dataset routes over a real SQLite config store,
// so the migration, the store, the service and the handler are exercised together.
func newEvalsDatasetsTestAPI(t *testing.T, logs datasets.LogReader) *evalsTestAPI {
	t.Helper()
	store, err := configstore.NewConfigStore(context.Background(), &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "datasets.db")},
	}, &mockLogger{})
	require.NoError(t, err)
	r := router.New()
	NewEvalsDatasetsHandler(datasets.NewService(store, logs)).RegisterRoutes(r)
	return &evalsTestAPI{t: t, router: r}
}

// column returns the ID of a dataset's column by name.
func (a *evalsTestAPI) column(datasetID, name string) string {
	a.t.Helper()
	status, body := a.do("GET", "/api/evals/datasets/"+datasetID, "")
	require.Equal(a.t, fasthttp.StatusOK, status, string(body))
	id := gjson.GetBytes(body, `dataset.columns.#(name=="`+name+`").id`).String()
	require.NotEmpty(a.t, id, "no column named %s", name)
	return id
}

func TestEvalsDatasetsHandler_Lifecycle(t *testing.T) {
	api := newEvalsDatasetsTestAPI(t, nil)

	status, body := api.do("POST", "/api/evals/datasets", `{"name":"Support","template":"qa","tags":["golden"]}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	id := gjson.GetBytes(body, "dataset.id").String()
	require.NotEmpty(t, id)
	assert.Equal(t, int64(2), gjson.GetBytes(body, "dataset.columns.#").Int())
	assert.Equal(t, int64(0), gjson.GetBytes(body, "dataset.head_version").Int())

	status, body = api.do("POST", "/api/evals/datasets", `{"name":"Support"}`)
	assert.Equal(t, fasthttp.StatusConflict, status, "a taken name is a conflict: %s", body)
	status, body = api.do("POST", "/api/evals/datasets", `{"name":"x","template":"nope"}`)
	assert.Equal(t, fasthttp.StatusBadRequest, status, string(body))
	status, _ = api.do("GET", "/api/evals/datasets/missing", "")
	assert.Equal(t, fasthttp.StatusNotFound, status)

	// Rows: add by column name, patch one cell, read back.
	status, body = api.do("POST", "/api/evals/datasets/"+id+"/rows", `{"rows":[{"input":"reset password?","expected_output":"use the link"},{"input":"invoice?"}]}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, int64(2), gjson.GetBytes(body, "count").Int())
	rowID := gjson.GetBytes(body, "rows.1.id").String()
	require.NotEmpty(t, rowID)

	status, body = api.do("PATCH", "/api/evals/datasets/"+id+"/rows/"+rowID, `{"cells":{"expected_output":"in billing"}}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	status, body = api.do("GET", "/api/evals/datasets/"+id+"/rows/"+rowID+"/variables", "")
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, "invoice?", gjson.GetBytes(body, "variables.input").String())
	assert.Equal(t, "in billing", gjson.GetBytes(body, "variables.expected_output").String())

	status, body = api.do("GET", "/api/evals/datasets/"+id+"/rows?limit=1", "")
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, int64(1), gjson.GetBytes(body, "rows.#").Int())
	assert.Equal(t, int64(2), gjson.GetBytes(body, "total").Int())
	next := gjson.GetBytes(body, "next_after").Int()
	require.NotZero(t, next)
	status, body = api.do("GET", fmt.Sprintf("/api/evals/datasets/%s/rows?limit=1&after=%d", id, next), "")
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, rowID, gjson.GetBytes(body, "rows.0.id").String())
	assert.Equal(t, int64(0), gjson.GetBytes(body, "next_after").Int())

	status, body = api.do("GET", "/api/evals/datasets/"+id+"/rows?limit=1&offset=1", "")
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, rowID, gjson.GetBytes(body, "rows.0.id").String(), "offset reads a page by row position")
	status, body = api.do("GET", "/api/evals/datasets/"+id+"/rows?search=invoice", "")
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, int64(1), gjson.GetBytes(body, "total").Int(), "the total counts the rows the search matches")

	// A paste: change cells of existing rows and add a row in one call.
	status, body = api.do("POST", "/api/evals/datasets/"+id+"/rows/batch",
		fmt.Sprintf(`{"updates":[{"id":%q,"cells":{"input":"invoice location?"}}],"rows":[{"input":"refund?"}]}`, rowID))
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, rowID, gjson.GetBytes(body, "updated.0.id").String())
	assert.Equal(t, int64(1), gjson.GetBytes(body, "added.#").Int())
	pastedID := gjson.GetBytes(body, "added.0.id").String()
	require.NotEmpty(t, pastedID)
	status, body = api.do("POST", "/api/evals/datasets/"+id+"/rows/batch", `{}`)
	assert.Equal(t, fasthttp.StatusBadRequest, status, string(body))
	status, body = api.do("POST", "/api/evals/datasets/"+id+"/rows/batch", `{"updates":[{"id":"missing","cells":{"input":"x"}}]}`)
	assert.Equal(t, fasthttp.StatusNotFound, status, string(body))
	status, body = api.do("PATCH", "/api/evals/datasets/"+id+"/rows/"+rowID, `{"cells":{"input":"invoice?"}}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	status, body = api.do("POST", "/api/evals/datasets/"+id+"/rows/delete", fmt.Sprintf(`{"row_ids":[%q]}`, pastedID))
	require.Equal(t, fasthttp.StatusOK, status, string(body))

	// Columns.
	status, body = api.do("POST", "/api/evals/datasets/"+id+"/columns", `{"name":"context","type":"variable"}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	contextCol := gjson.GetBytes(body, "column.id").String()
	status, body = api.do("POST", "/api/evals/datasets/"+id+"/columns", `{"name":"output","type":"variable"}`)
	assert.Equal(t, fasthttp.StatusBadRequest, status, "a variable column cannot shadow a typed variable: %s", body)
	status, body = api.do("PUT", "/api/evals/datasets/"+id+"/columns/order",
		fmt.Sprintf(`{"column_ids":[%q,%q,%q]}`, contextCol, api.column(id, "input"), api.column(id, "expected_output")))
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	status, body = api.do("PUT", "/api/evals/datasets/"+id+"/columns/"+contextCol, `{"name":"docs","type":"variable"}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	status, body = api.do("GET", "/api/evals/datasets/"+id, "")
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, "docs", gjson.GetBytes(body, "dataset.columns.0.name").String())
	assert.Equal(t, int64(2), gjson.GetBytes(body, "dataset.row_count").Int())

	// Splits.
	status, body = api.do("POST", "/api/evals/datasets/"+id+"/splits", `{"name":"smoke"}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	splitID := gjson.GetBytes(body, "split.id").String()
	status, body = api.do("POST", "/api/evals/datasets/"+id+"/splits/"+splitID+"/rows", fmt.Sprintf(`{"add":[%q]}`, rowID))
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, int64(1), gjson.GetBytes(body, "added").Int())
	status, body = api.do("GET", "/api/evals/datasets/"+id+"/rows?split="+splitID, "")
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, int64(1), gjson.GetBytes(body, "total").Int())
	status, body = api.do("POST", "/api/evals/datasets/"+id+"/splits", `{"name":"smoke"}`)
	assert.Equal(t, fasthttp.StatusConflict, status, string(body))

	// Folders: create, move the dataset in, rename, and delete without losing the dataset.
	status, body = api.do("POST", "/api/evals/datasets/folders", `{"name":"Tickets"}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	folderID := gjson.GetBytes(body, "folder.id").String()
	require.NotEmpty(t, folderID)
	status, body = api.do("POST", "/api/evals/datasets/folders", `{"name":"Tickets"}`)
	assert.Equal(t, fasthttp.StatusConflict, status, string(body))
	status, body = api.do("PUT", "/api/evals/datasets/"+id+"/folder", fmt.Sprintf(`{"folder_id":%q}`, folderID))
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, folderID, gjson.GetBytes(body, "dataset.folder_id").String())
	status, body = api.do("PUT", "/api/evals/datasets/folders/"+folderID, `{"name":"Support tickets"}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	status, body = api.do("GET", "/api/evals/datasets/folders", "")
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, "Support tickets", gjson.GetBytes(body, "folders.0.name").String())
	status, body = api.do("POST", "/api/evals/datasets/folders", fmt.Sprintf(`{"name":"Nested","parent_id":%q}`, folderID))
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	nestedID := gjson.GetBytes(body, "folder.id").String()
	assert.Equal(t, folderID, gjson.GetBytes(body, "folder.parent_id").String())
	status, body = api.do("PUT", "/api/evals/datasets/folders/"+folderID+"/parent", fmt.Sprintf(`{"parent_id":%q}`, nestedID))
	assert.Equal(t, fasthttp.StatusBadRequest, status, "a folder cannot move below itself: %s", body)
	status, body = api.do("PUT", "/api/evals/datasets/folders/"+nestedID+"/parent", `{"parent_id":null}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.False(t, gjson.GetBytes(body, "folder.parent_id").Exists())
	status, body = api.do("DELETE", "/api/evals/datasets/folders/"+nestedID, "")
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	status, body = api.do("DELETE", "/api/evals/datasets/folders/"+folderID, "")
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	status, body = api.do("GET", "/api/evals/datasets/"+id, "")
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.False(t, gjson.GetBytes(body, "dataset.folder_id").Exists(), "the dataset outlives its folder")
	status, body = api.do("PUT", "/api/evals/datasets/"+id+"/folder", `{"folder_id":null}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))

	// Listing, duplicate, delete.
	// Listing, duplicate, delete.
	status, body = api.do("GET", "/api/evals/datasets?search=supp&tag=golden", "")
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, int64(1), gjson.GetBytes(body, "total").Int())
	assert.Equal(t, int64(3), gjson.GetBytes(body, "datasets.0.column_count").Int())
	assert.Equal(t, int64(2), gjson.GetBytes(body, "datasets.0.row_count").Int())
	assert.Equal(t, int64(1), gjson.GetBytes(body, "datasets.0.split_count").Int())

	status, body = api.do("POST", "/api/evals/datasets/"+id+"/duplicate", `{"name":"Support v2"}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, int64(2), gjson.GetBytes(body, "dataset.row_count").Int())
	assert.Equal(t, int64(1), gjson.GetBytes(body, "dataset.splits.0.row_count").Int())

	status, _ = api.do("DELETE", "/api/evals/datasets/"+id, "")
	require.Equal(t, fasthttp.StatusOK, status)
	status, _ = api.do("GET", "/api/evals/datasets/"+id, "")
	assert.Equal(t, fasthttp.StatusNotFound, status)
}

func TestEvalsDatasetsHandler_ImportExportAndVersions(t *testing.T) {
	api := newEvalsDatasetsTestAPI(t, nil)
	status, body := api.do("POST", "/api/evals/datasets", `{"name":"Imported","template":"qa"}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	id := gjson.GetBytes(body, "dataset.id").String()
	base := "/api/evals/datasets/" + id

	status, body = api.do("POST", base+"/versions", "")
	assert.Equal(t, fasthttp.StatusConflict, status, "nothing to save yet: %s", body)

	file := "input,expected_output,persona\n\"hello, world\",hi,admin\nsecond,,guest\n"
	status, body = api.do("POST", base+"/import/preview", file)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, "persona", gjson.GetBytes(body, "header.2").String())
	assert.Equal(t, api.column(id, "input"), gjson.GetBytes(body, "mapping.0").String())
	assert.Equal(t, "", gjson.GetBytes(body, "mapping.2").String())

	// An explicit mapping travels as a query parameter; the body is the file.
	mapping := fmt.Sprintf(`[{"index":0,"column_id":%q},{"index":1,"column_id":%q},{"index":2,"name":"persona","type":"variable"}]`,
		api.column(id, "input"), api.column(id, "expected_output"))
	status, body = api.do("POST", base+"/import?mapping="+url.QueryEscape(mapping), file)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, int64(2), gjson.GetBytes(body, "rows_added").Int())
	assert.Equal(t, "persona", gjson.GetBytes(body, "columns_created.0").String())
	assert.Equal(t, int64(1), gjson.GetBytes(body, "version").Int(), "an import saves a version")

	status, body = api.do("POST", base+"/import", "input,expected_output\n")
	assert.Equal(t, fasthttp.StatusBadRequest, status, "a file with no rows: %s", body)

	status, body = api.do("GET", base+"/export", "")
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, file, string(body), "the export is the file that was imported")
	status, body = api.do("GET", base+"/export?format=jsonl", "")
	require.Equal(t, fasthttp.StatusOK, status)
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	require.Len(t, lines, 2)
	assert.JSONEq(t, `{"input":"hello, world","expected_output":"hi","persona":"admin"}`, lines[0])
	status, _ = api.do("GET", base+"/export?format=xlsx", "")
	assert.Equal(t, fasthttp.StatusBadRequest, status)
	status, _ = api.do("GET", base+"/export?version=7", "")
	assert.Equal(t, fasthttp.StatusNotFound, status, "an unknown version is a 404, not an empty file")

	// Edit, compare the draft, save, restore.
	status, body = api.do("GET", base+"/rows", "")
	require.Equal(t, fasthttp.StatusOK, status)
	rowID := gjson.GetBytes(body, "rows.0.id").String()
	status, body = api.do("POST", base+"/rows/delete", fmt.Sprintf(`{"row_ids":[%q]}`, rowID))
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, int64(1), gjson.GetBytes(body, "deleted").Int())

	status, body = api.do("GET", base+"/versions/draft/diff", "")
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, int64(1), gjson.GetBytes(body, "from").Int())
	assert.Equal(t, "removed", gjson.GetBytes(body, "rows.0.change").String())

	status, body = api.do("POST", base+"/versions", `{"name":"trimmed","description":"dropped the greeting"}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, int64(2), gjson.GetBytes(body, "version.number").Int())
	assert.Equal(t, int64(1), gjson.GetBytes(body, "version.rows_removed").Int())

	status, body = api.do("GET", base+"/rows?version=1", "")
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, int64(2), gjson.GetBytes(body, "total").Int(), "version 1 still has both rows")

	status, body = api.do("GET", base+"/versions/2/diff?against=1", "")
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, int64(1), gjson.GetBytes(body, "rows.#").Int())
	status, _ = api.do("GET", base+"/versions/1/diff?against=2", "")
	assert.Equal(t, fasthttp.StatusBadRequest, status)

	status, body = api.do("PUT", base+"/versions/1", `{"name":"baseline"}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	status, body = api.do("POST", base+"/versions/1/restore", "")
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, int64(3), gjson.GetBytes(body, "version.number").Int())
	assert.Equal(t, int64(1), gjson.GetBytes(body, "version.restored_from").Int())

	status, body = api.do("GET", base+"/versions", "")
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, int64(3), gjson.GetBytes(body, "versions.#").Int())
	assert.Equal(t, "baseline", gjson.GetBytes(body, "versions.2.name").String())

	status, body = api.do("DELETE", base+"/versions/3", "")
	assert.Equal(t, fasthttp.StatusConflict, status, "the latest version stays: %s", body)
	status, _ = api.do("DELETE", base+"/versions/2", "")
	require.Equal(t, fasthttp.StatusOK, status)
	status, _ = api.do("GET", base+"/versions/2", "")
	assert.Equal(t, fasthttp.StatusNotFound, status)

	// A version cap removes the oldest versions.
	status, body = api.do("PUT", base, `{"name":"Imported","max_versions":1}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	status, body = api.do("GET", base+"/versions", "")
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Equal(t, int64(1), gjson.GetBytes(body, "versions.#").Int())
	status, body = api.do("PUT", base, `{"name":"Imported","max_versions":0}`)
	assert.Equal(t, fasthttp.StatusBadRequest, status, string(body))
}

func TestEvalsDatasetsHandler_RowsFromLogsAndSessions(t *testing.T) {
	without := newEvalsDatasetsTestAPI(t, nil)
	status, body := without.do("POST", "/api/evals/datasets", `{"name":"No logs","template":"qa"}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	status, body = without.do("POST", "/api/evals/datasets/"+gjson.GetBytes(body, "dataset.id").String()+"/rows/from-logs", `{"log_ids":["a"]}`)
	assert.Equal(t, fasthttp.StatusBadRequest, status, "no log store: %s", body)

	logs := &datasetTestLogs{
		logs: map[string]*logstore.Log{
			"log-1": datasetTestLog("log-1", "weather?", "Sunny."),
			"log-2": datasetTestLog("log-2", "and tomorrow?", "Rain."),
			"empty": {ID: "empty"},
		},
		sessions: map[string][]string{"sess-1": {"log-1", "log-2"}},
	}
	api := newEvalsDatasetsTestAPI(t, logs)
	status, body = api.do("POST", "/api/evals/datasets", `{"name":"Curated","columns":[{"name":"input","type":"input"},{"name":"output","type":"output"}]}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	id := gjson.GetBytes(body, "dataset.id").String()
	base := "/api/evals/datasets/" + id

	status, body = api.do("POST", base+"/rows/from-logs", `{"log_ids":["log-1","empty","gone"]}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, int64(1), gjson.GetBytes(body, "rows_added").Int())
	assert.Equal(t, int64(2), gjson.GetBytes(body, "skipped.#").Int())
	assert.Equal(t, int64(1), gjson.GetBytes(body, "version").Int())

	status, body = api.do("POST", base+"/rows/from-sessions", `{"session_ids":["sess-1","sess-none"]}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, int64(1), gjson.GetBytes(body, "rows_added").Int())
	assert.Equal(t, "sess-none", gjson.GetBytes(body, "skipped.0.id").String())

	status, body = api.do("GET", base+"/rows", "")
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	require.Equal(t, int64(2), gjson.GetBytes(body, "total").Int())
	sessionRow := gjson.GetBytes(body, "rows.1.id").String()
	status, body = api.do("GET", base+"/rows/"+sessionRow+"/variables", "")
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	assert.Equal(t, "weather?", gjson.GetBytes(body, "variables.input").String())
	assert.Equal(t, "Rain.", gjson.GetBytes(body, "variables.output").String())
	session := gjson.GetBytes(body, "variables.session").String()
	assert.Equal(t, int64(2), gjson.Get(session, "#").Int())
	assert.Equal(t, "and tomorrow?", gjson.Get(session, "1.request").String())
}

func TestEvalsDatasetsHandler_Files(t *testing.T) {
	api := newEvalsDatasetsTestAPI(t, nil)
	status, body := api.do("POST", "/api/evals/datasets", `{"name":"Vision","template":"qa","columns":[{"name":"image","type":"file"}]}`)
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	id := gjson.GetBytes(body, "dataset.id").String()

	// call sends a request with a content type and returns the whole response.
	call := func(method, path, contentType string, payload []byte) *fasthttp.RequestCtx {
		var req fasthttp.Request
		req.Header.SetMethod(method)
		req.SetRequestURI(path)
		if contentType != "" {
			req.Header.SetContentType(contentType)
		}
		req.SetBody(payload)
		ctx := &fasthttp.RequestCtx{}
		ctx.Init(&req, nil, nil)
		api.router.Handler(ctx)
		return ctx
	}

	missingName := call("POST", "/api/evals/datasets/"+id+"/files", "image/png", []byte("png bytes"))
	assert.Equal(t, fasthttp.StatusBadRequest, missingName.Response.StatusCode())
	uploaded := call("POST", "/api/evals/datasets/"+id+"/files?name="+url.QueryEscape("cat photo.png"), "image/png", []byte("png bytes"))
	require.Equal(t, fasthttp.StatusOK, uploaded.Response.StatusCode(), string(uploaded.Response.Body()))
	fileID := gjson.GetBytes(uploaded.Response.Body(), "file.id").String()
	require.NotEmpty(t, fileID)
	assert.Equal(t, "cat photo.png", gjson.GetBytes(uploaded.Response.Body(), "file.name").String())
	fileURL := gjson.GetBytes(uploaded.Response.Body(), "file.url").String()
	assert.Equal(t, "/api/evals/datasets/"+id+"/files/"+fileID, fileURL)

	// An image is served to be shown in place, with the type it was uploaded as.
	served := call("GET", fileURL, "", nil)
	require.Equal(t, fasthttp.StatusOK, served.Response.StatusCode())
	assert.Equal(t, "png bytes", string(served.Response.Body()))
	assert.Equal(t, "image/png", string(served.Response.Header.ContentType()))
	assert.Equal(t, "nosniff", string(served.Response.Header.Peek("X-Content-Type-Options")))
	assert.Contains(t, string(served.Response.Header.Peek("Content-Disposition")), "inline")

	// Anything a browser could run is served as a download, never as a page of this origin.
	page := call("POST", "/api/evals/datasets/"+id+"/files?name=x.html", "text/html", []byte("<script>alert(1)</script>"))
	require.Equal(t, fasthttp.StatusOK, page.Response.StatusCode())
	pageServed := call("GET", gjson.GetBytes(page.Response.Body(), "file.url").String(), "", nil)
	assert.Contains(t, string(pageServed.Response.Header.Peek("Content-Disposition")), "attachment")
	assert.Equal(t, "application/octet-stream", string(pageServed.Response.Header.ContentType()))
	assert.Contains(t, string(pageServed.Response.Header.Peek("Content-Security-Policy")), "sandbox")

	assert.Equal(t, fasthttp.StatusNotFound, call("GET", "/api/evals/datasets/"+id+"/files/missing", "", nil).Response.StatusCode())

	// The file goes into a row's file cell, and comes back as a list.
	status, body = api.do("POST", "/api/evals/datasets/"+id+"/rows",
		fmt.Sprintf(`{"rows":[{"input":"what is this","image":"[{\"id\":\"%s\",\"name\":\"cat photo.png\",\"content_type\":\"image/png\"}]"},{"image":"https://example.com/dog.png"}]}`, fileID))
	require.Equal(t, fasthttp.StatusOK, status, string(body))
	second := gjson.Parse(gjson.GetBytes(body, "rows.1.cells."+api.column(id, "image")).String())
	assert.Equal(t, "dog.png", second.Get("0.name").String())
	status, body = api.do("POST", "/api/evals/datasets/"+id+"/rows", `{"rows":[{"image":"not a url"}]}`)
	assert.Equal(t, fasthttp.StatusBadRequest, status, string(body))
}
