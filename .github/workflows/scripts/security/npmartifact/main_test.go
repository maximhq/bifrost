package main

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// member defines synthetic tar members without extracting their contents.
type member struct {
	name, body string
	kind       byte
}

// archiveFixture writes harmless tar fixtures including deliberately invalid members.
func archiveFixture(t *testing.T, path string, members []member) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, m := range members {
		header := &tar.Header{Name: m.name, Mode: 0644, Size: int64(len(m.body)), Typeflag: m.kind}
		if m.kind == tar.TypeSymlink {
			header.Size = 0
			header.Linkname = "outside"
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := tw.Write([]byte(m.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, close := range []func() error{tw.Close, gz.Close, f.Close} {
		if err := close(); err != nil {
			t.Fatal(err)
		}
	}
}

// TestArchiveMembershipAndBytes ensures only the exact approved package can publish.
func TestArchiveMembershipAndBytes(t *testing.T) {
	root := t.TempDir()
	pkg := `{"name":"fixture","files":["bin.js"]}`
	for name, body := range map[string]string{"package.json": pkg, "bin.js": "approved"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	approved := []member{{"package/package.json", pkg, tar.TypeReg}, {"package/bin.js", "approved", tar.TypeReg}}
	cases := [][]member{approved, append(append([]member{}, approved...), member{"package/sitecustomize.py", "injected", tar.TypeReg}), append(append([]member{}, approved...), approved[1]), {approved[0], {"package/bin.js", "modified", tar.TypeReg}}, {approved[0]}, append(append([]member{}, approved...), member{"../escape", "injected", tar.TypeReg}), {approved[0], {"package/bin.js", "", tar.TypeSymlink}}}
	for i, members := range cases {
		archive := filepath.Join(root, "package.tgz")
		archiveFixture(t, archive, members)
		err := verify(archive, root)
		if (i == 0) != (err == nil) {
			t.Fatalf("case %d: %v", i, err)
		}
	}
	archive := filepath.Join(root, "corrupt.tgz")
	archiveFixture(t, archive, approved)
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-8] ^= 0xff
	if err := os.WriteFile(archive, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := verify(archive, root); err == nil {
		t.Fatal("corrupt gzip checksum accepted")
	}
}
