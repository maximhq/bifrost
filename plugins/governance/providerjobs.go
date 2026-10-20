package governance

import (
	"bytes"
	"fmt"
	"net/url"
	"path"
	"slices"
	"sort"
	"strings"

	bifrost "github.com/maximhq/bifrost/core"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// Provider objects (batches, videos, files, containers and the files inside them, cached contents) are created through the
// operator's provider key, which every virtual key allowed on the provider shares, and are then
// addressed by the provider-side id alone. This file makes them belong to the virtual key that
// created them: the create response records the key, the read, download, delete, remix and
// container-file paths honour it, and list pages are narrowed to what the key may see.
//
// Who created an object is read from the ownership row written here, and, for batches and
// videos, from the accounting job row the logging plugin writes, which predates ownership rows.
// A batch row also names the batch's input, output and error files, so those files are bound to
// the batch's key even when they were never recorded on their own.
//
// The rule is deliberately narrow. A request that presented no virtual key is unrestricted, as it
// is everywhere else in governance. An object with no row (created outside the gateway, or before
// its kind was recorded) cannot be bound to anyone and stays reachable. Only a row that names a
// different virtual key is refused, and it is refused as not found, so the other tenant's object
// is not confirmed to exist.

// providerObjectRef names the provider object a request addresses, or the kind a list request
// pages through.
type providerObjectRef struct {
	kind      string
	provider  string
	requestID string   // the id as the caller wrote it, for messages
	ids       []string // the forms the id may be recorded under
	isList    bool
	indirect  bool // named inside a request that creates or runs something else, not addressed by id
	// passthroughList is a list through a passthrough route: its raw page cannot be narrowed.
	passthroughList bool
}

// governanceAutoContainerContextKey marks a request whose code_interpreter tool asked the provider
// for an automatic container, so the container named in its response is recorded for the key. Its
// value is the ids of the containers the same request named outright, which are never claimed.
const governanceAutoContainerContextKey schemas.BifrostContextKey = "bf-governance-auto-container"

// providerObjectLabel is the noun a kind goes by in client-facing messages.
func providerObjectLabel(kind string) string {
	switch kind {
	case configstoreTables.ProviderObjectKindBatch:
		return "batch"
	case configstoreTables.ProviderObjectKindVideo:
		return "video"
	case configstoreTables.ProviderObjectKindFile:
		return "file"
	case configstoreTables.ProviderObjectKindContainer:
		return "container"
	case configstoreTables.ProviderObjectKindCachedContent:
		return "cached content"
	case configstoreTables.ProviderObjectKindContainerFile:
		return "container file"
	}
	return "object"
}

// requestVirtualKeyID is the virtual key a request is attributed to, read from its Grant, or
// empty when the request presented no key or nothing has resolved one yet.
func requestVirtualKeyID(ctx *schemas.BifrostContext) string {
	if ctx == nil {
		return ""
	}
	grant := ctx.Grant()
	if grant == nil {
		return ""
	}
	identity := grant.Identity()
	if identity == nil {
		return ""
	}
	virtualKey := identity.VirtualKey()
	if virtualKey == nil {
		return ""
	}
	return virtualKey.ID
}

// videoIDForms returns the forms a video id may be recorded under: as written, bare, and scoped
// with its ":<provider>" suffix the way providers hand it back.
func videoIDForms(provider schemas.ModelProvider, id string) []string {
	if id == "" {
		return nil
	}
	bare := providerUtils.StripVideoIDProviderSuffix(id, provider)
	forms := []string{id}
	for _, form := range []string{bare, providerUtils.AddVideoIDProviderSuffix(bare, provider)} {
		if form != "" && form != id && (len(forms) < 2 || form != forms[1]) {
			forms = append(forms, form)
		}
	}
	return forms
}

// resourceCollections is, per kind, the collection a Google resource name puts in front of the
// id: a Gemini batch is "batches/<id>", a file "files/<id>", a cached content "cachedContents/<id>".
var resourceCollections = map[string]string{
	configstoreTables.ProviderObjectKindBatch:         "batches",
	configstoreTables.ProviderObjectKindFile:          "files",
	configstoreTables.ProviderObjectKindCachedContent: "cachedContents",
}

// providerObjectIDForms returns the forms an id may be recorded under, as written first. A video
// id is taken with and without its ":<provider>" suffix. Gemini addresses an object by a resource
// name and accepts its short form too: a batch as "batches/<id>" or "<id>", a cached content as
// "cachedContents/<n>" or "<n>", so a short id is also taken bare and under its collection. The
// provider is not consulted for that: a custom provider built on Gemini goes by its own name, and
// an id of another shape gains forms that nothing is recorded under. A full resource name
// ("projects/<p>/locations/<l>/batchPredictionJobs/<n>") is exact and is the only form it is
// recorded under; its short forms are resolved against full names by suffix instead (see
// resolveShortResourceNames), since a job number is one project's and cannot be a row of its own.
func providerObjectIDForms(kind string, provider schemas.ModelProvider, id string) []string {
	if id == "" {
		return nil
	}
	if kind == configstoreTables.ProviderObjectKindVideo {
		return videoIDForms(provider, id)
	}
	forms := []string{id}
	collection, ok := resourceCollections[kind]
	if !ok || strings.HasPrefix(id, "projects/") {
		return forms
	}
	bare := strings.TrimPrefix(id, collection+"/")
	prefixed := collection + "/" + bare
	for _, form := range []string{bare, prefixed} {
		if form != "" && !slices.Contains(forms, form) {
			forms = append(forms, form)
		}
	}
	return forms
}

func objectRef(kind string, provider schemas.ModelProvider, id string) (providerObjectRef, bool) {
	if id == "" {
		return providerObjectRef{}, false
	}
	return providerObjectRef{kind: kind, provider: string(provider), requestID: id, ids: providerObjectIDForms(kind, provider, id)}, true
}

func listRef(kind string, provider schemas.ModelProvider) (providerObjectRef, bool) {
	return providerObjectRef{kind: kind, provider: string(provider), isList: true}, true
}

// providerObjectReference returns the provider object a request addresses, or false when the
// request addresses none.
func providerObjectReference(req *schemas.BifrostRequest) (providerObjectRef, bool) {
	if req == nil {
		return providerObjectRef{}, false
	}
	batch := configstoreTables.ProviderObjectKindBatch
	video := configstoreTables.ProviderObjectKindVideo
	file := configstoreTables.ProviderObjectKindFile
	container := configstoreTables.ProviderObjectKindContainer
	cached := configstoreTables.ProviderObjectKindCachedContent
	switch {
	case req.BatchRetrieveRequest != nil:
		return objectRef(batch, req.BatchRetrieveRequest.Provider, req.BatchRetrieveRequest.BatchID)
	case req.BatchCancelRequest != nil:
		return objectRef(batch, req.BatchCancelRequest.Provider, req.BatchCancelRequest.BatchID)
	case req.BatchResultsRequest != nil:
		return objectRef(batch, req.BatchResultsRequest.Provider, req.BatchResultsRequest.BatchID)
	case req.BatchDeleteRequest != nil:
		return objectRef(batch, req.BatchDeleteRequest.Provider, req.BatchDeleteRequest.BatchID)
	case req.BatchListRequest != nil:
		return listRef(batch, req.BatchListRequest.Provider)
	case req.BatchCreateRequest != nil:
		// A batch runs on its input file; another key's file is as closed to it as the file itself.
		return objectRef(file, req.BatchCreateRequest.Provider, req.BatchCreateRequest.InputFileID)
	case req.FileRetrieveRequest != nil:
		return objectRef(file, req.FileRetrieveRequest.Provider, req.FileRetrieveRequest.FileID)
	case req.FileContentRequest != nil:
		return objectRef(file, req.FileContentRequest.Provider, req.FileContentRequest.FileID)
	case req.FileDeleteRequest != nil:
		return objectRef(file, req.FileDeleteRequest.Provider, req.FileDeleteRequest.FileID)
	case req.FileListRequest != nil:
		return listRef(file, req.FileListRequest.Provider)
	case req.VideoRetrieveRequest != nil:
		return objectRef(video, req.VideoRetrieveRequest.Provider, req.VideoRetrieveRequest.ID)
	case req.VideoDownloadRequest != nil:
		return objectRef(video, req.VideoDownloadRequest.Provider, req.VideoDownloadRequest.ID)
	case req.VideoDeleteRequest != nil:
		return objectRef(video, req.VideoDeleteRequest.Provider, req.VideoDeleteRequest.ID)
	case req.VideoRemixRequest != nil:
		return objectRef(video, req.VideoRemixRequest.Provider, req.VideoRemixRequest.ID)
	case req.VideoListRequest != nil:
		return listRef(video, req.VideoListRequest.Provider)
	case req.ContainerRetrieveRequest != nil:
		return objectRef(container, req.ContainerRetrieveRequest.Provider, req.ContainerRetrieveRequest.ContainerID)
	case req.ContainerDeleteRequest != nil:
		return objectRef(container, req.ContainerDeleteRequest.Provider, req.ContainerDeleteRequest.ContainerID)
	case req.ContainerFileCreateRequest != nil:
		return objectRef(container, req.ContainerFileCreateRequest.Provider, req.ContainerFileCreateRequest.ContainerID)
	case req.VideoEditRequest != nil:
		// An edit runs on its source video; another key's video is as closed to it as the video itself.
		if req.VideoEditRequest.Input == nil {
			return providerObjectRef{}, false
		}
		ref, ok := objectRef(video, req.VideoEditRequest.Provider, req.VideoEditRequest.Input.Video.ID)
		ref.indirect = true
		return ref, ok
	case req.ContainerFileListRequest != nil:
		return objectRef(container, req.ContainerFileListRequest.Provider, req.ContainerFileListRequest.ContainerID)
	case req.ContainerFileRetrieveRequest != nil:
		return objectRef(container, req.ContainerFileRetrieveRequest.Provider, req.ContainerFileRetrieveRequest.ContainerID)
	case req.ContainerFileContentRequest != nil:
		return objectRef(container, req.ContainerFileContentRequest.Provider, req.ContainerFileContentRequest.ContainerID)
	case req.ContainerFileDeleteRequest != nil:
		return objectRef(container, req.ContainerFileDeleteRequest.Provider, req.ContainerFileDeleteRequest.ContainerID)
	case req.ContainerListRequest != nil:
		return listRef(container, req.ContainerListRequest.Provider)
	case req.CachedContentRetrieveRequest != nil:
		return objectRef(cached, req.CachedContentRetrieveRequest.Provider, req.CachedContentRetrieveRequest.Name)
	case req.CachedContentUpdateRequest != nil:
		return objectRef(cached, req.CachedContentUpdateRequest.Provider, req.CachedContentUpdateRequest.Name)
	case req.CachedContentDeleteRequest != nil:
		return objectRef(cached, req.CachedContentDeleteRequest.Provider, req.CachedContentDeleteRequest.Name)
	case req.CachedContentListRequest != nil:
		return listRef(cached, req.CachedContentListRequest.Provider)
	}
	return providerObjectRef{}, false
}

// providerObjectReferences returns every provider object a request names. Most requests address
// one object by id (providerObjectReference). Several shapes name objects indirectly: a container
// create copies the files in file_ids into the new container, a container-file create can copy an
// existing file in by file_id, a video edit runs on its source video, a chat or responses request
// can carry uploaded files by file_id in its messages, and a chat, responses, image or
// transcription request can run on a cached content (Gemini's cached_content, in either key
// spelling). Those are checked like a direct reference, since the provider would otherwise hand
// another key's file, video or cache to the caller through the object it is creating or the
// answer. A file inside a container is an object of its own: a request that addresses one names
// both the container and the file.
func providerObjectReferences(req *schemas.BifrostRequest) []providerObjectRef {
	if req == nil {
		return nil
	}
	file := configstoreTables.ProviderObjectKindFile
	containerFile := configstoreTables.ProviderObjectKindContainerFile
	if req.ContainerCreateRequest != nil {
		var refs []providerObjectRef
		for _, fileID := range req.ContainerCreateRequest.FileIDs {
			if ref, ok := objectRef(file, req.ContainerCreateRequest.Provider, fileID); ok {
				ref.indirect = true
				refs = append(refs, ref)
			}
		}
		return refs
	}
	if r := req.ContainerFileCreateRequest; r != nil {
		var refs []providerObjectRef
		if ref, ok := objectRef(configstoreTables.ProviderObjectKindContainer, r.Provider, r.ContainerID); ok {
			refs = append(refs, ref)
		}
		if r.FileID != nil {
			if ref, ok := objectRef(file, r.Provider, *r.FileID); ok {
				ref.indirect = true
				refs = append(refs, ref)
			}
		}
		return refs
	}
	if r := req.PassthroughRequest; r != nil {
		return passthroughObjectRefs(r)
	}
	if r := req.BatchCreateRequest; r != nil {
		// A batch runs on its input file, or on inline items whose bodies the provider reads with
		// the files and caches named there.
		var refs []providerObjectRef
		if ref, ok := objectRef(file, r.Provider, r.InputFileID); ok {
			refs = append(refs, ref)
		}
		for _, item := range r.Requests {
			refs = append(refs, jsonObjectRefs(r.Provider, item.Body)...)
			refs = append(refs, jsonObjectRefs(r.Provider, item.Params)...)
		}
		return refs
	}
	if r := req.CachedContentCreateRequest; r != nil {
		var refs []providerObjectRef
		for _, name := range cachedContentFileNames(r) {
			if ref, ok := objectRef(file, r.Provider, name); ok {
				ref.indirect = true
				refs = append(refs, ref)
			}
		}
		return refs
	}
	if provider, containerID, fileID, ok := containerFileAddress(req); ok {
		var refs []providerObjectRef
		if ref, ok := objectRef(configstoreTables.ProviderObjectKindContainer, provider, containerID); ok {
			refs = append(refs, ref)
		}
		if ref, ok := objectRef(containerFile, provider, fileID); ok {
			refs = append(refs, ref)
		}
		return refs
	}
	if provider, extra, ok := cachedContentCarrier(req); ok {
		var refs []providerObjectRef
		// Both spellings are checked when both are present: providers disagree on which one wins
		// (Gemini's image converters read cachedContent first, its chat converter cached_content),
		// so checking only one would let the other carry a different cache through.
		var seenCache string
		for _, key := range []string{"cached_content", "cachedContent"} {
			if name, ok := schemas.SafeExtractString(extra[key]); ok && name != "" && name != seenCache {
				ref, _ := objectRef(configstoreTables.ProviderObjectKindCachedContent, provider, name)
				ref.indirect = true
				refs = append(refs, ref)
				seenCache = name
			}
		}
		for _, fileID := range inferenceFileIDs(req) {
			ref, _ := objectRef(file, provider, fileID)
			ref.indirect = true
			refs = append(refs, ref)
		}
		refs = append(refs, toolObjectRefs(req, provider)...)
		return refs
	}
	if ref, ok := providerObjectReference(req); ok {
		return []providerObjectRef{ref}
	}
	return nil
}

// containerFileAddress returns the container and file a container-file retrieve, content or
// delete request addresses, or false for any other request.
func containerFileAddress(req *schemas.BifrostRequest) (schemas.ModelProvider, string, string, bool) {
	switch {
	case req.ContainerFileRetrieveRequest != nil:
		r := req.ContainerFileRetrieveRequest
		return r.Provider, r.ContainerID, r.FileID, true
	case req.ContainerFileContentRequest != nil:
		r := req.ContainerFileContentRequest
		return r.Provider, r.ContainerID, r.FileID, true
	case req.ContainerFileDeleteRequest != nil:
		r := req.ContainerFileDeleteRequest
		return r.Provider, r.ContainerID, r.FileID, true
	}
	return "", "", "", false
}

// toolObjectRefs returns the containers and files a Responses request's code_interpreter tools
// name: the container they run in when it is an existing one named by id, or the file_ids an
// automatic container is seeded with. The provider runs the tool against them under the shared
// key, so each is checked like any other object the request names.
func toolObjectRefs(req *schemas.BifrostRequest, provider schemas.ModelProvider) []providerObjectRef {
	if req.ResponsesRequest == nil || req.ResponsesRequest.Params == nil {
		return nil
	}
	var refs []providerObjectRef
	add := func(kind, id string) {
		if ref, ok := objectRef(kind, provider, strings.TrimSpace(id)); ok {
			ref.indirect = true
			refs = append(refs, ref)
		}
	}
	for _, tool := range req.ResponsesRequest.Params.Tools {
		if tool.ResponsesToolCodeInterpreter == nil {
			continue
		}
		switch container := tool.ResponsesToolCodeInterpreter.Container.(type) {
		case string:
			add(configstoreTables.ProviderObjectKindContainer, container)
		case *string:
			if container != nil {
				add(configstoreTables.ProviderObjectKindContainer, *container)
			}
		case map[string]interface{}:
			if fileIDs, ok := container["file_ids"].([]interface{}); ok {
				for _, fileID := range fileIDs {
					if id, ok := fileID.(string); ok {
						add(configstoreTables.ProviderObjectKindFile, id)
					}
				}
			} else if fileIDs, ok := container["file_ids"].([]string); ok {
				for _, id := range fileIDs {
					add(configstoreTables.ProviderObjectKindFile, id)
				}
			}
		}
	}
	return refs
}

// passthroughCollections maps a passthrough path segment that names a collection to its object kind.
var passthroughCollections = map[string]string{
	"files":          configstoreTables.ProviderObjectKindFile,
	"batches":        configstoreTables.ProviderObjectKindBatch,
	"containers":     configstoreTables.ProviderObjectKindContainer,
	"videos":         configstoreTables.ProviderObjectKindVideo,
	"cachedContents": configstoreTables.ProviderObjectKindCachedContent,
}

// passthroughObjectRefs returns the provider objects a passthrough request reaches. The router
// forwards the provider's own path and body under the shared key, so the object a path addresses
// (files/<id>, batches/<id>, containers/<id> and the files inside it, videos/<id>,
// cachedContents/<id>) is checked directly, a GET of one of those collections is a list, and the
// objects a JSON body names are checked like the same references on the native routes (see
// jsonObjectRefs). A body that is not JSON names nothing that can be checked.
func passthroughObjectRefs(r *schemas.BifrostPassthroughRequest) []providerObjectRef {
	var refs []providerObjectRef
	gemini := r.Provider == schemas.Gemini
	add := func(kind, id string) {
		if ref, ok := objectRef(kind, r.Provider, strings.TrimSpace(id)); ok {
			if kind == configstoreTables.ProviderObjectKindVideo {
				ref.ids = videoIDForms(r.Provider, ref.requestID)
			}
			refs = append(refs, ref)
		}
	}
	// The path is matched as it will be sent: the HTTP client resolves . and .. segments and
	// collapses repeated slashes, so files/x/../file-other reaches file-other, not x. A path with
	// neither is already in that form and is not rebuilt.
	sent := r.Path
	if strings.Contains(sent, "/.") || strings.Contains(sent, "//") || strings.HasPrefix(sent, ".") {
		sent = path.Clean("/" + sent)
	}
	segments := strings.Split(strings.Trim(sent, "/"), "/")
	// A GET that ends on a collection lists it.
	if last := segments[len(segments)-1]; strings.EqualFold(r.Method, "GET") {
		if kind, ok := passthroughCollections[last]; ok {
			if last == "files" && len(segments) >= 3 && segments[len(segments)-3] == "containers" {
				kind = configstoreTables.ProviderObjectKindContainerFile
			}
			refs = append(refs, providerObjectRef{kind: kind, provider: string(r.Provider), isList: true, passthroughList: true})
		}
	}
	// A collection segment followed by an id addresses that object.
	for i := 0; i+1 < len(segments); i++ {
		raw := segments[i+1]
		id, _, _ := strings.Cut(raw, ":")
		if id == "" {
			continue
		}
		switch segments[i] {
		case "files":
			if gemini {
				id = "files/" + id
			}
			if i >= 2 && segments[i-2] == "containers" {
				add(configstoreTables.ProviderObjectKindContainerFile, id)
			} else {
				add(configstoreTables.ProviderObjectKindFile, id)
			}
		case "batches":
			if gemini {
				id = "batches/" + id
			}
			add(configstoreTables.ProviderObjectKindBatch, id)
		case "containers":
			add(configstoreTables.ProviderObjectKindContainer, id)
		case "videos":
			// A video id may carry its ":<provider>" suffix in the path; every form is checked.
			add(configstoreTables.ProviderObjectKindVideo, raw)
		case "cachedContents":
			add(configstoreTables.ProviderObjectKindCachedContent, "cachedContents/"+id)
		}
	}
	if len(r.Body) == 0 || !mayNameProviderObjects(r.Body) {
		return refs
	}
	var body any
	if err := schemas.Unmarshal(r.Body, &body); err != nil {
		return refs
	}
	return append(refs, jsonObjectRefs(r.Provider, body)...)
}

// providerObjectBodyMarkers are the keys a JSON body names a provider object under; a body that
// contains none of them names nothing and is not parsed.
var providerObjectBodyMarkers = [][]byte{
	[]byte(`"file_id`), []byte(`"fileUri"`), []byte(`"file_uri"`),
	[]byte(`"cached_content"`), []byte(`"cachedContent"`), []byte(`code_interpreter`),
	[]byte(`"input_file_id"`), []byte(`"training_file"`), []byte(`"validation_file"`), []byte(`"fileName"`),
}

var unicodeEscape = []byte(`\u`)

// mayNameProviderObjects reports whether a raw body could name a provider object, so passthrough
// bodies that cannot are forwarded without a JSON parse. A \u escape can spell any marker
// character (file\u005fid decodes to file_id), so a body carrying one is always parsed; the other
// escapes (\n, \") cannot produce a marker's letters and leave the shortcut in place.
func mayNameProviderObjects(body []byte) bool {
	if bytes.Contains(body, unicodeEscape) {
		return true
	}
	for _, marker := range providerObjectBodyMarkers {
		if bytes.Contains(body, marker) {
			return true
		}
	}
	return false
}

// jsonObjectRefs returns the provider objects a JSON request body names anywhere in it: file_id
// and file_ids, a code_interpreter tool's container named by id, a Gemini fileUri (or file_uri),
// a cached content (cached_content or cachedContent), and the input file a batch or fine-tune
// runs on (input_file_id, training_file, validation_file, and a Gemini batch's fileName). It
// reads passthrough bodies and the bodies of inline batch items, which reach the provider as
// written.
func jsonObjectRefs(provider schemas.ModelProvider, body any) []providerObjectRef {
	var refs []providerObjectRef
	add := func(kind, id string) {
		if ref, ok := objectRef(kind, provider, strings.TrimSpace(id)); ok {
			ref.indirect = true
			refs = append(refs, ref)
		}
	}
	var walk func(value any)
	walk = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			if t, _ := v["type"].(string); t == "code_interpreter" {
				if container, ok := v["container"].(string); ok {
					add(configstoreTables.ProviderObjectKindContainer, container)
				}
			}
			for key, item := range v {
				switch key {
				case "file_id", "input_file_id", "training_file", "validation_file":
					if id, ok := item.(string); ok {
						add(configstoreTables.ProviderObjectKindFile, id)
					}
				case "fileName":
					// A Gemini batch's inputConfig names its input file by resource name.
					if name, ok := item.(string); ok && strings.HasPrefix(name, "files/") {
						add(configstoreTables.ProviderObjectKindFile, name)
					}
				case "file_ids":
					if ids, ok := item.([]any); ok {
						for _, id := range ids {
							if s, ok := id.(string); ok {
								add(configstoreTables.ProviderObjectKindFile, s)
							}
						}
					}
				case "fileUri", "file_uri":
					if uri, ok := item.(string); ok {
						if name := geminiFileName(&uri); name != nil {
							add(configstoreTables.ProviderObjectKindFile, *name)
						}
					}
				case "cached_content", "cachedContent":
					if name, ok := item.(string); ok {
						add(configstoreTables.ProviderObjectKindCachedContent, name)
					}
				}
				walk(item)
			}
		case []any:
			for _, item := range v {
				walk(item)
			}
		}
	}
	walk(body)
	return refs
}

