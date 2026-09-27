package types

import "testing"

func TestIsIMFallbackAnswerOnlyMatchesSystemOwnedText(t *testing.T) {
	for _, content := range []string{IMNoAnswerFallback, IMErrorFallback, IMCancelledFallback, " " + IMErrorFallback + " "} {
		if !IsIMFallbackAnswer(content) {
			t.Fatalf("expected exact fallback recognition for %q", content)
		}
	}
	for _, content := range []string{"", "模型曾说：" + IMErrorFallback, "抱歉，我无法核实来源。"} {
		if IsIMFallbackAnswer(content) {
			t.Fatalf("ordinary answer misclassified as fallback: %q", content)
		}
	}
}
