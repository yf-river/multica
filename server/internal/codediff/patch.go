package codediff

import (
	"fmt"
	"strings"
)

// linePatchLimit caps the LCS table one file may allocate. Above it the whole
// changed region is emitted as one replacement hunk: a truthful, bounded patch
// beats either an OOM or a silently missing file.
const linePatchLimit = 4_000_000

const patchContext = 3

type lineKind byte

const (
	lineEqual  lineKind = ' '
	lineDelete lineKind = '-'
	lineInsert lineKind = '+'
)

type lineOp struct {
	kind lineKind
	text string
}

// splitLines keeps the trailing newline out of the line text so a file without a
// final newline compares equal to the same content with one.
func splitLines(content string) []string {
	if content == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(content, "\n"), "\n")
}

func diffLines(before, after []string) []lineOp {
	prefix := 0
	for prefix < len(before) && prefix < len(after) && before[prefix] == after[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(before)-prefix && suffix < len(after)-prefix &&
		before[len(before)-1-suffix] == after[len(after)-1-suffix] {
		suffix++
	}
	ops := make([]lineOp, 0, len(before)+len(after))
	for _, line := range before[:prefix] {
		ops = append(ops, lineOp{lineEqual, line})
	}
	ops = append(ops, diffMiddle(before[prefix:len(before)-suffix], after[prefix:len(after)-suffix])...)
	for _, line := range before[len(before)-suffix:] {
		ops = append(ops, lineOp{lineEqual, line})
	}
	return ops
}

func diffMiddle(a, b []string) []lineOp {
	ops := make([]lineOp, 0, len(a)+len(b))
	if len(a) == 0 || len(b) == 0 || len(a)*len(b) > linePatchLimit {
		for _, line := range a {
			ops = append(ops, lineOp{lineDelete, line})
		}
		for _, line := range b {
			ops = append(ops, lineOp{lineInsert, line})
		}
		return ops
	}
	width := len(b) + 1
	table := make([]int32, (len(a)+1)*width)
	at := func(i, j int) *int32 { return &table[i*width+j] }
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				*at(i, j) = *at(i+1, j+1) + 1
			} else if *at(i+1, j) >= *at(i, j+1) {
				*at(i, j) = *at(i+1, j)
			} else {
				*at(i, j) = *at(i, j+1)
			}
		}
	}
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			ops = append(ops, lineOp{lineEqual, a[i]})
			i++
			j++
		case *at(i+1, j) >= *at(i, j+1):
			ops = append(ops, lineOp{lineDelete, a[i]})
			i++
		default:
			ops = append(ops, lineOp{lineInsert, b[j]})
			j++
		}
	}
	for ; i < len(a); i++ {
		ops = append(ops, lineOp{lineDelete, a[i]})
	}
	for ; j < len(b); j++ {
		ops = append(ops, lineOp{lineInsert, b[j]})
	}
	return ops
}

// unifiedPatch renders ops as a unified diff with three lines of context. The
// headers use the same a/ b/ prefixes as `git diff`, so a patch stays readable
// when copied out of the card.
func unifiedPatch(path string, ops []lineOp) string {
	var changes []int
	for index, op := range ops {
		if op.kind != lineEqual {
			changes = append(changes, index)
		}
	}
	if len(changes) == 0 {
		return ""
	}
	var out strings.Builder
	fmt.Fprintf(&out, "--- a/%s\n+++ b/%s\n", path, path)
	// Line numbers before each op position, so a hunk header can name the slice
	// it covers without re-walking the diff.
	beforeAt := make([]int, len(ops)+1)
	afterAt := make([]int, len(ops)+1)
	beforeLine, afterLine := 1, 1
	for index, op := range ops {
		beforeAt[index] = beforeLine
		afterAt[index] = afterLine
		switch op.kind {
		case lineEqual:
			beforeLine++
			afterLine++
		case lineDelete:
			beforeLine++
		case lineInsert:
			afterLine++
		}
	}
	beforeAt[len(ops)] = beforeLine
	afterAt[len(ops)] = afterLine
	for index := 0; index < len(changes); {
		first := changes[index]
		last := first
		next := index + 1
		// Merge neighbouring changes when their context windows would overlap.
		for next < len(changes) && changes[next]-last <= 2*patchContext {
			last = changes[next]
			next++
		}
		start := max(0, first-patchContext)
		end := min(len(ops), last+patchContext+1)
		removed, added := 0, 0
		for _, op := range ops[start:end] {
			switch op.kind {
			case lineEqual:
				removed++
				added++
			case lineDelete:
				removed++
			case lineInsert:
				added++
			}
		}
		from, to := beforeAt[start], afterAt[start]
		if removed == 0 {
			from--
		}
		if added == 0 {
			to--
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", from, removed, to, added)
		for _, op := range ops[start:end] {
			out.WriteByte(byte(op.kind))
			out.WriteString(op.text)
			out.WriteByte('\n')
		}
		index = next
	}
	return out.String()
}

// buildPatch compares two text bodies and returns the unified patch together
// with the added and deleted line counts of the whole file. The counts describe
// the full change even when the caller truncates the emitted patch.
func buildPatch(path, before, after string) (patch string, added, deleted int) {
	ops := diffLines(splitLines(before), splitLines(after))
	for _, op := range ops {
		switch op.kind {
		case lineInsert:
			added++
		case lineDelete:
			deleted++
		}
	}
	return unifiedPatch(path, ops), added, deleted
}

// CountLineChanges reports how many lines a text edit added and removed. The
// server records platform-text edits (a skill body, an agent prompt) that never
// pass through a work tree, and the card needs the same numbers as a file.
func CountLineChanges(before, after string) (added, deleted int) {
	_, added, deleted = buildPatch("", before, after)
	return added, deleted
}

// ContainsSecret reports whether a text body carries a credential. The body is
// recorded as counts only in that case, never as readable text.
func ContainsSecret(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		if _, hit := RedactLine(line); hit {
			return true
		}
		if IsSecretPath(line) {
			return true
		}
	}
	return false
}
