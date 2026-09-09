//
//  Copyright 2025 Google LLC
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.
//

package bedrock

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// TestConvertMessages_UniqueDocumentNames covers #7003: the Converse API
// rejects duplicate document names, so two untitled documents in one request
// must not both be named "document".
func TestConvertMessages_UniqueDocumentNames(t *testing.T) {
	docBlock := func() schemas.ChatContentBlock {
		return schemas.ChatContentBlock{
			Type: schemas.ChatContentBlockTypeFile,
			File: &schemas.ChatInputFile{},
		}
	}
	msg := schemas.ChatMessage{
		Role: schemas.ChatMessageRoleUser,
		Content: &schemas.ChatMessageContent{
			ContentBlocks: []schemas.ChatContentBlock{docBlock(), docBlock()},
		},
	}

	bedrockMsgs, _, err := convertMessages(t.Context(), "anthropic.claude-sonnet-5", []schemas.ChatMessage{msg})
	if err != nil {
		t.Fatalf("convertMessages: %v", err)
	}
	if len(bedrockMsgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(bedrockMsgs))
	}

	seen := map[string]bool{}
	for _, content := range bedrockMsgs[0].Content {
		if content.Document == nil {
			continue
		}
		name := content.Document.Name
		if seen[name] {
			t.Fatalf("duplicate document name %q; names must be unique per request", name)
		}
		seen[name] = true
	}
	if len(seen) < 2 {
		t.Fatalf("expected at least 2 document blocks, saw %d unique names: %v", len(seen), seen)
	}
}

// TestConvertMessages_TitledDocumentsKeepTheirNames: explicit filenames are
// kept verbatim (normalized) and never receive a numeric suffix unless they
// actually collide.
func TestConvertMessages_TitledDocumentsKeepTheirNames(t *testing.T) {
	nameA := "report_pdf" // normalization maps '.' to '_' (existing behavior)
	nameB := "notes_pdf"
	mkBlock := func(n string) schemas.ChatContentBlock {
		return schemas.ChatContentBlock{
			Type: schemas.ChatContentBlockTypeFile,
			File: &schemas.ChatInputFile{Filename: &n},
		}
	}
	msg := schemas.ChatMessage{
		Role: schemas.ChatMessageRoleUser,
		Content: &schemas.ChatMessageContent{
			ContentBlocks: []schemas.ChatContentBlock{mkBlock(nameA), mkBlock(nameB)},
		},
	}

	bedrockMsgs, _, err := convertMessages(t.Context(), "anthropic.claude-sonnet-5", []schemas.ChatMessage{msg})
	if err != nil {
		t.Fatalf("convertMessages: %v", err)
	}

	var names []string
	for _, content := range bedrockMsgs[0].Content {
		if content.Document != nil {
			names = append(names, content.Document.Name)
		}
	}
	if len(names) != 2 || names[0] != nameA || names[1] != nameB {
		t.Fatalf("titled documents renamed: %v", names)
	}
}
