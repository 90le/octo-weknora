package im

import "testing"

func TestSafeProgressToolStagesDoNotExposeUnrecognizedInputs(t *testing.T) {
	for name, want := range map[string]string{
		"search_knowledge": "knowledge", "read_document": "documents", "octo_configuration": "configuration",
		"octo_knowledge_preview": "preview", "octo_knowledge_confirm": "confirmation", "arbitrary sensitive string": "",
	} {
		if got := safeProgressToolStage(name); got != want {
			t.Fatalf("%q: got %q want %q", name, got, want)
		}
	}
}
