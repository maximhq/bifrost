package openapimcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelectBodyContent(t *testing.T) {
	obj := map[string]any{"type": "object"}
	for _, tc := range []struct {
		name     string
		content  map[string]any
		wantCT   string
		wantOK   bool
		wantWord string
	}{
		{"json preferred over form", map[string]any{"application/x-www-form-urlencoded": map[string]any{"schema": obj}, "application/json": map[string]any{"schema": obj}}, ContentTypeJSON, true, ""},
		{"json with charset", map[string]any{"application/json; charset=utf-8": map[string]any{"schema": obj}}, ContentTypeJSON, true, ""},
		{"vendor json", map[string]any{"application/vnd.api+json": map[string]any{"schema": obj}}, ContentTypeJSON, true, ""},
		{"wildcard", map[string]any{"*/*": map[string]any{"schema": obj}}, ContentTypeJSON, true, ""},
		{"form only", map[string]any{"application/x-www-form-urlencoded": map[string]any{"schema": obj}}, ContentTypeForm, true, ""},
		{"text only, no schema", map[string]any{"text/plain": map[string]any{}}, ContentTypeText, true, ""},
		{"multipart only", map[string]any{"multipart/form-data": map[string]any{"schema": obj}}, "", false, "multipart"},
		{"octet stream", map[string]any{"application/octet-stream": map[string]any{}}, "", false, "binary"},
		{"image", map[string]any{"image/png": map[string]any{}}, "", false, "image/png"},
		{"streaming jsonl", map[string]any{"application/jsonl": map[string]any{"itemSchema": obj}}, "", false, "streaming"},
		{"json with itemSchema is streaming", map[string]any{"application/json": map[string]any{"itemSchema": obj}}, "", false, "streaming"},
		{"xml only", map[string]any{"application/xml": map[string]any{"schema": obj}}, "", false, "not supported"},
		{"empty", map[string]any{}, "", false, "no content"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ct, schema, ok, reason := SelectBodyContent(tc.content)
			assert.Equal(t, tc.wantOK, ok, reason)
			assert.Equal(t, tc.wantCT, ct)
			if tc.wantOK {
				assert.NotNil(t, schema)
			} else {
				assert.Contains(t, reason, tc.wantWord)
			}
		})
	}
}

func TestNormalizeSchema(t *testing.T) {
	t.Run("3.1 null type array becomes nullable", func(t *testing.T) {
		got := NormalizeSchema(map[string]any{"type": []any{"string", "null"}, "maxLength": 3})
		assert.Equal(t, map[string]any{"type": "string", "nullable": true, "maxLength": 3}, got)
	})
	t.Run("multi type becomes anyOf", func(t *testing.T) {
		got := NormalizeSchema(map[string]any{"type": []any{"string", "integer", "null"}})
		assert.Equal(t, true, got["nullable"])
		assert.Nil(t, got["type"])
		assert.Equal(t, []any{map[string]any{"type": "string"}, map[string]any{"type": "integer"}}, got["anyOf"])
	})
	t.Run("bare null type", func(t *testing.T) {
		got := NormalizeSchema(map[string]any{"type": "null"})
		assert.Equal(t, map[string]any{"nullable": true}, got)
	})
	t.Run("unknown keywords stripped", func(t *testing.T) {
		got := NormalizeSchema(map[string]any{"type": "object", "xml": map[string]any{"name": "pet"}, "externalDocs": "x", "discriminator": map[string]any{"propertyName": "k"}, "example": 1, "x-vendor": true, "deprecated": true, "readOnly": true, "description": "kept", "title": "Pet"})
		assert.Equal(t, map[string]any{"type": "object", "description": "kept", "title": "Pet"}, got)
	})
	t.Run("boolean exclusive bounds dropped, numeric kept", func(t *testing.T) {
		got := NormalizeSchema(map[string]any{"type": "number", "minimum": 1, "exclusiveMinimum": true})
		assert.Equal(t, map[string]any{"type": "number", "minimum": 1}, got)
		got = NormalizeSchema(map[string]any{"type": "number", "exclusiveMinimum": 0})
		assert.Equal(t, 0, got["exclusiveMinimum"])
	})
	t.Run("readOnly properties dropped and required pruned", func(t *testing.T) {
		got := NormalizeSchema(map[string]any{
			"properties": map[string]any{
				"id":   map[string]any{"type": "integer", "readOnly": true},
				"name": map[string]any{"type": "string"},
			},
			"required": []any{"id", "name"},
		})
		assert.Equal(t, "object", got["type"], "properties imply object")
		assert.Equal(t, map[string]any{"name": map[string]any{"type": "string"}}, got["properties"])
		assert.Equal(t, []any{"name"}, got["required"])
	})
	t.Run("required removed entirely when empty", func(t *testing.T) {
		got := NormalizeSchema(map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "integer", "readOnly": true}}, "required": []any{"id"}})
		_, has := got["required"]
		assert.False(t, has)
	})
	t.Run("allOf of objects merged", func(t *testing.T) {
		got := NormalizeSchema(map[string]any{"allOf": []any{
			map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}, "required": []any{"a"}},
			map[string]any{"properties": map[string]any{"b": map[string]any{"type": "integer"}}, "required": []any{"b"}, "description": "second"},
		}})
		_, hasAllOf := got["allOf"]
		assert.False(t, hasAllOf)
		assert.Equal(t, "object", got["type"])
		assert.Equal(t, []any{"a", "b"}, got["required"])
		assert.Len(t, got["properties"], 2)
	})
	t.Run("allOf with non-object member kept", func(t *testing.T) {
		got := NormalizeSchema(map[string]any{"allOf": []any{map[string]any{"type": "string"}, map[string]any{"minLength": 1}}})
		assert.Len(t, got["allOf"], 2)
	})
	t.Run("nested items and additionalProperties recurse", func(t *testing.T) {
		got := NormalizeSchema(map[string]any{
			"type":                 "array",
			"items":                map[string]any{"type": []any{"integer", "null"}, "xml": "x"},
			"additionalProperties": map[string]any{"type": "string", "example": "e"},
		})
		assert.Equal(t, map[string]any{"type": "integer", "nullable": true}, got["items"])
		assert.Equal(t, map[string]any{"type": "string"}, got["additionalProperties"])
	})
	t.Run("nil yields an object", func(t *testing.T) {
		assert.Equal(t, map[string]any{"type": "object"}, NormalizeSchema(nil))
	})
}

