package gigachat

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func TestToGigaChatFilePurpose(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		purpose schemas.FilePurpose
		want    string
	}{
		{name: "assistants maps to assistant", purpose: schemas.FilePurposeAssistants, want: gigaChatFilePurposeAssistant},
		{name: "batch maps to general", purpose: schemas.FilePurposeBatch, want: gigaChatFilePurposeGeneral},
		{name: "fine tune maps to general", purpose: schemas.FilePurposeFineTune, want: gigaChatFilePurposeGeneral},
		{name: "vision maps to general", purpose: schemas.FilePurposeVision, want: gigaChatFilePurposeGeneral},
		{name: "responses maps to general", purpose: schemas.FilePurposeResponses, want: gigaChatFilePurposeGeneral},
		{name: "evals maps to general", purpose: schemas.FilePurposeEvals, want: gigaChatFilePurposeGeneral},
		{name: "user data maps to general", purpose: schemas.FilePurposeUserData, want: gigaChatFilePurposeGeneral},
		{name: "batch output maps to general", purpose: schemas.FilePurposeBatchOutput, want: gigaChatFilePurposeGeneral},
		{name: "empty maps to general", purpose: "", want: gigaChatFilePurposeGeneral},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := toGigaChatFilePurpose(tt.purpose); got != tt.want {
				t.Fatalf("toGigaChatFilePurpose(%q) = %q, want %q", tt.purpose, got, tt.want)
			}
		})
	}
}

func TestToBifrostFilePurpose(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		gigaChatPurpose  string
		requestedPurpose schemas.FilePurpose
		want             schemas.FilePurpose
	}{
		{
			name:            "assistant maps to assistants",
			gigaChatPurpose: gigaChatFilePurposeAssistant,
			want:            schemas.FilePurposeAssistants,
		},
		{
			name:            "general defaults to user data",
			gigaChatPurpose: gigaChatFilePurposeGeneral,
			want:            schemas.FilePurposeUserData,
		},
		{
			name:             "general keeps requested purpose when available",
			gigaChatPurpose:  gigaChatFilePurposeGeneral,
			requestedPurpose: schemas.FilePurposeBatch,
			want:             schemas.FilePurposeBatch,
		},
		{
			name: "empty defaults to user data",
			want: schemas.FilePurposeUserData,
		},
		{
			name:            "unknown purpose is preserved",
			gigaChatPurpose: "custom_purpose",
			want:            schemas.FilePurpose("custom_purpose"),
		},
		{
			name:            "general trims whitespace",
			gigaChatPurpose: " general ",
			want:            schemas.FilePurposeUserData,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := toBifrostFilePurpose(tt.gigaChatPurpose, tt.requestedPurpose)
			if got != tt.want {
				t.Fatalf("toBifrostFilePurpose(%q, %q) = %q, want %q", tt.gigaChatPurpose, tt.requestedPurpose, got, tt.want)
			}
		})
	}
}

func TestGigaChatFileTypesJSON(t *testing.T) {
	t.Parallel()

	accessPolicy := "public"
	uploaded := GigaChatUploadedFile{
		ID:           "file-1",
		Object:       "file",
		Bytes:        123,
		CreatedAt:    1780306293,
		Filename:     "document.txt",
		Purpose:      gigaChatFilePurposeGeneral,
		AccessPolicy: &accessPolicy,
	}

	raw, err := json.Marshal(GigaChatUploadedFiles{Data: []GigaChatUploadedFile{uploaded}})
	if err != nil {
		t.Fatalf("marshal uploaded files: %v", err)
	}

	var decoded GigaChatUploadedFiles
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal uploaded files: %v", err)
	}
	if len(decoded.Data) != 1 {
		t.Fatalf("decoded %d files, want 1", len(decoded.Data))
	}
	if decoded.Data[0].ID != uploaded.ID || decoded.Data[0].Purpose != uploaded.Purpose {
		t.Fatalf("decoded file = %+v, want %+v", decoded.Data[0], uploaded)
	}

	contentRaw, err := json.Marshal(GigaChatFileContent{Content: "SGVsbG8="})
	if err != nil {
		t.Fatalf("marshal file content: %v", err)
	}
	if string(contentRaw) != `{"content":"SGVsbG8="}` {
		t.Fatalf("content JSON = %s", contentRaw)
	}
}

