//go:build ignore

// Command genunsegmentedscripts writes unsegmentedscripts.go, the Unicode
// range table of letters belonging to scripts that do not separate words with
// spaces. The header of the generated file documents the derivation.
//
// Usage:
//
//	go run genunsegmentedscripts.go [-source URL_OR_PATH] [-out FILE]
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// defaultSource is the canonical location of the Line_Break property file in
// the Unicode Character Database.
const defaultSource = "https://www.unicode.org/Public/UCD/latest/ucd/LineBreak.txt"

// unsegmentedLineBreakClasses are the Line_Break classes UAX #29 §4 identifies
// as marking scripts whose word boundaries are not determined by spaces.
var unsegmentedLineBreakClasses = map[string]bool{
	"ID": true, // East Asian ideographs and kana
	"SA": true, // South East Asian: no demarcation of words
	"AK": true, // Aksara (Brahmic)
	"AS": true, // Aksara start (Brahmic)
	"CJ": true, // Conditional Japanese Starter; UAX #14 resolves CJ to ID in non-strict line breaking
	"NS": true, // Nonstarter; the CJK/kana iteration marks 々 ゝ ゞ ヽ ヾ 〻
}

// entryPattern matches a single Line_Break data line, for example
// "4E00..9FFF;ID" or "3005;ID", ignoring the trailing "# ..." comment.
var entryPattern = regexp.MustCompile(`^\s*([0-9A-Fa-f]{4,6})(?:\.\.([0-9A-Fa-f]{4,6}))?\s*;\s*([A-Za-z0-9]+)`)

// codepointRange is a closed, contiguous run of code points.
type codepointRange struct {
	lo rune
	hi rune
}

func main() {
	source := flag.String("source", defaultSource, "URL or local path of LineBreak.txt")
	out := flag.String("out", "unsegmentedscripts.go", "path of the Go file to write")
	flag.Parse()

	data, err := readSource(*source)
	if err != nil {
		log.Fatalf("read %s: %v", *source, err)
	}

	fileName, fileDate := parseHeader(data)
	if fileName == "" || fileDate == "" {
		log.Fatalf("read %s: could not find the UCD file name and date in the header", *source)
	}

	ranges, total, err := collectRanges(data)
	if err != nil {
		log.Fatalf("parse %s: %v", *source, err)
	}
	if err := checkPlausible(ranges, total); err != nil {
		log.Fatalf("parse %s: %v", *source, err)
	}

	rendered := render(ranges, total, fileName, fileDate, *source)
	if err := os.WriteFile(*out, []byte(rendered), 0o644); err != nil {
		log.Fatalf("write %s: %v", *out, err)
	}
	fmt.Printf("wrote %s: %d code points in %d ranges, from %s (%s)\n", *out, total, len(ranges), fileName, fileDate)
}

// readSource fetches the property file over HTTP, or reads it from disk when
// source is not a URL.
func readSource(source string) (string, error) {
	if !strings.HasPrefix(source, "http://") && !strings.HasPrefix(source, "https://") {
		data, err := os.ReadFile(source)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}

	resp, err := http.Get(source)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %s", resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// parseHeader extracts the versioned file name and the revision date that the
// UCD writes as the first two comment lines, so the generated file can record
// exactly which revision it was built from.
func parseHeader(data string) (fileName, fileDate string) {
	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "#") {
			break
		}
		trimmed := strings.TrimSpace(strings.TrimPrefix(line, "#"))
		switch {
		case strings.HasPrefix(trimmed, "LineBreak-") && fileName == "":
			fileName = trimmed
		case strings.HasPrefix(trimmed, "Date:") && fileDate == "":
			fileDate = strings.TrimSpace(strings.TrimPrefix(trimmed, "Date:"))
		}
		if fileName != "" && fileDate != "" {
			return fileName, fileDate
		}
	}
	return fileName, fileDate
}