// requestsAutomaticContainer reports whether a Responses request's code_interpreter tool asks the
// provider for an automatic container (a container object rather than an existing id).
func requestsAutomaticContainer(req *schemas.BifrostRequest) bool {
	if req == nil || req.ResponsesRequest == nil || req.ResponsesRequest.Params == nil {
		return false
	}
	for _, tool := range req.ResponsesRequest.Params.Tools {
		if tool.ResponsesToolCodeInterpreter == nil {
			continue
		}
		switch tool.ResponsesToolCodeInterpreter.Container.(type) {
		case string, *string, nil:
		default:
			return true
		}
	}
	return false
}

// geminiFileHost is the host of a Gemini file URI, "https://generativelanguage.googleapis.com/<version>/files/<id>".
const geminiFileHost = "generativelanguage.googleapis.com"

// geminiFileName returns the name a Gemini file URI's file is recorded under ("files/<id>", the
// name the upload response carries), or nil when the URL is not a Gemini file URI. The GenAI route
// turns a native fileData.fileUri into a file or image URL, and Gemini then reads that file under
// the shared key, so the URI names the file as surely as its id does.
func geminiFileName(rawURL *string) *string {
	if rawURL == nil || *rawURL == "" {
		return nil
	}
	parsed, err := url.Parse(strings.TrimSpace(*rawURL))
	if err != nil || !strings.EqualFold(parsed.Hostname(), geminiFileHost) {
		return nil
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i := 0; i+1 < len(segments); i++ {
		if segments[i] == "files" && segments[i+1] != "" {
			name := "files/" + strings.SplitN(segments[i+1], ":", 2)[0]
			return &name
		}
	}
	return nil
}

// cachedContentFileNames returns the Gemini files a cached-content create builds the cache from:
// every fileData.fileUri (or file_data.file_uri) anywhere in its contents and system instruction.
func cachedContentFileNames(req *schemas.BifrostCachedContentCreateRequest) []string {
	var names []string
	seen := make(map[string]struct{})
	var walk func(value any)
	walk = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			for key, item := range v {
				if key == "fileUri" || key == "file_uri" {
					if uri, ok := item.(string); ok {
						if name := geminiFileName(&uri); name != nil {
							if _, dup := seen[*name]; !dup {
								seen[*name] = struct{}{}
								names = append(names, *name)
							}
						}
					}
					continue
				}
				walk(item)
			}
		case []any:
			for _, item := range v {
				walk(item)
			}
		}
	}
	for _, content := range req.Contents {
		walk(content)
	}
	walk(req.SystemInstruction)
	return names
}