func TestGigaChatFilesHTTP(t *testing.T) {
	t.Parallel()

	t.Run("UploadMultipart", testGigaChatFileUploadMultipart)
	t.Run("ListUsesKeyBaseURLAndAuthHeaders", testGigaChatFileListUsesKeyBaseURLAndAuthHeaders)
	t.Run("ListReturnsAllFilesAndRawRequest", testGigaChatFileListReturnsAllFilesAndRawRequest)
	t.Run("ListRejectsUnsupportedPaginationControls", testGigaChatFileListRejectsUnsupportedPaginationControls)
	t.Run("ListPreservesUpstreamPurposeWhenFiltering", testGigaChatFileListPreservesUpstreamPurposeWhenFiltering)
	t.Run("ListRetrieveDelete", testGigaChatFileListRetrieveDelete)
	t.Run("ContentRawBytes", testGigaChatFileContentRawBytes)
	t.Run("ContentBase64Wrapper", testGigaChatFileContentBase64Wrapper)
	t.Run("RefreshesTokenAfterUnauthorized", testGigaChatFileUploadRefreshesTokenAfterUnauthorized)
}

func testGigaChatFileUploadMultipart(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/files" {
			t.Fatalf("path mismatch: got %s", request.URL.Path)
		}
		if request.Method != http.MethodPost {
			t.Fatalf("method mismatch: got %s, want POST", request.Method)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer file-upload-token" {
			t.Fatalf("authorization header mismatch: got %q", got)
		}
		if !strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/form-data;") {
			t.Fatalf("content type mismatch: got %q", request.Header.Get("Content-Type"))
		}
		if err := request.ParseMultipartForm(1024); err != nil {
			t.Fatalf("ParseMultipartForm returned error: %v", err)
		}
		if got := request.FormValue("purpose"); got != gigaChatFilePurposeGeneral {
			t.Fatalf("purpose mismatch: got %q, want %q", got, gigaChatFilePurposeGeneral)
		}
		file, header, err := request.FormFile("file")
		if err != nil {
			t.Fatalf("FormFile returned error: %v", err)
		}
		defer file.Close()
		if header.Filename != "input.txt" {
			t.Fatalf("filename mismatch: got %q", header.Filename)
		}
		if got := header.Header.Get("Content-Type"); got != "text/plain" {
			t.Fatalf("file content type mismatch: got %q, want text/plain", got)
		}
		body, err := io.ReadAll(file)
		if err != nil {
			t.Fatalf("ReadAll returned error: %v", err)
		}
		if !bytes.Equal(body, []byte(`{"ok":true}`)) {
			t.Fatalf("file body mismatch: got %q", string(body))
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "file-upload-request-id")
		_, _ = w.Write([]byte(`{"id":"file-uploaded","object":"file","bytes":11,"created_at":1780306293,"filename":"input.txt","purpose":"general","access_policy":"private"}`))
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	key := testGigaChatAccessTokenKey("file-upload-token")
	contentType := "application/jsonl"

	response, bifrostErr := provider.FileUpload(testBifrostContext(), key, &schemas.BifrostFileUploadRequest{
		Provider:    schemas.GigaChat,
		File:        []byte(`{"ok":true}`),
		Filename:    "input.jsonl",
		Purpose:     schemas.FilePurposeBatch,
		ContentType: &contentType,
	})
	if bifrostErr != nil {
		t.Fatalf("FileUpload returned error: %v", bifrostErr)
	}
	if response.ID != "file-uploaded" || response.Filename != "input.txt" || response.Bytes != 11 {
		t.Fatalf("unexpected upload response: %#v", response)
	}
	if response.Purpose != schemas.FilePurposeBatch {
		t.Fatalf("purpose mismatch: got %q, want %q", response.Purpose, schemas.FilePurposeBatch)
	}
	if response.StorageBackend != schemas.FileStorageAPI {
		t.Fatalf("storage backend mismatch: got %q", response.StorageBackend)
	}
	if response.ExtraFields.ProviderResponseHeaders["X-Request-Id"] != "file-upload-request-id" {
		t.Fatalf("provider headers mismatch: %#v", response.ExtraFields.ProviderResponseHeaders)
	}
}

func TestGigaChatFileUploadMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		filename        string
		contentType     *string
		file            []byte
		wantFilename    string
		wantContentType string
	}{
		{
			name:            "json content type is uploaded as supported text file",
			filename:        "input.jsonl",
			contentType:     schemas.Ptr("application/json"),
			file:            []byte(`{"custom_id":"req-1"}` + "\n"),
			wantFilename:    "input.txt",
			wantContentType: "text/plain",
		},
		{
			name:            "missing filename text defaults to txt",
			file:            []byte(`{"custom_id":"req-1"}` + "\n"),
			wantFilename:    "file.txt",
			wantContentType: "text/plain",
		},
		{
			name:            "supported binary type is preserved",
			filename:        "document.pdf",
			contentType:     schemas.Ptr("application/pdf"),
			file:            []byte("%PDF-1.7\n"),
			wantFilename:    "document.pdf",
			wantContentType: "application/pdf",
		},
		{
			name:            "xlsx mime alias uses gigachat supported type",
			filename:        "table.xlsx",
			contentType:     schemas.Ptr("application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"),
			file:            []byte("xlsx"),
			wantFilename:    "table.xlsx",
			wantContentType: "application/vnd.ms-excel",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body, contentType, bifrostErr := buildGigaChatFileUploadBody(&schemas.BifrostFileUploadRequest{
				Provider:    schemas.GigaChat,
				File:        tt.file,
				Filename:    tt.filename,
				Purpose:     schemas.FilePurposeUserData,
				ContentType: tt.contentType,
			})
			if bifrostErr != nil {
				t.Fatalf("buildGigaChatFileUploadBody returned error: %v", bifrostErr)
			}
			if !strings.HasPrefix(contentType, "multipart/form-data;") {
				t.Fatalf("content type mismatch: got %q", contentType)
			}

			request := httptest.NewRequest(http.MethodPost, "/files", bytes.NewReader(body))
			request.Header.Set("Content-Type", contentType)
			if err := request.ParseMultipartForm(1024); err != nil {
				t.Fatalf("ParseMultipartForm returned error: %v", err)
			}
			file, header, err := request.FormFile("file")
			if err != nil {
				t.Fatalf("FormFile returned error: %v", err)
			}
			defer file.Close()
			if header.Filename != tt.wantFilename {
				t.Fatalf("filename mismatch: got %q, want %q", header.Filename, tt.wantFilename)
			}
			if got := header.Header.Get("Content-Type"); got != tt.wantContentType {
				t.Fatalf("file content type mismatch: got %q, want %q", got, tt.wantContentType)
			}
		})
	}
}

func TestGigaChatMultipartFilenameEscaping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		filename string
		want     string
	}{
		{
			name:     "normal unicode filename is preserved",
			filename: "отчет.txt",
			want:     "отчет.txt",
		},
		{
			name:     "quotes are escaped",
			filename: `report "final".txt`,
			want:     `report \"final\".txt`,
		},
		{
			name:     "backslashes are escaped",
			filename: `dir\file.txt`,
			want:     `dir\\file.txt`,
		},
		{
			name:     "line feed is replaced",
			filename: "bad\nname.txt",
			want:     "bad_name.txt",
		},
		{
			name:     "carriage return is replaced",
			filename: "bad\rname.txt",
			want:     "bad_name.txt",
		},
		{
			name:     "crlf is replaced",
			filename: "bad\r\nname.txt",
			want:     "bad__name.txt",
		},
		{
			name:     "other control characters are replaced",
			filename: "bad\x00\t\x7fname.txt",
			want:     "bad___name.txt",
		},
		{
			name:     "sanitized filename can still escape quotes and backslashes",
			filename: "bad\r\ndir\\file \"final\".txt",
			want:     "bad__dir\\\\file \\\"final\\\".txt",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := escapeGigaChatMultipartFilename(tt.filename)
			if got != tt.want {
				t.Fatalf("escapeGigaChatMultipartFilename(%q) = %q, want %q", tt.filename, got, tt.want)
			}
			if strings.ContainsAny(got, "\r\n") {
				t.Fatalf("escaped filename still contains CR/LF: %q", got)
			}
		})
	}
}

