// Command npmartifact compares every published launcher file to reviewed source.
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// fileHash requires ordinary source files and hashes them in bounded memory.
func fileHash(path string) ([32]byte, int64, error) {
	var sum [32]byte
	info, err := os.Lstat(path)
	if err != nil {
		return sum, 0, err
	}
	if !info.Mode().IsRegular() {
		return sum, 0, fmt.Errorf("not a regular source file: %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return sum, 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	copy(sum[:], h.Sum(nil))
	return sum, n, err
}

// verify rejects injection, altered bytes, links, duplicate members, and omissions.
func verify(archive, source string) error {
	data, err := os.ReadFile(filepath.Join(source, "package.json"))
	if err != nil {
		return err
	}
	var pkg struct {
		Files                []string       `json:"files"`
		Dependencies         map[string]any `json:"dependencies"`
		DevDependencies      map[string]any `json:"devDependencies"`
		OptionalDependencies map[string]any `json:"optionalDependencies"`
	}
	if err = json.Unmarshal(data, &pkg); err != nil {
		return err
	}
	if len(pkg.Dependencies)+len(pkg.DevDependencies)+len(pkg.OptionalDependencies) > 0 {
		return fmt.Errorf("launcher dependencies require a reviewed build process")
	}
	if len(pkg.Files) == 0 {
		return fmt.Errorf("package files must be explicit")
	}
	expected := map[string]string{"package/package.json": filepath.Join(source, "package.json")}
	for _, name := range pkg.Files {
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") || filepath.Base(name) != name {
			return fmt.Errorf("invalid top-level filename: %q", name)
		}
		if _, duplicate := expected["package/"+name]; duplicate {
			return fmt.Errorf("duplicate source filename: %s", name)
		}
		expected["package/"+name] = filepath.Join(source, name)
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	zipped, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer zipped.Close()
	stream := tar.NewReader(zipped)
	seen := map[string]bool{}
	for {
		member, err := stream.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		sourceFile, ok := expected[member.Name]
		if !ok || seen[member.Name] || (member.Typeflag != tar.TypeReg && member.Typeflag != tar.TypeRegA) {
			return fmt.Errorf("unexpected or duplicate npm archive member: %s", member.Name)
		}
		want, size, err := fileHash(sourceFile)
		if err != nil {
			return err
		}
		if size != member.Size {
			return fmt.Errorf("npm archive size mismatch: %s", member.Name)
		}
		h := sha256.New()
		n, err := io.Copy(h, stream)
		if err != nil {
			return err
		}
		if n != size || string(h.Sum(nil)) != string(want[:]) {
			return fmt.Errorf("npm source mismatch: %s", member.Name)
		}
		seen[member.Name] = true
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("npm archive is missing approved files")
	}
	// Finish gzip validation: tar EOF alone does not validate its checksum trailer.
	tail, err := io.ReadAll(io.LimitReader(zipped, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(tail) > 1<<20 {
		return fmt.Errorf("excessive trailing archive padding")
	}
	for _, value := range tail {
		if value != 0 {
			return fmt.Errorf("unexpected data after npm archive")
		}
	}
	return nil
}

// main requires both artifact and approved source paths.
func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: npmartifact ARCHIVE SOURCE")
		os.Exit(2)
	}
	if err := verify(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