// inferenceFileIDs returns the uploaded files a chat or responses request names by file_id in its
// messages (a file or image block in chat, an input block in responses), each once.
func inferenceFileIDs(req *schemas.BifrostRequest) []string {
	var ids []string
	seen := make(map[string]struct{})
	add := func(id *string) {
		if id == nil || *id == "" {
			return
		}
		if _, dup := seen[*id]; dup {
			return
		}
		seen[*id] = struct{}{}
		ids = append(ids, *id)
	}
	switch {
	case req.ChatRequest != nil:
		for _, message := range req.ChatRequest.Input {
			if message.Content == nil {
				continue
			}
			for _, block := range message.Content.ContentBlocks {
				if block.File != nil {
					add(block.File.FileID)
					add(geminiFileName(block.File.FileURL))
				}
				if block.ImageURLStruct != nil {
					add(block.ImageURLStruct.FileID)
					add(geminiFileName(&block.ImageURLStruct.URL))
				}
			}
		}
	case req.ResponsesRequest != nil:
		for _, message := range req.ResponsesRequest.Input {
			if message.Content == nil {
				continue
			}
			for _, block := range message.Content.ContentBlocks {
				add(block.FileID)
				if block.ResponsesInputMessageContentBlockFile != nil {
					add(geminiFileName(block.ResponsesInputMessageContentBlockFile.FileURL))
				}
				if block.ResponsesInputMessageContentBlockImage != nil {
					add(geminiFileName(block.ResponsesInputMessageContentBlockImage.ImageURL))
				}
			}
		}
	}
	return ids
}

