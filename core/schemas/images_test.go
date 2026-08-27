package schemas

import "testing"

// An image source arrives either as the object form or as a bare string, so images: ["https://..."]
// reads the way input_images does on /v1/images/generations.
func TestImageInputUnmarshalJSON(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantURL string
		wantImg string
		wantErr bool
	}{
		{name: "bare string url", body: `"https://example.com/teapot.jpg"`, wantURL: "https://example.com/teapot.jpg"},
		{name: "bare string data uri", body: `"data:image/png;base64,aGVsbG8="`, wantURL: "data:image/png;base64,aGVsbG8="},
		{name: "object url", body: `{"url":"https://example.com/teapot.jpg"}`, wantURL: "https://example.com/teapot.jpg"},
		{name: "object base64 bytes", body: `{"image":"aGVsbG8="}`, wantImg: "hello"},
		{name: "null clears both arms", body: `null`},
		{name: "number is neither form", body: `42`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got ImageInput
			err := Unmarshal([]byte(tc.body), &got)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.URL != tc.wantURL {
				t.Fatalf("url = %q, want %q", got.URL, tc.wantURL)
			}
			if string(got.Image) != tc.wantImg {
				t.Fatalf("image = %q, want %q", got.Image, tc.wantImg)
			}
		})
	}
}

// The two forms mix freely inside one images array, and decoding into a reused value must not
// leave the other arm populated.
func TestImageEditInputUnmarshalMixedImageForms(t *testing.T) {
	var input ImageEditInput
	if err := Unmarshal([]byte(
		`{"prompt":"blue","images":["https://example.com/a.jpg",{"image":"aGVsbG8="},{"url":"https://example.com/b.jpg"}]}`,
	), &input); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(input.Images) != 3 {
		t.Fatalf("images = %+v, want 3 entries", input.Images)
	}
	if input.Images[0].URL != "https://example.com/a.jpg" || input.Images[0].Image != nil {
		t.Fatalf("string form = %+v", input.Images[0])
	}
	if string(input.Images[1].Image) != "hello" || input.Images[1].URL != "" {
		t.Fatalf("bytes form = %+v", input.Images[1])
	}
	if input.Images[2].URL != "https://example.com/b.jpg" {
		t.Fatalf("object url form = %+v", input.Images[2])
	}

	// A second decode into the same value replaces both arms rather than merging them.
	reused := &input.Images[1]
	if err := Unmarshal([]byte(`"https://example.com/c.jpg"`), reused); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reused.URL != "https://example.com/c.jpg" || reused.Image != nil {
		t.Fatalf("reused decode = %+v, want only the url arm set", reused)
	}
}

// Marshalling stays on the object form; the string form is an input convenience only, so logged
// and replayed requests keep one shape.
func TestImageInputMarshalsObjectForm(t *testing.T) {
	out, err := Marshal(ImageInput{URL: "https://example.com/teapot.jpg"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != `{"image":null,"url":"https://example.com/teapot.jpg"}` {
		t.Fatalf("marshalled = %s", out)
	}
}
