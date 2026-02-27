package network

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// ParseMultipartFormFields extracts text form fields from a multipart/form-data body,
// skipping file parts to avoid loading binary data into memory.
func ParseMultipartFormFields(contentType string, body []byte) (map[string]any, error) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, err
	}
	boundary := params["boundary"]
	if boundary == "" {
		return nil, fmt.Errorf("no boundary in content-type")
	}
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	payload := make(map[string]any)
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if part.FileName() != "" {
			_ = part.Close()
			continue
		}
		name := part.FormName()
		if name != "" {
			val, readErr := io.ReadAll(part)
			if readErr != nil {
				_ = part.Close()
				return nil, readErr
			}
			payload[name] = string(val)
		}
		_ = part.Close()
	}
	return payload, nil
}

// ReconstructMultipartBody rebuilds a multipart/form-data body from the original,
// replacing text field values with those from payload (e.g. updated "model") and
// copying file parts byte-for-byte.
func ReconstructMultipartBody(origContentType string, origBody []byte, payload map[string]any) ([]byte, string, error) {
	_, params, err := mime.ParseMediaType(origContentType)
	if err != nil {
		return nil, "", err
	}
	boundary := params["boundary"]
	if boundary == "" {
		return nil, "", fmt.Errorf("no boundary in content-type")
	}
	reader := multipart.NewReader(bytes.NewReader(origBody), boundary)
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	writtenFields := make(map[string]bool)

	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", err
		}
		name := part.FormName()
		if part.FileName() != "" {
			fw, createErr := writer.CreatePart(part.Header)
			if createErr != nil {
				_ = part.Close()
				return nil, "", createErr
			}
			if _, copyErr := io.Copy(fw, part); copyErr != nil {
				_ = part.Close()
				return nil, "", copyErr
			}
		} else if name != "" {
			if val, ok := payload[name]; ok {
				if err := WriteMultipartField(writer, name, val); err != nil {
					_ = part.Close()
					return nil, "", err
				}
			} else {
				origVal, readErr := io.ReadAll(part)
				if readErr != nil {
					_ = part.Close()
					return nil, "", readErr
				}
				if err := writer.WriteField(name, string(origVal)); err != nil {
					_ = part.Close()
					return nil, "", err
				}
			}
			writtenFields[name] = true
		}
		_ = part.Close()
	}

	for key, val := range payload {
		if writtenFields[key] {
			continue
		}
		if err := WriteMultipartField(writer, key, val); err != nil {
			return nil, "", err
		}
	}

	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), writer.FormDataContentType(), nil
}

// WriteMultipartField writes a single form field to the multipart writer,
// handling string, []string, and other value types.
func WriteMultipartField(writer *multipart.Writer, name string, val any) error {
	switch v := val.(type) {
	case string:
		return writer.WriteField(name, v)
	case []string:
		encoded, err := sonic.Marshal(v)
		if err != nil {
			return err
		}
		return writer.WriteField(name, string(encoded))
	default:
		return writer.WriteField(name, fmt.Sprintf("%v", val))
	}
}

// SerializePayloadToRequest writes the modified payload back to req.Body,
// using multipart reconstruction for multipart/form-data or JSON for everything else.
func SerializePayloadToRequest(req *schemas.HTTPRequest, payload map[string]any, isMultipart bool, origContentType string) error {
	if isMultipart {
		newBody, newCT, err := ReconstructMultipartBody(origContentType, req.Body, payload)
		if err != nil {
			return err
		}
		req.Body = newBody
		for k := range req.Headers {
			if strings.EqualFold(k, "content-type") {
				delete(req.Headers, k)
			}
		}
		req.Headers["Content-Type"] = newCT
		return nil
	}
	body, err := sonic.Marshal(payload)
	if err != nil {
		return err
	}
	req.Body = body
	return nil
}