// equivalentProviders returns the provider names under which an object on provider may have been
// recorded: provider itself, and every configured provider that reaches the same upstream, a
// custom provider and the base it is built on, or two custom providers on one base, when they
// also share an endpoint. Objects live at the upstream, so an id recorded under one of these names
// is the same object under the others, and a key must not reach another key's object by switching
// the name it asks through. Sharing an API format is not sharing an upstream: a custom provider
// built on OpenAI but pointed at another service's base URL keeps its own objects, and an id that
// happens to match there is not the OpenAI object.
func (p *GovernancePlugin) equivalentProviders(provider string) []string {
	names := []string{provider}
	if p.inMemoryStore == nil {
		return names
	}
	configured := p.inMemoryStore.GetConfiguredProviders()
	if len(configured) == 0 {
		return names
	}
	base := baseProviderOf(schemas.ModelProvider(provider), configured)
	endpoint := providerEndpoint(schemas.ModelProvider(provider), configured)
	var aliases []string
	for name := range configured {
		if string(name) != provider && baseProviderOf(name, configured) == base && providerEndpoint(name, configured) == endpoint {
			aliases = append(aliases, string(name))
		}
	}
	sort.Strings(aliases)
	return append(names, aliases...)
}

// baseProviderDefaultEndpoints are the endpoints the base providers a custom provider can be built
// on send to when no base URL is configured, as their constructors set them. Bedrock is regional and
// has none; a provider missing here compares by its configured URL alone.
var baseProviderDefaultEndpoints = map[schemas.ModelProvider]string{
	schemas.OpenAI:      "https://api.openai.com",
	schemas.Anthropic:   "https://api.anthropic.com",
	schemas.Cohere:      "https://api.cohere.ai",
	schemas.Gemini:      "https://generativelanguage.googleapis.com/v1beta",
	schemas.HuggingFace: "https://router.huggingface.co",
	schemas.Replicate:   "https://api.replicate.com",
}

// providerEndpoint is the base URL a configured provider sends to, normalized for comparison. An
// unset base URL is its base provider's default, so a provider left on the default and one naming
// that default explicitly compare equal.
func providerEndpoint(name schemas.ModelProvider, configured map[schemas.ModelProvider]configstore.ProviderConfig) string {
	var url string
	if config, ok := configured[name]; ok && config.NetworkConfig != nil {
		url = config.NetworkConfig.BaseURL
	}
	if strings.TrimSpace(url) == "" {
		url = baseProviderDefaultEndpoints[baseProviderOf(name, configured)]
	}
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(url)), "/")
}

// baseProviderOf is the upstream a configured provider name reaches: the base of a custom
// provider, or the name itself.
func baseProviderOf(name schemas.ModelProvider, configured map[schemas.ModelProvider]configstore.ProviderConfig) schemas.ModelProvider {
	if config, ok := configured[name]; ok && config.CustomProviderConfig != nil && config.CustomProviderConfig.BaseProviderType != "" {
		return config.CustomProviderConfig.BaseProviderType
	}
	return name
}

// cachedContentCarrier returns the provider and extra params of a request that can run on a
// cached content, or false for any other request.
func cachedContentCarrier(req *schemas.BifrostRequest) (schemas.ModelProvider, map[string]interface{}, bool) {
	switch {
	case req.ChatRequest != nil:
		if req.ChatRequest.Params == nil {
			return req.ChatRequest.Provider, nil, true
		}
		return req.ChatRequest.Provider, req.ChatRequest.Params.ExtraParams, true
	case req.ResponsesRequest != nil:
		if req.ResponsesRequest.Params == nil {
			return req.ResponsesRequest.Provider, nil, true
		}
		return req.ResponsesRequest.Provider, req.ResponsesRequest.Params.ExtraParams, true
	case req.ImageGenerationRequest != nil:
		if req.ImageGenerationRequest.Params == nil {
			return req.ImageGenerationRequest.Provider, nil, true
		}
		return req.ImageGenerationRequest.Provider, req.ImageGenerationRequest.Params.ExtraParams, true
	case req.ImageEditRequest != nil:
		if req.ImageEditRequest.Params == nil {
			return req.ImageEditRequest.Provider, nil, true
		}
		return req.ImageEditRequest.Provider, req.ImageEditRequest.Params.ExtraParams, true
	case req.TranscriptionRequest != nil:
		if req.TranscriptionRequest.Params == nil {
			return req.TranscriptionRequest.Provider, nil, true
		}
		return req.TranscriptionRequest.Provider, req.TranscriptionRequest.Params.ExtraParams, true
	}
	return "", nil, false
}

