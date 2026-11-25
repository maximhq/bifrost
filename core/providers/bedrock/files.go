package bedrock

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// escapeS3KeyForURL escapes each segment of an S3 key path individually.
// This prevents signature and URL parsing failures with special characters.
// We can't use url.PathEscape on the full key as it escapes "/" to "%2F",
// but we need each segment properly escaped per RFC 3986 for AWS SigV4 signing.
func escapeS3KeyForURL(key string) string {
	if key == "" {
		return ""
	}
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// s3BucketRe matches the DNS-compatible bucket names a virtual-hosted S3 URL can carry.
var s3BucketRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// validateS3Bucket rejects bucket names that would change the request host, since the
// bucket becomes the leading label of "https://{bucket}.{s3host}/".
func validateS3Bucket(bucket string) *schemas.BifrostError {
	if !s3BucketRe.MatchString(bucket) {
		return providerUtils.NewBifrostBadRequestError(fmt.Sprintf("invalid s3 bucket name: %q", bucket))
	}
	return nil
}

// keyAllowsS3Object reports whether key may be used for objectKey in bucket. A key that
// lists buckets in batch_s3_config is limited to them: the bucket must be listed, and when
// the entry carries a prefix the object key must sit inside it. A key that lists no buckets
// is not limited, which keeps deployments that name the bucket per request working.
//
// The S3 request is signed with the key's own AWS credentials, so without this rule a
// caller could point a file or batch operation at any bucket those credentials can reach.
func keyAllowsS3Object(key schemas.Key, bucket, objectKey string) bool {
	if key.BedrockKeyConfig == nil || key.BedrockKeyConfig.BatchS3Config == nil || len(key.BedrockKeyConfig.BatchS3Config.Buckets) == 0 {
		return true
	}
	for _, allowed := range key.BedrockKeyConfig.BatchS3Config.Buckets {
		if allowed.BucketName != bucket {
			continue
		}
		prefix := strings.TrimPrefix(allowed.Prefix, "/")
		if prefix == "" {
			return true
		}
		// A prefix names a folder, so "jobs" and "jobs/" both mean keys under "jobs/".
		if !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
		if !strings.HasPrefix(objectKey, prefix) {
			continue
		}
		// S3 keys are literal, but a ".." segment can be collapsed on the way to S3.
		if slices.Contains(strings.Split(objectKey, "/"), "..") {
			continue
		}
		return true
	}
	return false
}

// keysAllowingS3Object returns the keys that may be used for objectKey in bucket, in their
// original order. When no key may be used the operation is refused with a 403.
func keysAllowingS3Object(keys []schemas.Key, bucket, objectKey string) ([]schemas.Key, *schemas.BifrostError) {
	// The caller's slice is returned as it came until a key is refused, so a deployment that
	// lists no buckets, where nothing is ever filtered, allocates nothing here.
	allowed := keys
	filtered := false
	for i := range keys {
		if keyAllowsS3Object(keys[i], bucket, objectKey) {
			if filtered {
				allowed = append(allowed, keys[i])
			}
			continue
		}
		if !filtered {
			allowed = append(make([]schemas.Key, 0, len(keys)), keys[:i]...)
			filtered = true
		}
	}
	if len(allowed) == 0 {
		return nil, &schemas.BifrostError{
			IsBifrostError: true,
			StatusCode:     schemas.Ptr(http.StatusForbidden),
			Error: &schemas.ErrorField{
				Message: fmt.Sprintf("s3 location %q is not in the bucket list (batch_s3_config) of any key available for this request", "s3://"+bucket+"/"+objectKey),
			},
		}
	}
	return allowed, nil
}

// keysAllowingS3Folder is keysAllowingS3Object for a location that names a folder, such as
// a batch output location. "jobs" and "jobs/" name the same folder, so it is checked with
// its trailing slash: a key limited to "jobs/" accepts both spellings and still refuses
// "jobs-archive". The bucket root stays the empty key.
func keysAllowingS3Folder(keys []schemas.Key, bucket, folderKey string) ([]schemas.Key, *schemas.BifrostError) {
	if folderKey != "" && !strings.HasSuffix(folderKey, "/") {
		folderKey += "/"
	}
	return keysAllowingS3Object(keys, bucket, folderKey)
}

// batchInputFolder is the folder the input generated for an inline batch is written to.
const batchInputFolder = "bifrost-batch-input/"

// batchInputS3Location returns where the input generated for an inline batch is written:
// the output bucket, in batchInputFolder. A key limited to a prefix of that bucket gets the
// folder inside the prefix that covers the output, so the key can write its own input and
// read or delete it afterwards. When the output is the prefix itself the input folder sits
// inside the output folder; BatchResults skips it by name.
func batchInputS3Location(key schemas.Key, outputS3URI, jobName string) (bucket, objectKey string) {
	bucket, outputKey := parseS3URI(outputS3URI)
	objectKey = generateBatchInputS3Key(jobName)
	if key.BedrockKeyConfig == nil || key.BedrockKeyConfig.BatchS3Config == nil {
		return bucket, objectKey
	}
	if outputKey != "" && !strings.HasSuffix(outputKey, "/") {
		outputKey += "/"
	}
	for _, allowed := range key.BedrockKeyConfig.BatchS3Config.Buckets {
		if allowed.BucketName != bucket {
			continue
		}
		prefix := strings.TrimPrefix(allowed.Prefix, "/")
		if prefix == "" {
			return bucket, objectKey
		}
		if !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
		if strings.HasPrefix(outputKey, prefix) {
			return bucket, prefix + objectKey
		}
	}
	return bucket, objectKey
}

// s3FolderURI returns an s3:// URI in the form that names a folder: with a trailing slash on
// its key. "s3://b/jobs" and "s3://b/jobs/" name the same folder; listing the first as written
// would list the prefix "jobs", which is not inside a key's "jobs/". A URI with no key (the
// bucket itself) is returned unchanged.
func s3FolderURI(uri string) string {
	_, objectKey := parseS3URI(uri)
	if objectKey == "" || strings.HasSuffix(objectKey, "/") {
		return uri
	}
	return uri + "/"
}

// isBatchResultFile reports whether an object listed under a batch output folder holds
// results: the JSONL files, not the manifest. A ".jsonl.out" file is always a result. A plain
// ".jsonl" file sitting directly in a batchInputFolder is the input generated for an inline
// batch, which lands inside the output folder when the output is the key's prefix itself.
func isBatchResultFile(fileID string) bool {
	if strings.HasSuffix(fileID, ".jsonl.out") {
		return true
	}
	if !strings.HasSuffix(fileID, ".jsonl") {
		return false
	}
	// The folder is matched in the object key: a bucket can carry the same name.
	_, objectKey := parseS3URI(fileID)
	dir := objectKey[:strings.LastIndexByte(objectKey, '/')+1]
	return dir != batchInputFolder && !strings.HasSuffix(dir, "/"+batchInputFolder)
}

// s3KeyErrors collects the failures of an S3 file operation that tries every key in turn.
// Only a key in the bucket's region gets S3's real answer (404, 403, ...); the others get a
// redirect to the bucket's regional endpoint. The real answer is what the caller needs, so it
// wins over a wrong-region redirect, which in turn wins over a key that never reached S3.
type s3KeyErrors struct {
	answer      *schemas.BifrostError // S3's answer from the bucket's region
	wrongRegion *schemas.BifrostError // S3 redirected: the key's region is not the bucket's
	local       *schemas.BifrostError // the request never got an S3 answer
}

// status records a non-success S3 status. The key's regional endpoint is not the bucket's when S3
// answers with a 3xx, or names another region in x-amz-bucket-region: a bucket in a region launched
// after March 2019 answers a wrong-region request with 400 instead of a redirect. Such a response is
// reported as a 400 that names the bucket's region, because a redirect status is nothing a client can
// act on. Any other status is S3's answer from the bucket's region, passed in already built.
func (e *s3KeyErrors) status(statusCode int, header http.Header, bucket, keyRegion string, answer *schemas.BifrostError) {
	bucketRegion := header.Get("x-amz-bucket-region")
	redirected := statusCode >= 300 && statusCode < 400
	if !redirected && (bucketRegion == "" || strings.EqualFold(bucketRegion, keyRegion)) {
		e.answer = answer
		return
	}
	msg := fmt.Sprintf("s3 bucket %q is not in the key's region %s (S3 answered %d)", bucket, keyRegion, statusCode)
	if bucketRegion != "" {
		msg = fmt.Sprintf("s3 bucket %q is in region %s, not the key's region %s; add a bedrock key for %s", bucket, bucketRegion, keyRegion, bucketRegion)
	}
	e.wrongRegion = providerUtils.NewProviderAPIError(msg, nil, http.StatusBadRequest, nil, nil)
}

// result is the error to return once every key has failed.
func (e *s3KeyErrors) result() *schemas.BifrostError {
	if e.answer != nil {
		return e.answer
	}
	if e.wrongRegion != nil {
		return e.wrongRegion
	}
	return e.local
}

// parseS3URI parses an S3 URI (s3://bucket/key or bucket-name) and returns bucket name and key.
func parseS3URI(uri string) (bucket, key string) {
	if strings.HasPrefix(uri, "s3://") {
		uri = strings.TrimPrefix(uri, "s3://")
		parts := strings.SplitN(uri, "/", 2)
		bucket = parts[0]
		if len(parts) > 1 {
			key = parts[1]
		}
	} else {
		// Assume it's just a bucket name
		bucket = uri
	}
	return
}

// bedrockS3LocationFromURL converts an s3:// content URL into the s3Location member of
// a Converse source union, so the object reference travels with the request instead of
// its bytes.
//
// Converse resolves S3 objects itself -- s3Location is a documented member of both
// DocumentSource and ImageSource -- which means Bifrost neither has to download the
// object nor squeeze it through the 25 MiB inline cap. ok is false for anything that is
// not an s3:// URI, leaving the caller's existing http(s) fetch path in charge.
//
// A bucket-only URI ("s3://bucket") is rejected: Converse needs an object, and parseS3URI
// happily returns an empty key for it.
func bedrockS3LocationFromURL(rawURL string) (*BedrockS3Location, bool) {
	if !strings.HasPrefix(rawURL, "s3://") {
		return nil, false
	}
	bucket, key := parseS3URI(rawURL)
	if bucket == "" || key == "" {
		return nil, false
	}
	return &BedrockS3Location{URI: rawURL}, true
}

// bedrockS3LocationUnsupportedError refuses an s3:// reference bound for a model whose
// Converse backend cannot read it. See schemas.BedrockModelSupportsS3Location for why the
// alternative is not "let Bedrock reject it": Bedrock does not reject it, it drops the
// source member and lets the model fail on the resulting empty source, several layers
// away from anything the caller wrote.
//
// inlineHint names the working alternative for this content kind, because the two differ:
// a document travels as base64 file_data, an image as a data: URL. Presigning is offered as
// the other way out for the same reason Vertex's classifyURLSource offers it: Bifrost cannot
// fetch the object on the caller's behalf here, since AWS credentials live on
// BedrockKeyConfig and never reach a request converter.
func bedrockS3LocationUnsupportedError(model, kind, rawURL, inlineHint string) error {
	return providerUtils.InvalidRequestErrorf(
		"model %q does not support s3:// references on Bedrock Converse (%s %s): the s3Location source is dropped for this model and the request then fails inside the model itself. Send the %s %s, or presign the object to an https:// URL",
		model, kind, rawURL, kind, inlineHint,
	)
}

// S3ListObjectsResponse represents S3 ListObjectsV2 response.
type S3ListObjectsResponse struct {
	Contents              []S3Object `json:"contents"`
	IsTruncated           bool       `json:"isTruncated"`
	NextContinuationToken string     `json:"nextContinuationToken,omitempty"`
}

// S3Object represents an S3 object in list response.
type S3Object struct {
	Key          string    `json:"key"`
	Size         int64     `json:"size"`
	LastModified time.Time `json:"lastModified"`
}

// parseS3ListResponse parses S3 ListObjectsV2 XML response.
func parseS3ListResponse(body []byte, resp *S3ListObjectsResponse) error {
	// S3 returns XML, so we need to parse it
	// Try JSON first (some S3-compatible services return JSON)
	if err := sonic.Unmarshal(body, resp); err == nil && len(resp.Contents) > 0 {
		return nil
	}

	// Parse XML using simple string matching for key fields
	// This is a lightweight approach that doesn't require encoding/xml
	bodyStr := string(body)

	// Parse IsTruncated
	if strings.Contains(bodyStr, "<IsTruncated>true</IsTruncated>") {
		resp.IsTruncated = true
	}

	// Parse NextContinuationToken
	if start := strings.Index(bodyStr, "<NextContinuationToken>"); start >= 0 {
		start += len("<NextContinuationToken>")
		if end := strings.Index(bodyStr[start:], "</NextContinuationToken>"); end >= 0 {
			resp.NextContinuationToken = bodyStr[start : start+end]
		}
	}

	// Parse Contents
	contents := bodyStr
	for {
		start := strings.Index(contents, "<Contents>")
		if start < 0 {
			break
		}
		end := strings.Index(contents[start:], "</Contents>")
		if end < 0 {
			break
		}

		contentBlock := contents[start : start+end+len("</Contents>")]
		contents = contents[start+end+len("</Contents>"):]

		obj := S3Object{}

		// Parse Key
		if keyStart := strings.Index(contentBlock, "<Key>"); keyStart >= 0 {
			keyStart += len("<Key>")
			if keyEnd := strings.Index(contentBlock[keyStart:], "</Key>"); keyEnd >= 0 {
				obj.Key = html.UnescapeString(contentBlock[keyStart : keyStart+keyEnd])
			}
		}

		// Parse Size
		if sizeStart := strings.Index(contentBlock, "<Size>"); sizeStart >= 0 {
			sizeStart += len("<Size>")
			if sizeEnd := strings.Index(contentBlock[sizeStart:], "</Size>"); sizeEnd >= 0 {
				sizeStr := contentBlock[sizeStart : sizeStart+sizeEnd]
				fmt.Sscanf(sizeStr, "%d", &obj.Size)
			}
		}

		// Parse LastModified
		if lmStart := strings.Index(contentBlock, "<LastModified>"); lmStart >= 0 {
			lmStart += len("<LastModified>")
			if lmEnd := strings.Index(contentBlock[lmStart:], "</LastModified>"); lmEnd >= 0 {
				lmStr := contentBlock[lmStart : lmStart+lmEnd]
				if t, err := time.Parse(time.RFC3339Nano, lmStr); err == nil {
					obj.LastModified = t
				}
			}
		}

		if obj.Key != "" {
			resp.Contents = append(resp.Contents, obj)
		}
	}

	return nil
}

// ==================== BEDROCK FILE TYPE CONVERTERS ====================

// ToBedrockFileUploadResponse converts a Bifrost file upload response to Bedrock format.
func ToBedrockFileUploadResponse(resp *schemas.BifrostFileUploadResponse) *BedrockFileUploadResponse {
	if resp == nil {
		return nil
	}

	// Parse S3 URI to get bucket and key
	bucket, key := parseS3URI(resp.ID)

	return &BedrockFileUploadResponse{
		S3Uri:       resp.ID,
		Bucket:      bucket,
		Key:         key,
		SizeBytes:   resp.Bytes,
		ContentType: "application/jsonl",
		CreatedAt:   resp.CreatedAt,
	}
}

// ToBedrockFileListResponse converts a Bifrost file list response to Bedrock format.
func ToBedrockFileListResponse(resp *schemas.BifrostFileListResponse) *BedrockFileListResponse {
	if resp == nil {
		return nil
	}

	files := make([]BedrockFileInfo, len(resp.Data))
	for i, f := range resp.Data {
		_, key := parseS3URI(f.ID)
		files[i] = BedrockFileInfo{
			S3Uri:        f.ID,
			Key:          key,
			SizeBytes:    f.Bytes,
			LastModified: f.CreatedAt,
		}
	}

	return &BedrockFileListResponse{
		Files:       files,
		IsTruncated: resp.HasMore,
	}
}

// ToBedrockFileRetrieveResponse converts a Bifrost file retrieve response to Bedrock format.
func ToBedrockFileRetrieveResponse(resp *schemas.BifrostFileRetrieveResponse) *BedrockFileRetrieveResponse {
	if resp == nil {
		return nil
	}

	_, key := parseS3URI(resp.ID)

	return &BedrockFileRetrieveResponse{
		S3Uri:        resp.ID,
		Key:          key,
		SizeBytes:    resp.Bytes,
		LastModified: resp.CreatedAt,
		ContentType:  "application/jsonl",
	}
}

// ToBedrockFileDeleteResponse converts a Bifrost file delete response to Bedrock format.
func ToBedrockFileDeleteResponse(resp *schemas.BifrostFileDeleteResponse) *BedrockFileDeleteResponse {
	if resp == nil {
		return nil
	}

	return &BedrockFileDeleteResponse{
		S3Uri:   resp.ID,
		Deleted: resp.Deleted,
	}
}

// ToBedrockFileContentResponse converts a Bifrost file content response to Bedrock format.
func ToBedrockFileContentResponse(resp *schemas.BifrostFileContentResponse) *BedrockFileContentResponse {
	if resp == nil {
		return nil
	}

	return &BedrockFileContentResponse{
		S3Uri:       resp.FileID,
		Content:     resp.Content,
		ContentType: resp.ContentType,
		SizeBytes:   int64(len(resp.Content)),
	}
}

// ==================== S3 API XML FORMATTERS ====================

// ToS3ListObjectsV2XML converts a Bifrost file list response to S3 ListObjectsV2 XML format.
func ToS3ListObjectsV2XML(resp *schemas.BifrostFileListResponse, bucket, prefix string, maxKeys int) []byte {
	if resp == nil {
		return []byte(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"></ListBucketResult>`)
	}

	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	sb.WriteString(`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
	sb.WriteString(fmt.Sprintf("<Name>%s</Name>", bucket))
	sb.WriteString(fmt.Sprintf("<Prefix>%s</Prefix>", prefix))
	sb.WriteString(fmt.Sprintf("<KeyCount>%d</KeyCount>", len(resp.Data)))
	sb.WriteString(fmt.Sprintf("<MaxKeys>%d</MaxKeys>", maxKeys))
	if resp.HasMore {
		sb.WriteString("<IsTruncated>true</IsTruncated>")
		if resp.After != nil && *resp.After != "" {
			sb.WriteString(fmt.Sprintf("<NextContinuationToken>%s</NextContinuationToken>", *resp.After))
		}
	} else {
		sb.WriteString("<IsTruncated>false</IsTruncated>")
	}

	for _, f := range resp.Data {
		// Extract key from S3 URI
		_, key := parseS3URI(f.ID)
		sb.WriteString("<Contents>")
		sb.WriteString(fmt.Sprintf("<Key>%s</Key>", key))
		sb.WriteString(fmt.Sprintf("<Size>%d</Size>", f.Bytes))
		if f.CreatedAt > 0 {
			sb.WriteString(fmt.Sprintf("<LastModified>%s</LastModified>", time.Unix(f.CreatedAt, 0).UTC().Format(time.RFC3339)))
		}
		sb.WriteString("<StorageClass>STANDARD</StorageClass>")
		sb.WriteString("</Contents>")
	}

	sb.WriteString("</ListBucketResult>")
	return []byte(sb.String())
}

// ToS3ErrorXML converts an error to S3 error XML format.
func ToS3ErrorXML(code, message, resource, requestID string) []byte {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	sb.WriteString("<Error>")
	sb.WriteString(fmt.Sprintf("<Code>%s</Code>", code))
	sb.WriteString(fmt.Sprintf("<Message>%s</Message>", message))
	sb.WriteString(fmt.Sprintf("<Resource>%s</Resource>", resource))
	sb.WriteString(fmt.Sprintf("<RequestId>%s</RequestId>", requestID))
	sb.WriteString("</Error>")
	return []byte(sb.String())
}
