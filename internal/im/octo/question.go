package octo

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/Tencent/WeKnora/internal/im/octo/wire"
)

type mentionSpan struct {
	start, end int
	target     string
}

// questionWithoutBotMention reads only structured Octo mention spans. A UID
// without offsets is sufficient for routing, but never for changing the text.
func questionWithoutBotMention(text string, entities []wire.Entity, botUID string) (string, bool) {
	units := utf16.Encode([]rune(text))
	spans := make([]mentionSpan, 0, len(entities))
	for _, e := range entities {
		if e.Offset < 0 || e.Offset >= len(units) || e.Length < 2 || e.Length > len(units)-e.Offset {
			continue
		}
		end := e.Offset + e.Length
		if units[e.Offset] != '@' || splitsUTF16(units, end) {
			continue
		}
		label := string(utf16.Decode(units[e.Offset+1 : end]))
		if strings.TrimSpace(label) != label || strings.ContainsRune(label, '@') ||
			strings.ContainsFunc(label, unicode.IsControl) {
			continue
		}
		spans = append(spans, mentionSpan{start: e.Offset, end: end, target: e.UID})
	}
	slices.SortFunc(spans, func(a, b mentionSpan) int { return a.start - b.start })
	spans = slices.Compact(spans)
	var out []uint16
	last := 0
	for i := 0; i < len(spans); {
		span := spans[i]
		j, end := i+1, span.end
		for j < len(spans) && spans[j].start < end {
			end = max(end, spans[j].end)
			j++
		}
		// Conflicting entities may include somebody else's mention; preserve
		// the original text for the entire overlapping group.
		if j == i+1 && span.target == botUID {
			if out == nil {
				out = make([]uint16, 0, len(units))
			}
			out = append(out, units[last:span.start]...)
			last = span.end
			// Avoid doubling horizontal whitespace around an inline mention.
			if len(out) > 0 && horizontalSpace(out[len(out)-1]) {
				for last < len(units) && horizontalSpace(units[last]) {
					last++
				}
			}
		}
		i = j
	}
	if out == nil {
		return text, false
	}
	out = append(out, units[last:]...)
	return strings.TrimSpace(string(utf16.Decode(out))), true
}

func splitsUTF16(units []uint16, i int) bool {
	return i > 0 && i < len(units) && units[i-1] >= 0xD800 && units[i-1] <= 0xDBFF &&
		units[i] >= 0xDC00 && units[i] <= 0xDFFF
}

func horizontalSpace(unit uint16) bool {
	return unit == ' ' || unit == '\t' || unit == '\u00a0' || unit == '\u3000'
}