// providerObjectOwners returns, per provider-side id, the virtual key recorded as its creator,
// and the ids whose creator cannot be told. The ownership row wins; for batches and videos the
// accounting job row is read next, and for files the batch rows naming the file as input, output
// or error. Ids nobody recorded are absent from both.
//
// Vertex records an object under the full resource name its create returned, and a short form
// (a bare job number, "cachedContents/<n>") cannot be expanded back to it without the key's
// project and region. So on Vertex a short id is also matched as the last segment of the full
// names on record, ownership rows and batch job rows alike, in one lookup per page. A job number
// is scoped to a project and region: when the full names that end in it belong to more than one
// virtual key, the id is ambiguous and reported as such rather than resolved by guessing. The
// suffix lookup is kept to providers built on Vertex.
func (p *GovernancePlugin) providerObjectOwners(ctx *schemas.BifrostContext, kind, provider string, ids []string) (map[string]string, map[string]struct{}, error) {
	return p.providerObjectOwnersOf(ctx, kind, provider, ids, false)
}

// providerObjectOwnersOf is providerObjectOwners with one more fact: sameObject says the ids
// are the forms of a single object (see providerObjectIDForms) rather than distinct objects. An
// ownership row under any form then answers for all of them, and the fallbacks, a suffix scan
// and a scan of the batch history for files, are skipped. The forms of one object cannot name
// two creators: an object is created once, by one key.
func (p *GovernancePlugin) providerObjectOwnersOf(ctx *schemas.BifrostContext, kind, provider string, ids []string, sameObject bool) (map[string]string, map[string]struct{}, error) {
	owners := make(map[string]string, len(ids))
	ambiguous := make(map[string]struct{})
	if len(ids) == 0 {
		return owners, ambiguous, nil
	}
	providers := p.equivalentProviders(provider)
	objectIDs := make([]string, 0, len(ids)*len(providers))
	for _, name := range providers {
		for _, id := range ids {
			objectIDs = append(objectIDs, configstoreTables.ProviderObjectID(kind, name, id))
		}
	}
	objects, err := p.configStore.GetProviderObjectsByIDs(ctx, objectIDs)
	if err != nil {
		return nil, nil, err
	}
	for _, object := range objects {
		if object != nil && object.VirtualKeyID != "" {
			owners[object.ObjectID] = object.VirtualKeyID
		}
	}
	// A second source that names a different key for an id already answered makes it ambiguous:
	// the id is refused rather than resolved by whichever source was read first.
	claim := func(id, owner string) {
		if id == "" || owner == "" {
			return
		}
		if existing, seen := owners[id]; seen {
			if existing != owner {
				ambiguous[id] = struct{}{}
			}
			return
		}
		owners[id] = owner
	}
	// The fallbacks answer for what the ownership rows did not, each id once.
	// A list page can carry thousands of ids, so past a handful they are deduplicated through a
	// set rather than a quadratic scan; a single-object request allocates no set.
	unresolved := make([]string, 0, len(ids))
	var queued map[string]struct{}
	if len(ids) > 8 {
		queued = make(map[string]struct{}, len(ids))
	}
	for _, id := range ids {
		if _, seen := owners[id]; seen {
			continue
		}
		if queued == nil {
			if !slices.Contains(unresolved, id) {
				unresolved = append(unresolved, id)
			}
			continue
		}
		if _, dup := queued[id]; !dup {
			queued[id] = struct{}{}
			unresolved = append(unresolved, id)
		}
	}
	if len(unresolved) == 0 || (sameObject && len(owners) > 0) {
		return owners, ambiguous, nil
	}
	if err := p.resolveShortResourceNames(ctx, kind, provider, providers, unresolved, owners, ambiguous); err != nil {
		return nil, nil, err
	}
	switch kind {
	case configstoreTables.ProviderObjectKindBatch, configstoreTables.ProviderObjectKindVideo:
		jobIDs := make([]string, 0, len(unresolved)*len(providers))
		for _, name := range providers {
			for _, id := range unresolved {
				jobIDs = append(jobIDs, configstoreTables.ProviderJobID(kind, name, id))
			}
		}
		jobs, err := p.configStore.GetProviderJobsByIDs(ctx, jobIDs)
		if err != nil {
			return nil, nil, err
		}
		for _, job := range jobs {
			if job != nil && job.VirtualKeyID != nil {
				claim(job.JobID, *job.VirtualKeyID)
			}
		}
	case configstoreTables.ProviderObjectKindFile:
		// Only the files a batch produced follow its key. Its input file was there before the
		// batch: one uploaded through the gateway has its own row, and one uploaded outside it
		// stays shared, so running a batch on it must not make it the batch key's.
		wanted := make(map[string]struct{}, len(unresolved))
		for _, id := range unresolved {
			wanted[id] = struct{}{}
		}
		for _, name := range providers {
			jobs, err := p.configStore.GetProviderJobsByFileIDs(ctx, name, unresolved)
			if err != nil {
				return nil, nil, err
			}
			for _, job := range jobs {
				if job == nil || job.VirtualKeyID == nil {
					continue
				}
				for _, fileID := range []string{derefString(job.OutputFileID), derefString(job.ErrorFileID)} {
					if _, ok := wanted[fileID]; ok {
						claim(fileID, *job.VirtualKeyID)
					}
				}
			}
		}
	}
	return owners, ambiguous, nil
}

// resolveShortResourceNames answers, for the short ids among unresolved, from the full resource
// names on record whose last segment they are: ownership rows of the kind, and for batches the
// accounting job rows. One lookup per store per provider name covers every short id on the page.
// A short id whose full names belong to one key is claimed for it; one whose full names belong to
// several keys is ambiguous. Only providers built on Vertex record full names.
func (p *GovernancePlugin) resolveShortResourceNames(ctx *schemas.BifrostContext, kind, provider string, providers, unresolved []string, owners map[string]string, ambiguous map[string]struct{}) error {
	if _, named := resourceCollections[kind]; !named || !p.builtOnVertex(provider) {
		return nil
	}
	bySuffix := make(map[string][]string)
	for _, id := range unresolved {
		suffix, ok := resourceNameSuffix(kind, id)
		if !ok {
			continue
		}
		// The bare and collection forms of one short id share a suffix; a row under either form
		// has answered for both, so no scan is spent on the other.
		if _, done := owners[suffix]; done {
			continue
		}
		if _, done := owners[resourceCollections[kind]+"/"+suffix]; done {
			continue
		}
		bySuffix[suffix] = append(bySuffix[suffix], id)
	}
	if len(bySuffix) == 0 {
		return nil
	}
	suffixes := make([]string, 0, len(bySuffix))
	for suffix := range bySuffix {
		suffixes = append(suffixes, suffix)
	}
	sort.Strings(suffixes)
	claimants := make(map[string]map[string]struct{}, len(suffixes))
	note := func(fullName, owner string) {
		if owner == "" {
			return
		}
		suffix := fullName[strings.LastIndex(fullName, "/")+1:]
		if _, wanted := bySuffix[suffix]; !wanted {
			return
		}
		if claimants[suffix] == nil {
			claimants[suffix] = make(map[string]struct{})
		}
		claimants[suffix][owner] = struct{}{}
	}
	for _, name := range providers {
		objects, err := p.configStore.GetProviderObjectsByObjectIDSuffixes(ctx, kind, name, suffixes)
		if err != nil {
			return err
		}
		for _, object := range objects {
			if object != nil {
				note(object.ObjectID, object.VirtualKeyID)
			}
		}
		if kind != configstoreTables.ProviderObjectKindBatch {
			continue
		}
		jobs, err := p.configStore.GetProviderJobsByJobIDSuffixes(ctx, kind, name, suffixes)
		if err != nil {
			return err
		}
		for _, job := range jobs {
			if job != nil && job.VirtualKeyID != nil {
				note(job.JobID, *job.VirtualKeyID)
			}
		}
	}
	for suffix, keys := range claimants {
		for _, id := range bySuffix[suffix] {
			if len(keys) > 1 {
				ambiguous[id] = struct{}{}
				continue
			}
			for owner := range keys {
				if _, seen := owners[id]; !seen {
					owners[id] = owner
				}
			}
		}
	}
	return nil
}

