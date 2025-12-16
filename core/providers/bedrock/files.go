package bedrock

import (
	"fmt"
	"html"
	"net/url"
	"strings"
	"time"

	"github.com/bytedance/sonic"
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