func testGigaChatFileListUsesKeyBaseURLAndAuthHeaders(t *testing.T) {
	t.Parallel()

	networkServer := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		t.Fatalf("network base_url server should not be used, got %s", request.URL.Path)
	}))
	defer networkServer.Close()

	keyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/custom-api/v1/files" {
			t.Fatalf("path mismatch: got %s", request.URL.Path)
		}
		if request.Method != http.MethodGet {
			t.Fatalf("method mismatch: got %s, want GET", request.Method)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer key-base-url-token" {
			t.Fatalf("authorization header mismatch: got %q", got)
		}
		if got := request.Header.Get(gigaChatUserAgentHeader); got != gigaChatUserAgent {
			t.Fatalf("user-agent mismatch: got %q", got)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer keyServer.Close()

	provider := newTestGigaChatChatProvider(t, networkServer.URL)
	key := schemas.Key{
		GigaChatKeyConfig: &schemas.GigaChatKeyConfig{
			AccessToken: schemas.NewSecretVar("key-base-url-token"),
			BaseURL:     keyServer.URL + "/custom-api",
		},
	}

	response, bifrostErr := provider.FileList(testBifrostContext(), []schemas.Key{key}, &schemas.BifrostFileListRequest{Provider: schemas.GigaChat})
	if bifrostErr != nil {
		t.Fatalf("FileList returned error: %v", bifrostErr)
	}
	if response.Object != "list" || len(response.Data) != 0 {
		t.Fatalf("unexpected list response: %#v", response)
	}
}

func testGigaChatFileListReturnsAllFilesAndRawRequest(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/files" {
			t.Fatalf("path mismatch: got %s", request.URL.Path)
		}
		if request.Method != http.MethodGet {
			t.Fatalf("method mismatch: got %s, want GET", request.Method)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"file-1","object":"file","bytes":10,"created_at":1780306293,"filename":"one.txt","purpose":"general"},{"id":"file-2","object":"file","bytes":20,"created_at":1780306294,"filename":"two.txt","purpose":"general"}]}`))
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	provider.sendBackRawRequest = true
	provider.sendBackRawResponse = true

	ctx := testBifrostContext()
	ctx.SetValue(schemas.BifrostContextKeyCaptureRawRequest, true)
	ctx.SetValue(schemas.BifrostContextKeyCaptureRawResponse, true)

	response, bifrostErr := provider.FileList(ctx, []schemas.Key{testGigaChatAccessTokenKey("files-token")}, &schemas.BifrostFileListRequest{
		Provider: schemas.GigaChat,
	})
	if bifrostErr != nil {
		t.Fatalf("FileList returned error: %v", bifrostErr)
	}
	if len(response.Data) != 2 || response.Data[0].ID != "file-1" || response.Data[1].ID != "file-2" {
		t.Fatalf("file list mismatch: %#v", response.Data)
	}
	if got := stringifyGigaChatRaw(response.ExtraFields.RawRequest); got != `{}` {
		t.Fatalf("raw request mismatch: got %s", got)
	}
	if response.ExtraFields.RawResponse == nil {
		t.Fatal("expected raw response")
	}
}

func testGigaChatFileListRejectsUnsupportedPaginationControls(t *testing.T) {
	t.Parallel()

	provider, err := NewGigaChatProvider(&schemas.ProviderConfig{}, nil)
	if err != nil {
		t.Fatalf("NewGigaChatProvider returned error: %v", err)
	}
	order := "desc"
	testCases := []struct {
		name       string
		request    *schemas.BifrostFileListRequest
		wantErrSub string
	}{
		{
			name:       "limit",
			request:    &schemas.BifrostFileListRequest{Provider: schemas.GigaChat, Limit: 1},
			wantErrSub: "does not support limit pagination",
		},
		{
			name:       "order",
			request:    &schemas.BifrostFileListRequest{Provider: schemas.GigaChat, Order: &order},
			wantErrSub: "does not support order sorting",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			response, bifrostErr := provider.FileList(testBifrostContext(), []schemas.Key{testGigaChatAccessTokenKey("files-token")}, testCase.request)
			if response != nil {
				t.Fatalf("expected nil response, got %#v", response)
			}
			if bifrostErr == nil || !strings.Contains(bifrostErr.GetErrorString(), testCase.wantErrSub) {
				t.Fatalf("expected %q error, got %v", testCase.wantErrSub, bifrostErr)
			}
		})
	}
}

func testGigaChatFileListPreservesUpstreamPurposeWhenFiltering(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/files" {
			t.Fatalf("path mismatch: got %s", request.URL.Path)
		}
		if request.Method != http.MethodGet {
			t.Fatalf("method mismatch: got %s, want GET", request.Method)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"general-file","object":"file","bytes":10,"created_at":1780306293,"filename":"general.txt","purpose":"general"},{"id":"assistant-file","object":"file","bytes":20,"created_at":1780306294,"filename":"assistant.txt","purpose":"assistant"}]}`))
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	response, bifrostErr := provider.FileList(testBifrostContext(), []schemas.Key{testGigaChatAccessTokenKey("files-token")}, &schemas.BifrostFileListRequest{
		Provider: schemas.GigaChat,
		Purpose:  schemas.FilePurposeAssistants,
	})
	if bifrostErr != nil {
		t.Fatalf("FileList returned error: %v", bifrostErr)
	}
	if len(response.Data) != 1 {
		t.Fatalf("file count mismatch: got %d files: %#v", len(response.Data), response.Data)
	}
	if response.Data[0].ID != "assistant-file" || response.Data[0].Purpose != schemas.FilePurposeAssistants {
		t.Fatalf("unexpected filtered file: %#v", response.Data[0])
	}
}