// resourceNameSuffix returns the last segment of a short form of a Google resource name: a bare
// id, or "<collection>/<id>" for the kind's collection. A full name ("projects/...") is not short,
// and neither is any other id with a path in it.
func resourceNameSuffix(kind, id string) (string, bool) {
	if id == "" {
		return "", false
	}
	if !strings.Contains(id, "/") {
		return id, true
	}
	collection := resourceCollections[kind]
	if collection != "" && strings.HasPrefix(id, collection+"/") && !strings.Contains(id[len(collection)+1:], "/") {
		return id[len(collection)+1:], true
	}
	return "", false
}

// builtOnVertex reports whether a provider name reaches Vertex: Vertex itself, or a custom
// provider built on it.
func (p *GovernancePlugin) builtOnVertex(provider string) bool {
	return configuredBaseProvider(p.inMemoryStore, schemas.ModelProvider(provider)) == schemas.Vertex
}

// enforceProviderObjectOwnership refuses a request for a provider object whose recorded creator
// is a virtual key other than the request's own. It runs after evaluation, which is what stamps
// the request's virtual key id. A store failure refuses the request rather than letting an
// unverified read through, and so does running without a config store: no creator is recorded
// anywhere then, so a virtual-key request that addresses a bound object cannot be checked and is
// refused. A list addresses nothing: its page is narrowed in the post-hook to what is recorded,
// and with nothing recorded there is nothing to hide, so it goes through unfiltered, as it did
// before objects were bound. Every object a request names is checked (see
// providerObjectReferences); the first one another key created refuses the request. None of
// this applies while client.enforce_vk_object_ownership is turned off; creators are still
// recorded then, so turning it back on also binds objects created meanwhile.
func (p *GovernancePlugin) enforceProviderObjectOwnership(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) *schemas.LLMPluginShortCircuit {
	vkID := requestVirtualKeyID(ctx)
	if vkID == "" {
		return nil
	}
	// With client.enforce_vk_object_ownership turned off nothing is refused, but an automatic
	// container still needs its marker so the post-hook records its creator.
	enforced := p.objectOwnershipEnforced()
	autoContainer := requestsAutomaticContainer(req)
	if !enforced && !autoContainer {
		return nil
	}
	refs := providerObjectReferences(req)
	if autoContainer {
		var named []string
		for _, ref := range refs {
			if ref.kind == configstoreTables.ProviderObjectKindContainer && !ref.isList {
				named = append(named, ref.ids...)
			}
		}
		ctx.SetValue(governanceAutoContainerContextKey, named)
	}
	if !enforced {
		return nil
	}
	for _, ref := range refs {
		if ref.isList {
			continue
		}
		if shortCircuit := p.refuseForeignProviderObject(ctx, req, ref, vkID); shortCircuit != nil {
			return shortCircuit
		}
	}
	// A native list is narrowed in the post-hook. A passthrough list is the provider's raw page,
	// which cannot be, so a virtual key is sent to the gateway's own list route; the objects the
	// path names (a container whose files are listed) were checked above first. With no config
	// store nothing is recorded and there is nothing to hide.
	for _, ref := range refs {
		if ref.passthroughList && p.configStore != nil {
			ctx.SetValue(governanceRejectedContextKey, true)
			return &schemas.LLMPluginShortCircuit{Error: &schemas.BifrostError{
				Type:       bifrost.Ptr(string(DecisionAccessBlocked)),
				StatusCode: bifrost.Ptr(403),
				Error:      &schemas.ErrorField{Message: fmt.Sprintf("listing %ss through a passthrough route is not available to virtual keys; use the gateway's list route, which shows the key's own objects", providerObjectLabel(ref.kind))},
			}}
		}
	}
	return nil
}

// refuseForeignProviderObject is enforceProviderObjectOwnership for one referenced object.
func (p *GovernancePlugin) refuseForeignProviderObject(ctx *schemas.BifrostContext, req *schemas.BifrostRequest, ref providerObjectRef, vkID string) *schemas.LLMPluginShortCircuit {
	label := providerObjectLabel(ref.kind)
	if p.configStore == nil {
		// A batch create only names its input file, and a container create or a request on a
		// cached content only names what it copies or runs on; with nothing recorded anywhere
		// those cannot be anyone else's on record, and the batch's own results stay refused
		// below. Creating and running are left open so a deployment without a config store can
		// still submit batches, build containers and use caches.
		if req.BatchCreateRequest != nil || ref.indirect {
			return nil
		}
		ctx.SetValue(governanceRejectedContextKey, true)
		return &schemas.LLMPluginShortCircuit{Error: &schemas.BifrostError{
			Type:       bifrost.Ptr(string(DecisionAccessBlocked)),
			StatusCode: bifrost.Ptr(403),
			Error:      &schemas.ErrorField{Message: fmt.Sprintf("%s access cannot be verified for virtual keys without a config store", label)},
		}}
	}
	owners, ambiguous, err := p.providerObjectOwnersOf(ctx, ref.kind, ref.provider, ref.ids, true)
	if err != nil {
		p.logger.Error("failed to load %s %s for ownership check: %v", label, ref.requestID, err)
		ctx.SetValue(governanceRejectedContextKey, true)
		return &schemas.LLMPluginShortCircuit{Error: &schemas.BifrostError{
			StatusCode: bifrost.Ptr(500),
			Error:      &schemas.ErrorField{Message: fmt.Sprintf("failed to verify access to %s '%s'", label, ref.requestID)},
		}}
	}
	for _, owner := range owners {
		if owner != vkID {
			ctx.SetValue(governanceRejectedContextKey, true)
			return &schemas.LLMPluginShortCircuit{Error: &schemas.BifrostError{
				Type:       bifrost.Ptr(string(DecisionAccessBlocked)),
				StatusCode: bifrost.Ptr(404),
				Error:      &schemas.ErrorField{Message: fmt.Sprintf("%s '%s' not found", label, ref.requestID)},
			}}
		}
	}
	if len(ambiguous) > 0 {
		// A short Vertex id that several keys' full names end in names one job per project, which
		// cannot be told apart before a key is chosen. The caller is told how to name it exactly.
		ctx.SetValue(governanceRejectedContextKey, true)
		return &schemas.LLMPluginShortCircuit{Error: &schemas.BifrostError{
			Type:       bifrost.Ptr(string(DecisionAccessBlocked)),
			StatusCode: bifrost.Ptr(400),
			Error:      &schemas.ErrorField{Message: fmt.Sprintf("%s '%s' matches objects in more than one project; address it by its full resource name", label, ref.requestID)},
		}}
	}
	return nil
}

// hiddenProviderObjects returns, from a page of ids, the ones another virtual key created, as a
// predicate on the listed id. Every form of each id is looked up, and an id is hidden when any
// of its forms is another key's. The predicate is nil when nothing on the page is hidden.
func (p *GovernancePlugin) hiddenProviderObjects(ctx *schemas.BifrostContext, kind, provider, vkID string, ids []string) (func(id string) bool, error) {
	forms := make([]string, 0, len(ids)*2)
	for _, id := range ids {
		forms = append(forms, providerObjectIDForms(kind, schemas.ModelProvider(provider), id)...)
	}
	owners, ambiguous, err := p.providerObjectOwners(ctx, kind, provider, forms)
	if err != nil {
		return nil, err
	}
	hidden := make(map[string]struct{})
	for id, owner := range owners {
		if owner != vkID {
			hidden[id] = struct{}{}
		}
	}
	// An item whose creator cannot be told is not shown rather than guessed at.
	for id := range ambiguous {
		hidden[id] = struct{}{}
	}
	if len(hidden) == 0 {
		return nil, nil
	}
	return func(id string) bool {
		for _, form := range providerObjectIDForms(kind, schemas.ModelProvider(provider), id) {
			if _, ok := hidden[form]; ok {
				return true
			}
		}
		return false
	}, nil
}

