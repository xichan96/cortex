package telemetry

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RenderTrace renders recorded spans as a chronological text forest, one line
// per span plus indented event and link lines. Spans whose parent is absent or
// remote are rendered as roots. Duplicate ids keep their first occurrence.
//
// The output is deterministic for a given set of spans: roots and siblings are
// ordered by start time and then id, and attribute keys are sorted. Control
// characters in names, keys, and status messages are escaped so the output is
// safe for terminals and logs. The renderer never follows links.
func RenderTrace(spans []RecordedSpan) string {
	unique := make([]RecordedSpan, 0, len(spans))
	seen := make(map[string]struct{}, len(spans))
	for _, span := range spans {
		if _, duplicate := seen[span.ID]; duplicate {
			continue
		}
		seen[span.ID] = struct{}{}
		unique = append(unique, span)
	}

	roots := make([]int, 0, len(unique))
	children := make(map[string][]int, len(unique))
	for index, span := range unique {
		if span.ParentID == "" {
			roots = append(roots, index)
			continue
		}
		if _, present := seen[span.ParentID]; !present {
			roots = append(roots, index)
			continue
		}
		children[span.ParentID] = append(children[span.ParentID], index)
	}
	order := func(indices []int) {
		sort.SliceStable(indices, func(left, right int) bool {
			a, b := unique[indices[left]], unique[indices[right]]
			if !a.StartTime.Equal(b.StartTime) {
				return a.StartTime.Before(b.StartTime)
			}
			return a.ID < b.ID
		})
	}
	order(roots)
	for _, indices := range children {
		order(indices)
	}

	lines := make([]string, 0, len(unique))
	type pendingSpan struct {
		index int
		depth int
	}
	stack := make([]pendingSpan, 0, len(unique))
	for index := len(roots) - 1; index >= 0; index-- {
		stack = append(stack, pendingSpan{index: roots[index]})
	}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		span := unique[current.index]
		indent := strings.Repeat("  ", current.depth)

		header := indent + escaped(span.Name)
		if span.Kind != "" {
			header += " kind=" + escaped(string(span.Kind))
		}
		header += formatAttributes(span.Attributes)
		header += " (" + formatMilliseconds(span.Duration()) + "ms)"
		if span.Status.IsError() {
			header += " error"
			if span.Status.Message != "" {
				header += " " + escaped(span.Status.Message)
			}
		}
		lines = append(lines, header)
		for _, event := range span.Events {
			lines = append(lines, indent+"  · "+escaped(event.Name)+formatAttributes(event.Attributes)+
				" (+"+formatMilliseconds(event.Offset)+"ms)")
		}
		for _, link := range span.Links {
			lines = append(lines, indent+"  → link trace="+escaped(link.Context.TraceID)+
				" span="+escaped(link.Context.SpanID)+formatAttributes(link.Attributes))
		}

		descendants := children[span.ID]
		for index := len(descendants) - 1; index >= 0; index-- {
			stack = append(stack, pendingSpan{index: descendants[index], depth: current.depth + 1})
		}
	}
	return strings.Join(lines, "\n")
}

func formatAttributes(attributes Attributes) string {
	if len(attributes) == 0 {
		return ""
	}
	keys := make([]string, 0, len(attributes))
	for key := range attributes {
		if attributes[key] == nil {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	for _, key := range keys {
		builder.WriteByte(' ')
		builder.WriteString(escaped(key))
		builder.WriteByte('=')
		builder.WriteString(formatAttributeValue(attributes[key]))
	}
	return builder.String()
}

func formatAttributeValue(value any) string {
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.String:
		return strconv.Quote(reflected.String())
	case reflect.Bool:
		return strconv.FormatBool(reflected.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(reflected.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(reflected.Uint(), 10)
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(reflected.Float(), 'g', -1, 64)
	case reflect.Slice:
		parts := make([]string, 0, reflected.Len())
		for index := 0; index < reflected.Len(); index++ {
			parts = append(parts, formatAttributeValue(reflected.Index(index).Interface()))
		}
		return "[" + strings.Join(parts, ",") + "]"
	default:
		return strconv.Quote(fmt.Sprintf("%v", value))
	}
}

func formatMilliseconds(duration time.Duration) string {
	return strconv.FormatFloat(float64(duration)/float64(time.Millisecond), 'f', -1, 64)
}

// escaped writes every C0/C1 control character and DEL as a \uXXXX escape. It
// mirrors the escaping applied to JSON string values so that ids, names, keys,
// and messages from another process never reach a terminal raw.
func escaped(text string) string {
	var builder strings.Builder
	for _, char := range text {
		if char < 0x20 || (char >= 0x7f && char <= 0x9f) {
			builder.WriteString(fmt.Sprintf("\\u%04x", char))
			continue
		}
		builder.WriteRune(char)
	}
	return builder.String()
}
