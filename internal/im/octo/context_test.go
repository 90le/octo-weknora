package octo

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/im/octo/wire"
)

func TestScopeDocumentsArePerBotAndRefreshAfterDeletion(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("not read-only")
		}
		w.Header().Set("Content-Type", "application/json")
		if deleted {
			fmt.Fprint(w, `{"content":"","version":0}`)
			return
		}
		fmt.Fprintf(w, `{"content":%q,"version":2}`, r.Header.Get("Authorization")+r.URL.Path)
	}))
	defer server.Close()
	scope, _ := wire.ParseScope("g____2098355867442221056", 5)
	for _, token := range []string{"bf_a", "bf_b"} {
		a, _ := NewAdapter(token, "bot")
		a.api.base = server.URL
		docs, err := a.ContextDocuments(context.Background(), scope)
		if err != nil || len(docs) != 2 {
			t.Fatalf("%v %v", docs, err)
		}
		if docs[0].Name != "GROUP.md" || docs[1].Name != "THREAD.md" || docs[1].SubareaID != scope.Subarea || docs[0].Content != "Bearer "+token+"/v1/bot/groups/g/md" {
			t.Fatal("scope mismatch")
		}
	}
	deleted = true
	a, _ := NewAdapter("bf_a", "bot")
	a.api.base = server.URL
	docs, err := a.ContextDocuments(context.Background(), scope)
	if err != nil || len(docs) != 0 {
		t.Fatal("deleted content retained")
	}
}

func TestScopeDocumentsRejectPartialAndForgedScope(t *testing.T) {
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			fmt.Fprint(w, `{"content":"parent","version":1}`)
		} else {
			w.WriteHeader(403)
		}
	}))
	defer s.Close()
	a, _ := NewAdapter("bf_a", "bot")
	a.api.base = s.URL
	scope, _ := wire.ParseScope("g____123", 5)
	if docs, err := a.ContextDocuments(context.Background(), scope); err == nil || docs != nil {
		t.Fatal("silently used partial parent")
	}
	scope.Group = "other"
	if _, err := a.ContextDocuments(context.Background(), scope); err == nil || calls != 2 {
		t.Fatal("forged scope fetched")
	}
	dm, _ := wire.ParseScope("user", 1)
	if docs, err := a.ContextDocuments(context.Background(), dm); err != nil || len(docs) != 0 || calls != 2 {
		t.Fatal("DM loaded group context")
	}
}