func testGigaChatFileListRetrieveDelete(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "Bearer files-token" {
			t.Fatalf("authorization header mismatch: got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")

		switch request.URL.Path {
		case "/v1/files":
			if request.Method != http.MethodGet {
				t.Fatalf("list method mismatch: got %s", request.Method)
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"file-1","object":"file","bytes":10,"created_at":1780306293,"filename":"assistant.txt","purpose":"assistant"},{"id":"file-2","object":"file","bytes":20,"created_at":1780306294,"filename":"general.txt","purpose":"general"}]}`))
		case "/v1/files/file-1":
			if request.Method != http.MethodGet {
				t.Fatalf("retrieve method mismatch: got %s", request.Method)
			}
			_, _ = w.Write([]byte(`{"id":"file-1","object":"file","bytes":10,"created_at":1780306293,"filename":"assistant.txt","purpose":"assistant"}`))
		case "/v1/files/file-1/delete":
			if request.Method != http.MethodPost {
				t.Fatalf("delete method mismatch: got %s", request.Method)
			}
			_, _ = w.Write([]byte(`{"id":"file-1","deleted":true}`))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	key := testGigaChatAccessTokenKey("files-token")
	ctx := testBifrostContext()

	listResponse, bifrostErr := provider.FileList(ctx, []schemas.Key{key}, &schemas.BifrostFileListRequest{Provider: schemas.GigaChat})
	if bifrostErr != nil {
		t.Fatalf("FileList returned error: %v", bifrostErr)
	}
	if len(listResponse.Data) != 2 {
		t.Fatalf("file count mismatch: got %d, want 2", len(listResponse.Data))
	}
	if listResponse.Data[0].Purpose != schemas.FilePurposeAssistants {
		t.Fatalf("assistant purpose mismatch: got %q", listResponse.Data[0].Purpose)
	}
	if listResponse.Data[1].Purpose != schemas.FilePurposeUserData {
		t.Fatalf("general purpose mismatch: got %q", listResponse.Data[1].Purpose)
	}

	retrieveResponse, bifrostErr := provider.FileRetrieve(ctx, []schemas.Key{key}, &schemas.BifrostFileRetrieveRequest{
		Provider: schemas.GigaChat,
		FileID:   "file-1",
	})
	if bifrostErr != nil {
		t.Fatalf("FileRetrieve returned error: %v", bifrostErr)
	}
	if retrieveResponse.ID != "file-1" || retrieveResponse.Purpose != schemas.FilePurposeAssistants {
		t.Fatalf("unexpected retrieve response: %#v", retrieveResponse)
	}

	deleteResponse, bifrostErr := provider.FileDelete(ctx, []schemas.Key{key}, &schemas.BifrostFileDeleteRequest{
		Provider: schemas.GigaChat,
		FileID:   "file-1",
	})
	if bifrostErr != nil {
		t.Fatalf("FileDelete returned error: %v", bifrostErr)
	}
	if deleteResponse.ID != "file-1" || !deleteResponse.Deleted || deleteResponse.Object != "file" {
		t.Fatalf("unexpected delete response: %#v", deleteResponse)
	}
}

func testGigaChatFileContentRawBytes(t *testing.T) {
	t.Parallel()

	wantContent := []byte{0xff, 0xd8, 0xff, 0xdb}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/files/image-file/content" {
			t.Fatalf("path mismatch: got %s", request.URL.Path)
		}
		if request.Method != http.MethodGet {
			t.Fatalf("method mismatch: got %s, want GET", request.Method)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer content-token" {
			t.Fatalf("authorization header mismatch: got %q", got)
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(wantContent)
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	response, bifrostErr := provider.FileContent(testBifrostContext(), []schemas.Key{testGigaChatAccessTokenKey("content-token")}, &schemas.BifrostFileContentRequest{
		Provider: schemas.GigaChat,
		FileID:   "image-file",
	})
	if bifrostErr != nil {
		t.Fatalf("FileContent returned error: %v", bifrostErr)
	}
	if !bytes.Equal(response.Content, wantContent) {
		t.Fatalf("content mismatch: got %v, want %v", response.Content, wantContent)
	}
	if response.ContentType != "image/jpeg" {
		t.Fatalf("content type mismatch: got %q", response.ContentType)
	}
}

func testGigaChatFileContentBase64Wrapper(t *testing.T) {
	t.Parallel()

	encoded := base64.StdEncoding.EncodeToString([]byte("decoded file content"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/files/wrapped-file/content" {
			t.Fatalf("path mismatch: got %s", request.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":"` + encoded + `"}`))
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	response, bifrostErr := provider.FileContent(testBifrostContext(), []schemas.Key{testGigaChatAccessTokenKey("wrapper-token")}, &schemas.BifrostFileContentRequest{
		Provider: schemas.GigaChat,
		FileID:   "wrapped-file",
	})
	if bifrostErr != nil {
		t.Fatalf("FileContent returned error: %v", bifrostErr)
	}
	if string(response.Content) != "decoded file content" {
		t.Fatalf("content mismatch: got %q", string(response.Content))
	}
	if response.ContentType != "application/octet-stream" {
		t.Fatalf("content type mismatch: got %q", response.ContentType)
	}
}

