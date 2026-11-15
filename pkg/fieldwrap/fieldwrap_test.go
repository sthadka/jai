package fieldwrap

import (
	"fmt"
	"reflect"
	"testing"
)

func TestParseArrayInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"comma", "a@x,b@x", []string{"a@x", "b@x"}},
		{"comma with spaces", "a@x, b@x , c@x", []string{"a@x", "b@x", "c@x"}},
		{"comma empties skipped", "a@x,,b@x", []string{"a@x", "b@x"}},
		{"json array", `["a@x","b@x"]`, []string{"a@x", "b@x"}},
		{"json array with spaces", `[ "a@x", "b@x" ]`, []string{"a@x", "b@x"}},
		{"json array empties skipped", `["a@x","","b@x"]`, []string{"a@x", "b@x"}},
		{"single value", "a@x", []string{"a@x"}},
		{"empty", "", nil},
		// A bracketed but non-JSON string is treated as comma input, not silently dropped.
		{"malformed bracket falls back to comma", "[a@x", []string{"[a@x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseArrayInput(tt.input)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParseArrayInputRoundTripsReadColumn(t *testing.T) {
	// The read column serializes user arrays as a JSON array of emails. Passing
	// that value straight back into a write must yield the same set.
	readColumn := `["jvmartin@redhat.com","ksanchet@redhat.com"]`
	got := ParseArrayInput(readColumn)
	want := []string{"jvmartin@redhat.com", "ksanchet@redhat.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip got %#v, want %#v", got, want)
	}
}

func TestWrapScalarFieldValue(t *testing.T) {
	resolveOK := func(v string) (string, error) { return "resolved-" + v, nil }
	tests := []struct {
		name    string
		jiraID  string
		value   string
		resolve ResolveAccountID
		want    interface{}
		wantOK  bool
		wantErr bool
	}{
		{"priority", "priority", "Major", nil, map[string]string{"name": "Major"}, true, false},
		{"assignee no resolver", "assignee", "user123", nil, map[string]string{"accountId": "user123"}, true, false},
		{"reporter resolves", "reporter", "u@x", resolveOK, map[string]string{"accountId": "resolved-u@x"}, true, false},
		{"assignee resolver error", "assignee", "no@x", func(string) (string, error) { return "", fmt.Errorf("nope") }, nil, true, true},
		{"unknown field", "labels", "a", nil, nil, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := WrapScalarFieldValue(tt.jiraID, tt.value, tt.resolve)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantOK && !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestWrapArrayItemsUser(t *testing.T) {
	// Emails resolve to accountId objects (the fix for the "expected Object" 400).
	resolve := func(email string) (string, error) {
		switch email {
		case "jvmartin@redhat.com":
			return "acc-1", nil
		case "ksanchet@redhat.com":
			return "acc-2", nil
		}
		return "", fmt.Errorf("no user for %q", email)
	}
	got, err := WrapArrayItems("customfield_10466", "user",
		[]string{"jvmartin@redhat.com", "ksanchet@redhat.com"}, resolve)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []interface{}{
		map[string]string{"accountId": "acc-1"},
		map[string]string{"accountId": "acc-2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestWrapArrayItemsUserBareAccountIDPassthrough(t *testing.T) {
	// A resolver mirroring jira.Client.ResolveAccountID returns non-email input
	// unchanged, so an accountId passed directly is still wrapped as an object.
	resolve := func(id string) (string, error) { return id, nil }
	got, err := WrapArrayItems("customfield_10466", "user", []string{"acc-xyz"}, resolve)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []interface{}{map[string]string{"accountId": "acc-xyz"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestWrapArrayItemsUserUnresolvableErrors(t *testing.T) {
	resolve := func(string) (string, error) { return "", fmt.Errorf("no user found") }
	_, err := WrapArrayItems("customfield_10466", "user", []string{"ghost@x"}, resolve)
	if err == nil {
		t.Fatal("expected error for unresolvable user, got nil")
	}
}

func TestWrapArrayItemsNonUser(t *testing.T) {
	tests := []struct {
		name     string
		jiraID   string
		itemType string
		items    []string
		want     []interface{}
	}{
		{"component by itemType", "customfield_1", "component", []string{"UI"}, []interface{}{map[string]string{"name": "UI"}}},
		{"version by itemType", "customfield_2", "version", []string{"1.0"}, []interface{}{map[string]string{"name": "1.0"}}},
		{"components by jiraID (no itemType)", "components", "", []string{"UI"}, []interface{}{map[string]string{"name": "UI"}}},
		{"fixVersions by jiraID (no itemType)", "fixVersions", "", []string{"1.0"}, []interface{}{map[string]string{"name": "1.0"}}},
		{"option", "customfield_3", "option", []string{"Red"}, []interface{}{map[string]string{"value": "Red"}}},
		{"labels bare strings", "labels", "string", []string{"bug", "security"}, []interface{}{"bug", "security"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := WrapArrayItems(tt.jiraID, tt.itemType, tt.items, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestWrapArrayItemSingle(t *testing.T) {
	resolve := func(string) (string, error) { return "acc-1", nil }
	got, err := WrapArrayItem("customfield_10466", "user", "a@x", resolve)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got, map[string]string{"accountId": "acc-1"}) {
		t.Fatalf("got %#v", got)
	}
}