// collectRanges returns the merged, sorted runs of code points that both carry
// an unsegmented Line_Break class and are letters, along with the total number
// of code points those runs cover.
func collectRanges(data string) ([]codepointRange, int, error) {
	var ranges []codepointRange
	total := 0
	appendCodepoint := func(r rune) {
		total++
		if n := len(ranges); n > 0 && ranges[n-1].hi == r-1 {
			ranges[n-1].hi = r
			return
		}
		ranges = append(ranges, codepointRange{lo: r, hi: r})
	}

	var codepoints []rune
	scanner := bufio.NewScanner(strings.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if idx := strings.IndexByte(line, '#'); idx >= 0 {
			line = line[:idx]
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		match := entryPattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		if !unsegmentedLineBreakClasses[strings.ToUpper(match[3])] {
			continue
		}
		lo, err := strconv.ParseUint(match[1], 16, 32)
		if err != nil {
			return nil, 0, fmt.Errorf("parse code point %q: %w", match[1], err)
		}
		hi := lo
		if match[2] != "" {
			if hi, err = strconv.ParseUint(match[2], 16, 32); err != nil {
				return nil, 0, fmt.Errorf("parse code point %q: %w", match[2], err)
			}
		}
		for r := rune(lo); r <= rune(hi); r++ {
			// Class ID also covers CJK punctuation, symbols and emoji. Only
			// letters are relevant to word boundaries.
			if unicode.IsLetter(r) {
				codepoints = append(codepoints, r)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, err
	}

	// The UCD lists ranges in ascending order, but sort anyway so the merge
	// below does not depend on that.
	sort.Slice(codepoints, func(i, j int) bool { return codepoints[i] < codepoints[j] })
	for _, r := range codepoints {
		appendCodepoint(r)
	}
	return ranges, total, nil
}

// minCodepoints is a floor below which the result is treated as a parse
// failure. A renamed or split Line_Break class would otherwise yield a smaller
// table and still report success.
const minCodepoints = 100000

// Spot checks: an ideograph, hiragana, the prolonged sound mark, the CJK
// iteration mark, Thai, a Balinese AK-class letter and a Batak AS-class
// letter must be present; Latin and Cyrillic must not.
var (
	requiredCodepoints  = []rune{0x4E00, 0x3042, 0x30FC, 0x3005, 0x0E01, 0x1B05, 0x1BC0}
	forbiddenCodepoints = []rune{0x0041, 0x0410}
)

// checkPlausible rejects a table that no longer looks like the one the package
// expects, rather than letting a silently shrunken table be written out.
func checkPlausible(ranges []codepointRange, total int) error {
	if total < minCodepoints {
		return fmt.Errorf("only %d code points matched, expected at least %d; the file format or the class names may have changed", total, minCodepoints)
	}
	for _, r := range requiredCodepoints {
		if !containsCodepoint(ranges, r) {
			return fmt.Errorf("U+%04X is missing from the table", r)
		}
	}
	for _, r := range forbiddenCodepoints {
		if containsCodepoint(ranges, r) {
			return fmt.Errorf("U+%04X must not be in the table", r)
		}
	}
	return nil
}

func containsCodepoint(ranges []codepointRange, r rune) bool {
	for _, rng := range ranges {
		if r >= rng.lo && r <= rng.hi {
			return true
		}
	}
	return false
}

// render produces the contents of the generated Go file.
func render(ranges []codepointRange, total int, fileName, fileDate, source string) string {
	var buf strings.Builder
	fmt.Fprintf(&buf, `// Code generated by genunsegmentedscripts.go. DO NOT EDIT.
//
// Source: %s
// UCD file: %s (%s)
//
// Contents: the %d code points, in %d ranges, that are letters and whose
// Line_Break class is ID, SA, AK, AS, CJ or NS — the classes UAX #29 §4 ties to
// the scripts whose word boundaries are not determined by spaces. Intersecting
// with Letter drops the punctuation, symbols and emoji those classes also carry,
// which already act as boundaries. Class ID also covers the fullwidth Latin
// letters, which occur only inside East Asian text and are therefore treated the
// same way; the fullwidth digits are not letters and are excluded. Hangul
// appears only where the UCD itself assigns class ID, namely the compatibility
// and halfwidth jamo; the rest of Hangul is a separate term in
// isUnsegmentedLetter.
//
// Regenerate with: cd plugins/routing && go generate ./complexity/

package complexity

import "unicode"

// unsegmentedScriptLetters holds letters from scripts that do not use spaces
// to separate words.
var unsegmentedScriptLetters = &unicode.RangeTable{
`, source, fileName, fileDate, total, len(ranges))

	var r16, r32 []codepointRange
	for _, rng := range ranges {
		if rng.hi <= 0xFFFF {
			r16 = append(r16, rng)
			continue
		}
		r32 = append(r32, rng)
	}

	if len(r16) > 0 {
		buf.WriteString("\tR16: []unicode.Range16{\n")
		for _, rng := range r16 {
			fmt.Fprintf(&buf, "\t\t{0x%04X, 0x%04X, 1},\n", rng.lo, rng.hi)
		}
		buf.WriteString("\t},\n")
	}
	if len(r32) > 0 {
		buf.WriteString("\tR32: []unicode.Range32{\n")
		for _, rng := range r32 {
			fmt.Fprintf(&buf, "\t\t{0x%06X, 0x%06X, 1},\n", rng.lo, rng.hi)
		}
		buf.WriteString("\t},\n")
	}
	buf.WriteString("}\n")
	return buf.String()
}