func testGigaChatFileUploadRefreshesTokenAfterUnauthorized(t *testing.T) {
	t.Parallel()

	var tokenRequests atomic.Int32
	var uploadRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/oauth":
			tokenIndex := tokenRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"files-token-` + formatInt32(tokenIndex) + `","expires_at":1893456000}`))
		case "/v1/files":
			uploadIndex := uploadRequests.Add(1)
			wantAuthorization := "Bearer files-token-" + formatInt32(uploadIndex)
			if got := request.Header.Get("Authorization"); got != wantAuthorization {
				t.Fatalf("authorization header mismatch on request %d: got %q, want %q", uploadIndex, got, wantAuthorization)
			}
			w.Header().Set("Content-Type", "application/json")
			if uploadIndex == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"status":401,"message":"expired token"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"file-refreshed","object":"file","bytes":4,"created_at":1780306293,"filename":"file.txt","purpose":"general"}`))
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	provider := newTestGigaChatChatProvider(t, server.URL)
	key := testGigaChatOAuthKey(server.URL+"/oauth", "", "test-credentials")

	response, bifrostErr := provider.FileUpload(testBifrostContext(), key, &schemas.BifrostFileUploadRequest{
		Provider: schemas.GigaChat,
		File:     []byte("test"),
		Filename: "file.txt",
		Purpose:  schemas.FilePurposeUserData,
	})
	if bifrostErr != nil {
		t.Fatalf("FileUpload returned error: %v", bifrostErr)
	}
	if response.ID != "file-refreshed" {
		t.Fatalf("unexpected response: %#v", response)
	}
	if tokenRequests.Load() != 2 {
		t.Fatalf("token request count mismatch: got %d, want 2", tokenRequests.Load())
	}
	if uploadRequests.Load() != 2 {
		t.Fatalf("upload request count mismatch: got %d, want 2", uploadRequests.Load())
	}
}
