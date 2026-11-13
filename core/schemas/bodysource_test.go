package schemas

import (
	"bytes"
	"io"
	"mime/multipart"
	"strings"
	"testing"
)

// TestBytesBodySource asserts the source shares the caller's bytes (no copy) and that every
// Open starts from the beginning, which is what lets a retry send the body again.
func TestBytesBodySource(t *testing.T) {
	b := []byte("hello")
	src := NewBytesBodySource(b)
	if src.Size() != 5 {
		t.Fatalf("Size() = %d, want 5", src.Size())
	}
	for i := 0; i < 2; i++ {
		got, err := ReadBodySource(src)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "hello" {
			t.Fatalf("open %d read %q, want hello", i, got)
		}
	}
	b[0] = 'j'
	got, _ := ReadBodySource(src)
	if string(got) != "jello" {
		t.Errorf("read %q after changing the caller's bytes, want jello (the source must not copy)", got)
	}
}

// TestMultipartFileBodySource asserts the source reports the part's size and reads the file,
// for a file kept in memory and for one the parser spilled to a temp file.
func TestMultipartFileBodySource(t *testing.T) {
	for _, tc := range []struct {
		name      string
		size      int
		maxMemory int64
	}{
		{"in memory", 1024, 1 << 20},
		{"temp file", 64 << 10, 1024},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := bytes.Repeat([]byte("x"), tc.size)
			var body bytes.Buffer
			w := multipart.NewWriter(&body)
			part, err := w.CreateFormFile("file", "f.jsonl")
			if err != nil {
				t.Fatal(err)
			}
			part.Write(content)
			w.Close()
			form, err := multipart.NewReader(&body, w.Boundary()).ReadForm(tc.maxMemory)
			if err != nil {
				t.Fatal(err)
			}
			defer form.RemoveAll()

			src := NewMultipartFileBodySource(form.File["file"][0])
			if src.Size() != int64(tc.size) {
				t.Fatalf("Size() = %d, want %d", src.Size(), tc.size)
			}
			got, err := ReadBodySource(src)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, content) {
				t.Errorf("read %d bytes that differ from the uploaded file", len(got))
			}
		})
	}
}

// fixedBodySource is a BodySource with a declared size that may not match its content.
type fixedBodySource struct {
	size    int64
	content string
}

func (s fixedBodySource) Size() int64 { return s.size }

func (s fixedBodySource) Open() (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(s.content)), nil
}

// TestReadBodySource asserts an unknown size reads to EOF and a source shorter than its declared
// size is an error rather than a silently truncated body.
func TestReadBodySource(t *testing.T) {
	got, err := ReadBodySource(fixedBodySource{size: -1, content: "abc"})
	if err != nil || string(got) != "abc" {
		t.Errorf("unknown size: got %q, %v; want abc, nil", got, err)
	}
	if _, err := ReadBodySource(fixedBodySource{size: 10, content: "abc"}); err == nil {
		t.Error("short source: want an error, got nil")
	}
}