// filterProviderObjectList narrows a list of provider objects to the ones the request's virtual
// key may see. Pagination cursors are the provider's and are left alone; only the page's items
// are narrowed. A provider can attach the whole unfiltered page as the raw response (OpenAI does
// so on every batch item), so once anything is removed the raw page is dropped from the list and
// from the kept items. If the ownership lookup fails it returns an error and leaves the list
// untouched; the caller fails the request rather than send a page it could not check.
func (p *GovernancePlugin) filterProviderObjectList(ctx *schemas.BifrostContext, provider string, result *schemas.BifrostResponse) error {
	if p.configStore == nil || result == nil || !p.objectOwnershipEnforced() {
		return nil
	}
	vkID := requestVirtualKeyID(ctx)
	if vkID == "" {
		return nil
	}
	fail := func(kind string, err error) error {
		label := providerObjectLabel(kind)
		p.logger.Error("failed to load %s ownership for list filter: %v", label, err)
		return fmt.Errorf("failed to verify access to the %s list", label)
	}
	switch {
	case result.BatchListResponse != nil && len(result.BatchListResponse.Data) > 0:
		list := result.BatchListResponse
		ids := make([]string, 0, len(list.Data))
		for _, item := range list.Data {
			ids = append(ids, item.ID)
		}
		hidden, err := p.hiddenProviderObjects(ctx, configstoreTables.ProviderObjectKindBatch, provider, vkID, ids)
		if err != nil {
			return fail(configstoreTables.ProviderObjectKindBatch, err)
		}
		if hidden == nil {
			return nil
		}
		kept := make([]schemas.BifrostBatchRetrieveResponse, 0, len(list.Data))
		for _, item := range list.Data {
			if !hidden(item.ID) {
				item.ExtraFields.RawResponse = nil
				kept = append(kept, item)
			}
		}
		list.Data = kept
		list.ExtraFields.RawResponse = nil
	case result.FileListResponse != nil && len(result.FileListResponse.Data) > 0:
		list := result.FileListResponse
		ids := make([]string, 0, len(list.Data))
		for _, item := range list.Data {
			ids = append(ids, item.ID)
		}
		hidden, err := p.hiddenProviderObjects(ctx, configstoreTables.ProviderObjectKindFile, provider, vkID, ids)
		if err != nil {
			return fail(configstoreTables.ProviderObjectKindFile, err)
		}
		if hidden == nil {
			return nil
		}
		kept := make([]schemas.FileObject, 0, len(list.Data))
		for _, item := range list.Data {
			if !hidden(item.ID) {
				kept = append(kept, item)
			}
		}
		list.Data = kept
		list.ExtraFields.RawResponse = nil
	case result.VideoListResponse != nil && len(result.VideoListResponse.Data) > 0:
		list := result.VideoListResponse
		ids := make([]string, 0, len(list.Data))
		for _, item := range list.Data {
			ids = append(ids, item.ID)
		}
		hidden, err := p.hiddenProviderObjects(ctx, configstoreTables.ProviderObjectKindVideo, provider, vkID, ids)
		if err != nil {
			return fail(configstoreTables.ProviderObjectKindVideo, err)
		}
		if hidden == nil {
			return nil
		}
		kept := make([]schemas.VideoObject, 0, len(list.Data))
		for _, item := range list.Data {
			if !hidden(item.ID) {
				kept = append(kept, item)
			}
		}
		list.Data = kept
		list.ExtraFields.RawResponse = nil
	case result.ContainerListResponse != nil && len(result.ContainerListResponse.Data) > 0:
		list := result.ContainerListResponse
		ids := make([]string, 0, len(list.Data))
		for _, item := range list.Data {
			ids = append(ids, item.ID)
		}
		hidden, err := p.hiddenProviderObjects(ctx, configstoreTables.ProviderObjectKindContainer, provider, vkID, ids)
		if err != nil {
			return fail(configstoreTables.ProviderObjectKindContainer, err)
		}
		if hidden == nil {
			return nil
		}
		kept := make([]schemas.ContainerObject, 0, len(list.Data))
		for _, item := range list.Data {
			if !hidden(item.ID) {
				kept = append(kept, item)
			}
		}
		list.Data = kept
		list.ExtraFields.RawResponse = nil
	case result.ContainerFileListResponse != nil && len(result.ContainerFileListResponse.Data) > 0:
		list := result.ContainerFileListResponse
		ids := make([]string, 0, len(list.Data))
		for _, item := range list.Data {
			ids = append(ids, item.ID)
		}
		hidden, err := p.hiddenProviderObjects(ctx, configstoreTables.ProviderObjectKindContainerFile, provider, vkID, ids)
		if err != nil {
			return fail(configstoreTables.ProviderObjectKindContainerFile, err)
		}
		if hidden == nil {
			return nil
		}
		kept := make([]schemas.ContainerFileObject, 0, len(list.Data))
		for _, item := range list.Data {
			if !hidden(item.ID) {
				kept = append(kept, item)
			}
		}
		list.Data = kept
		list.ExtraFields.RawResponse = nil
	case result.CachedContentListResponse != nil && len(result.CachedContentListResponse.CachedContents) > 0:
		list := result.CachedContentListResponse
		ids := make([]string, 0, len(list.CachedContents))
		for _, item := range list.CachedContents {
			ids = append(ids, item.Name)
		}
		hidden, err := p.hiddenProviderObjects(ctx, configstoreTables.ProviderObjectKindCachedContent, provider, vkID, ids)
		if err != nil {
			return fail(configstoreTables.ProviderObjectKindCachedContent, err)
		}
		if hidden == nil {
			return nil
		}
		kept := make([]schemas.CachedContentObject, 0, len(list.CachedContents))
		for _, item := range list.CachedContents {
			if !hidden(item.Name) {
				kept = append(kept, item)
			}
		}
		list.CachedContents = kept
		list.ExtraFields.RawResponse = nil
	}
	return nil
}