func decodeSchema(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	return m
}

func TestFlattenInputSchema(t *testing.T) {
	doc := parseFixture(t, "petstore-3.0.yaml")

	t.Run("params only", func(t *testing.T) {
		raw, args, body, warnings, err := FlattenInputSchema(findOp(t, doc, "GET", "/pets"))
		require.NoError(t, err)
		assert.Nil(t, body)
		assert.Empty(t, warnings)
		schema := decodeSchema(t, raw)
		props := asMap(schema["properties"])
		assert.ElementsMatch(t, []string{"limit", "tags", "X-Request-Id"}, sortedKeys(props))
		assert.Equal(t, "Page size (query parameter)", asMap(props["limit"])["description"])
		assert.Equal(t, "(header parameter)", asMap(props["X-Request-Id"])["description"])
		_, hasRequired := schema["required"]
		assert.False(t, hasRequired)
		require.Len(t, args, 3)
		assert.Equal(t, ArgBinding{ArgName: "limit", Location: InQuery, Name: "limit", Explode: true}, args[0])
		assert.False(t, args[1].Explode, "explode: false honored")
	})

	t.Run("flattened json body with readOnly dropped and required propagated", func(t *testing.T) {
		raw, args, body, _, err := FlattenInputSchema(findOp(t, doc, "POST", "/pets"))
		require.NoError(t, err)
		require.NotNil(t, body)
		assert.Equal(t, "flattened", body.Mode)
		assert.Equal(t, ContentTypeJSON, body.ContentType)
		assert.Equal(t, []string{"name", "owner", "tag"}, body.ArgNames)
		assert.Empty(t, args)
		schema := decodeSchema(t, raw)
		props := asMap(schema["properties"])
		assert.NotContains(t, props, "id", "readOnly property is not an input")
		assert.Equal(t, []any{"name"}, schema["required"], "id was required but readOnly; body is required so name stays required")
		assert.Equal(t, true, asMap(props["tag"])["nullable"])
	})

	t.Run("param colliding with another param is suffixed on both sides", func(t *testing.T) {
		raw, args, _, _, err := FlattenInputSchema(findOp(t, doc, "GET", "/pets/{petId}"))
		require.NoError(t, err)
		props := asMap(decodeSchema(t, raw)["properties"])
		assert.ElementsMatch(t, []string{"petId__path", "petId__query"}, sortedKeys(props))
		assert.Equal(t, []any{"petId__path"}, decodeSchema(t, raw)["required"])
		require.Len(t, args, 2)
		assert.Equal(t, "petId", args[0].Name)
		assert.Equal(t, InPath, args[0].Location)
		assert.True(t, args[0].Required)
	})

	t.Run("text body is a single string", func(t *testing.T) {
		raw, _, body, _, err := FlattenInputSchema(findOp(t, doc, "PUT", "/notes"))
		require.NoError(t, err)
		assert.Equal(t, "single", body.Mode)
		assert.Equal(t, "body", body.SingleArg)
		schema := decodeSchema(t, raw)
		assert.Equal(t, "string", asMap(asMap(schema["properties"])["body"])["type"])
		assert.Equal(t, []any{"body"}, schema["required"])
	})

	t.Run("body property colliding with a param keeps the plain name", func(t *testing.T) {
		op := Operation{Method: "POST", Path: "/x",
			Params: []Parameter{{Name: "name", In: InQuery, Schema: map[string]any{"type": "string"}}},
			Body:   &RequestBody{ContentType: ContentTypeJSON, Required: true, Schema: map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}}},
		}
		raw, args, body, _, err := FlattenInputSchema(op)
		require.NoError(t, err)
		props := asMap(decodeSchema(t, raw)["properties"])
		assert.ElementsMatch(t, []string{"name", "name__query"}, sortedKeys(props))
		assert.Equal(t, []string{"name"}, body.ArgNames)
		assert.Equal(t, "name__query", args[0].ArgName)
	})

	t.Run("array and dictionary bodies are a single argument", func(t *testing.T) {
		for _, schema := range []map[string]any{
			{"type": "array", "items": map[string]any{"type": "string"}},
			{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
			{"oneOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "integer"}}},
		} {
			op := Operation{Method: "POST", Path: "/x", Body: &RequestBody{ContentType: ContentTypeJSON, Schema: schema}}
			raw, _, body, _, err := FlattenInputSchema(op)
			require.NoError(t, err)
			assert.Equal(t, "single", body.Mode)
			s := decodeSchema(t, raw)
			assert.Contains(t, asMap(s["properties"]), "body")
			_, hasRequired := s["required"]
			assert.False(t, hasRequired, "optional body is not required")
		}
	})

	t.Run("optional body properties are never required", func(t *testing.T) {
		op := Operation{Method: "POST", Path: "/x", Body: &RequestBody{ContentType: ContentTypeJSON, Required: false, Schema: map[string]any{"type": "object", "required": []any{"a"}, "properties": map[string]any{"a": map[string]any{"type": "string"}}}}}
		raw, _, _, _, err := FlattenInputSchema(op)
		require.NoError(t, err)
		_, hasRequired := decodeSchema(t, raw)["required"]
		assert.False(t, hasRequired)
	})

	t.Run("3.2 querystring object flattens into query args", func(t *testing.T) {
		doc32 := parseFixture(t, "petstore-3.2.yaml")
		raw, args, _, _, err := FlattenInputSchema(findOp(t, doc32, "GET", "/search"))
		require.NoError(t, err)
		schema := decodeSchema(t, raw)
		assert.ElementsMatch(t, []string{"page", "q"}, sortedKeys(asMap(schema["properties"])))
		assert.Equal(t, []any{"q"}, schema["required"])
		require.Len(t, args, 2)
		assert.Equal(t, InQuery, args[0].Location)
	})

	t.Run("3.2 querystring string is sent verbatim", func(t *testing.T) {
		op := Operation{Method: "GET", Path: "/x", Params: []Parameter{{Name: "raw", In: InQuerystring, Required: true, Schema: map[string]any{"type": "string"}}}}
		raw, args, _, _, err := FlattenInputSchema(op)
		require.NoError(t, err)
		assert.Equal(t, []any{"raw"}, decodeSchema(t, raw)["required"])
		assert.Equal(t, InQuerystring, args[0].Location)
	})

	t.Run("unknown location skipped with warning", func(t *testing.T) {
		op := Operation{Method: "GET", Path: "/x", Params: []Parameter{{Name: "weird", In: "matrix", Schema: map[string]any{"type": "string"}}}}
		_, args, _, warnings, err := FlattenInputSchema(op)
		require.NoError(t, err)
		assert.Empty(t, args)
		require.Len(t, warnings, 1)
	})
}

func TestSynthesize_Fixtures(t *testing.T) {
	t.Run("3.0", func(t *testing.T) {
		syn, err := Synthesize(parseFixture(t, "petstore-3.0.yaml"), SynthesizeOptions{ClientName: "petstore"})
		require.NoError(t, err)
		assert.Equal(t, []string{"putNote", "listPets", "createPet", "getPetById", "get_search"}, syn.ToolNames())
		var reasons []string
		for _, u := range syn.Unsupported {
			reasons = append(reasons, u.OperationID+": "+u.Reason)
		}
		require.Len(t, reasons, 2)
		assert.Contains(t, reasons[0], "deletePet: deprecated")
		assert.Contains(t, reasons[1], "uploadPetPhoto: multipart")

		get := findTool(t, syn, "getPetById")
		assert.True(t, *get.Annotations.ReadOnlyHint)
		assert.Empty(t, get.Warnings, "anonymous (security: []) operations have nothing to warn about")

		withDeprecated, err := Synthesize(parseFixture(t, "petstore-3.0.yaml"), SynthesizeOptions{ClientName: "petstore", IncludeDeprecated: true})
		require.NoError(t, err)
		assert.Contains(t, withDeprecated.ToolNames(), "deletePet")
		assert.Contains(t, findTool(t, withDeprecated, "deletePet").Description, "[deprecated]")
	})
	t.Run("swagger 2", func(t *testing.T) {
		syn, err := Synthesize(parseFixture(t, "petstore-swagger2.json"), SynthesizeOptions{ClientName: "petstore"})
		require.NoError(t, err)
		assert.Equal(t, []string{"login", "listPets", "createPet", "getPetById"}, syn.ToolNames())
		login := findTool(t, syn, "login")
		assert.Equal(t, ContentTypeForm, login.Body.ContentType)
		assert.Equal(t, "flattened", login.Body.Mode)
	})
	t.Run("3.1", func(t *testing.T) {
		syn, err := Synthesize(parseFixture(t, "petstore-3.1.json"), SynthesizeOptions{ClientName: "petstore"})
		require.NoError(t, err)
		create := findTool(t, syn, "createPet")
		props := asMap(decodeSchema(t, create.InputSchema)["properties"])
		assert.Equal(t, map[string]any{"type": "string", "nullable": true, "maxLength": float64(20)}, props["tag"])
		assert.Equal(t, []any{map[string]any{"type": "string"}, map[string]any{"type": "integer"}}, asMap(props["status"])["anyOf"])
		assert.Equal(t, float64(0), asMap(props["weight"])["exclusiveMinimum"])
	})
	t.Run("3.2", func(t *testing.T) {
		syn, err := Synthesize(parseFixture(t, "petstore-3.2.yaml"), SynthesizeOptions{ClientName: "petstore"})
		require.NoError(t, err)
		assert.Equal(t, []string{"listPets", "queryPets", "purgePets", "searchPets"}, syn.ToolNames())
		assert.True(t, *findTool(t, syn, "queryPets").Annotations.ReadOnlyHint)
		assert.True(t, *findTool(t, syn, "purgePets").Annotations.DestructiveHint)
		require.Len(t, syn.Unsupported, 1)
		assert.Contains(t, syn.Unsupported[0].Reason, "streaming")
	})
}

func TestSynthesize_WarningsAndLimits(t *testing.T) {
	doc := &Document{
		SecuritySchemes: map[string]SecurityScheme{"OAuth": {Name: "OAuth", Type: "oauth2"}},
		GlobalSecurity:  []SecurityRequirement{{"OAuth": {}}},
	}
	for i := 0; i < ToolCountWarnThreshold+1; i++ {
		doc.Operations = append(doc.Operations, Operation{ID: "op", Method: "GET", Path: "/p" + string(rune('a'+i%26)) + string(rune('a'+i/26))})
	}
	syn, err := Synthesize(doc, SynthesizeOptions{ClientName: "c"})
	require.NoError(t, err)
	assert.Len(t, syn.Tools, ToolCountWarnThreshold+1)
	joined := ""
	for _, w := range syn.Warnings {
		joined += w + "\n"
	}
	assert.Contains(t, joined, "exceed the recommended")
	assert.Contains(t, joined, "renamed")
	assert.Contains(t, syn.Tools[0].Warnings[0], "OAuth (oauth2)")

	huge := &Document{}
	for i := 0; i <= MaxToolCount; i++ {
		huge.Operations = append(huge.Operations, Operation{Method: "GET", Path: "/x"})
	}
	_, err = Synthesize(huge, SynthesizeOptions{})
	require.ErrorContains(t, err, "above the limit")

	_, err = Synthesize(nil, SynthesizeOptions{})
	require.Error(t, err)
}