// recordProviderObjectLifecycle remembers the virtual key a create response was produced under,
// and forgets an object a delete response confirms gone. A video create is told apart from a
// retrieve, which answers with the same shape, by the request type. An id is recorded only as the
// create returned it; short forms of a full resource name are resolved by suffix at lookup time
// (see resolveShortResourceNames). A delete forgets every form of the id it names, and a delete in
// short form also forgets the deleting key's own full name that ends in it. A write that fails is
// logged: the object exists either way, and the response carrying its id is the caller's.
func (p *GovernancePlugin) recordProviderObjectLifecycle(ctx *schemas.BifrostContext, requestType schemas.RequestType, provider string, result *schemas.BifrostResponse) {
	if p.configStore == nil || result == nil || provider == "" {
		return
	}
	vkID := requestVirtualKeyID(ctx)
	if vkID == "" {
		return
	}
	if named, auto := ctx.Value(governanceAutoContainerContextKey).([]string); auto {
		p.recordAutomaticContainers(ctx, provider, vkID, result, named)
	}
	var createdKind, createdID string
	var deletedKind, deletedID string
	switch {
	case result.FileUploadResponse != nil && requestType == schemas.FileUploadRequest:
		createdKind, createdID = configstoreTables.ProviderObjectKindFile, result.FileUploadResponse.ID
	case result.VideoGenerationResponse != nil && (requestType == schemas.VideoGenerationRequest || requestType == schemas.VideoEditRequest || requestType == schemas.VideoRemixRequest):
		createdKind, createdID = configstoreTables.ProviderObjectKindVideo, result.VideoGenerationResponse.ID
	case result.ContainerCreateResponse != nil:
		createdKind, createdID = configstoreTables.ProviderObjectKindContainer, result.ContainerCreateResponse.ID
	case result.ContainerFileCreateResponse != nil:
		createdKind, createdID = configstoreTables.ProviderObjectKindContainerFile, result.ContainerFileCreateResponse.ID
	case result.CachedContentCreateResponse != nil:
		createdKind, createdID = configstoreTables.ProviderObjectKindCachedContent, result.CachedContentCreateResponse.Name
	case result.BatchCreateResponse != nil:
		createdKind, createdID = configstoreTables.ProviderObjectKindBatch, result.BatchCreateResponse.ID
	case result.BatchRetrieveResponse != nil && requestType == schemas.BatchRetrieveRequest:
		p.recordBatchFiles(ctx, provider, result.BatchRetrieveResponse)
		return
	case result.FileDeleteResponse != nil && result.FileDeleteResponse.Deleted:
		deletedKind, deletedID = configstoreTables.ProviderObjectKindFile, result.FileDeleteResponse.ID
	case result.VideoDeleteResponse != nil && result.VideoDeleteResponse.Deleted:
		deletedKind, deletedID = configstoreTables.ProviderObjectKindVideo, result.VideoDeleteResponse.ID
	case result.ContainerDeleteResponse != nil && result.ContainerDeleteResponse.Deleted:
		deletedKind, deletedID = configstoreTables.ProviderObjectKindContainer, result.ContainerDeleteResponse.ID
	case result.ContainerFileDeleteResponse != nil && result.ContainerFileDeleteResponse.Deleted:
		deletedKind, deletedID = configstoreTables.ProviderObjectKindContainerFile, result.ContainerFileDeleteResponse.ID
	case result.CachedContentDeleteResponse != nil && result.CachedContentDeleteResponse.Deleted:
		deletedKind, deletedID = configstoreTables.ProviderObjectKindCachedContent, result.CachedContentDeleteResponse.Name
	case result.BatchDeleteResponse != nil:
		deletedKind, deletedID = configstoreTables.ProviderObjectKindBatch, result.BatchDeleteResponse.ID
	default:
		return
	}
	if createdKind != "" && createdID != "" {
		// Recorded as the provider named it, and under no short form: a Vertex job number is one
		// project's, so a row keyed by it would be shared by every project's job of that number.
		// Short forms are resolved against the full names when they are asked for instead.
		p.recordProviderObjectCreator(ctx, createdKind, provider, createdID, vkID)
		return
	}
	// The object is forgotten under every provider name that reaches the same upstream: the delete
	// was admitted through one of them, and a row recorded through another would otherwise linger.
	aliases := p.equivalentProviders(provider)
	for _, alias := range aliases {
		for _, id := range providerObjectIDForms(deletedKind, schemas.ModelProvider(alias), deletedID) {
			if err := p.configStore.DeleteProviderObject(ctx, configstoreTables.ProviderObjectID(deletedKind, alias, id)); err != nil {
				p.logger.Warn("failed to forget deleted %s %s on %s: %v", providerObjectLabel(deletedKind), id, alias, err)
			}
		}
	}
	if suffix, short := resourceNameSuffix(deletedKind, deletedID); short && p.builtOnVertex(provider) {
		p.forgetShortFormDelete(ctx, deletedKind, aliases, suffix, vkID)
	}
}

// forgetShortFormDelete forgets the deleting key's own full name that a delete by a short form
// removed. The provider deleted one object and echoes only the short form; when exactly one of the
// key's full names ends in it, that one is forgotten. When several do (the key's jobs with one
// number in two projects), which one went is unknown, so every binding is kept: a stale binding
// to the same key is harmless, an unbound survivor is not. Other keys' rows are never touched.
func (p *GovernancePlugin) forgetShortFormDelete(ctx *schemas.BifrostContext, kind string, providers []string, suffix, vkID string) {
	var own []*configstoreTables.TableProviderObject
	for _, name := range providers {
		objects, err := p.configStore.GetProviderObjectsByObjectIDSuffixes(ctx, kind, name, []string{suffix})
		if err != nil {
			p.logger.Warn("failed to look up the full name of deleted %s %s on %s: %v", providerObjectLabel(kind), suffix, name, err)
			return
		}
		for _, object := range objects {
			if object != nil && object.VirtualKeyID == vkID {
				own = append(own, object)
			}
		}
	}
	if len(own) != 1 {
		return
	}
	if err := p.configStore.DeleteProviderObject(ctx, own[0].ID); err != nil {
		p.logger.Warn("failed to forget deleted %s %s on %s by its full name: %v", providerObjectLabel(kind), suffix, own[0].Provider, err)
	}
}

// recordAutomaticContainers records the containers a Responses answer names for the key that asked
// for an automatic container: the container on the response and the one each code_interpreter call
// ran in. The ownership write keeps the first creator, so a container already recorded keeps its
// owner. Only requests that asked for an automatic container get here (see
// requestsAutomaticContainer), and named lists the containers the same request named by id, which
// the response may list beside the automatic one: a container named by id is never claimed by
// using it.
func (p *GovernancePlugin) recordAutomaticContainers(ctx *schemas.BifrostContext, provider, vkID string, result *schemas.BifrostResponse, named []string) {
	resp := result.ResponsesResponse
	if resp == nil && result.ResponsesStreamResponse != nil {
		resp = result.ResponsesStreamResponse.Response
	}
	if resp == nil {
		return
	}
	seen := make(map[string]struct{}, len(named))
	for _, id := range named {
		seen[id] = struct{}{}
	}
	record := func(id string) {
		if id == "" {
			return
		}
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		p.recordProviderObjectCreator(ctx, configstoreTables.ProviderObjectKindContainer, provider, id, vkID)
	}
	if resp.Container != nil {
		record(resp.Container.ID)
	}
	for _, item := range resp.Output {
		if item.ResponsesToolMessage != nil && item.ResponsesToolMessage.ResponsesCodeInterpreterToolCall != nil {
			record(item.ResponsesToolMessage.ResponsesCodeInterpreterToolCall.ContainerID)
		}
	}
}

// recordProviderObjectCreator writes the ownership row of an object, trying once more when the
// write fails: the object exists at the provider either way, and a bound object beats an unbound
// one. A second failure is logged and the object follows the unbound-object rule.
func (p *GovernancePlugin) recordProviderObjectCreator(ctx *schemas.BifrostContext, kind, provider, objectID, vkID string) {
	row := &configstoreTables.TableProviderObject{
		ID:           configstoreTables.ProviderObjectID(kind, provider, objectID),
		Kind:         kind,
		Provider:     provider,
		ObjectID:     objectID,
		VirtualKeyID: vkID,
	}
	err := p.configStore.UpsertProviderObject(ctx, row)
	if err != nil {
		err = p.configStore.UpsertProviderObject(ctx, row)
	}
	if err != nil {
		p.logger.Warn("failed to record the creating virtual key of %s %s on %s (after a retry): %v", providerObjectLabel(kind), objectID, provider, err)
	}
}

// recordBatchFiles binds the output and error files a batch retrieve names to the batch's
// recorded creator. Those files exist only once the batch has run, so the create response could
// not record them, and the accounting rows that also name them are written only when the batch
// sweeper runs, which needs the logging plugin. A batch nobody recorded hands its files to no
// one: the retrieving key is not made their owner.
func (p *GovernancePlugin) recordBatchFiles(ctx *schemas.BifrostContext, provider string, batch *schemas.BifrostBatchRetrieveResponse) {
	fileIDs := make([]string, 0, 2)
	for _, fileID := range []string{derefString(batch.OutputFileID), derefString(batch.ErrorFileID)} {
		if fileID != "" {
			fileIDs = append(fileIDs, fileID)
		}
	}
	if len(fileIDs) == 0 || batch.ID == "" {
		return
	}
	owners, _, err := p.providerObjectOwners(ctx, configstoreTables.ProviderObjectKindBatch, provider, []string{batch.ID})
	if err != nil {
		p.logger.Warn("failed to look up the creating virtual key of batch %s on %s: %v", batch.ID, provider, err)
		return
	}
	owner := owners[batch.ID]
	if owner == "" {
		return
	}
	for _, fileID := range fileIDs {
		p.recordProviderObjectCreator(ctx, configstoreTables.ProviderObjectKindFile, provider, fileID, owner)
	}
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
