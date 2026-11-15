// Package fieldwrap converts user-supplied scalar/array field values into the
// object shapes Jira's write API requires. It is shared by the CLI (jai set)
// and the MCP write tools (jai_set) so both paths wrap values identically.
package fieldwrap

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ResolveAccountID resolves an identifier (email or account ID) to a Jira Cloud
// account ID. Implementations typically delegate to jira.Client.ResolveAccountID.
type ResolveAccountID func(identifier string) (string, error)

// ParseArrayInput parses the value of an array field into its non-empty, trimmed
// elements. It accepts either a JSON array string (`["a@x","b@x"]`, the shape
// jai's read column emits for array fields) or a comma-delimited string
// (`a@x,b@x`). JSON is tried first; anything that is not a well-formed JSON array
// falls back to a comma split. Emails never contain commas or brackets, so this
// is unambiguous for the Contributors field and other user arrays.
func ParseArrayInput(value string) []string {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "[") {
		var arr []string
		if err := json.Unmarshal([]byte(trimmed), &arr); err == nil {
			out := make([]string, 0, len(arr))
			for _, e := range arr {
				if v := strings.TrimSpace(e); v != "" {
					out = append(out, v)
				}
			}
			return out
		}
	}
	return splitComma(value)
}

func splitComma(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			result = append(result, v)
		}
	}
	return result
}

// WrapScalarFieldValue converts a scalar string value into the object shape
// Jira's write API requires for reference fields (priority, assignee, reporter).
// ok is false for fields that accept a bare string/number/date as-is, in which
// case the caller should use the raw value. resolve maps an assignee/reporter
// identifier to an account ID; pass nil to send the identifier unresolved (e.g.
// in tests).
func WrapScalarFieldValue(jiraID, value string, resolve ResolveAccountID) (result interface{}, ok bool, err error) {
	switch jiraID {
	case "priority":
		return map[string]string{"name": value}, true, nil
	case "assignee", "reporter":
		accountID := value
		if resolve != nil {
			accountID, err = resolve(value)
			if err != nil {
				return nil, true, err
			}
		}
		return map[string]string{"accountId": accountID}, true, nil
	}
	return nil, false, nil
}

// WrapArrayItems wraps the elements of an array field into the object shapes Jira
// requires, keyed on the field's element sub-type (itemType, from the Jira field
// schema's schema.items). Element types:
//
//   - user: each item is resolved to an account ID and wrapped as
//     {"accountId": id}. This covers Contributors (customfield_10466) and any
//     other user-array custom field. An item that cannot be resolved is a hard
//     per-item error, never a silent drop.
//   - component / version: {"name": item} (also matched by the components /
//     fixVersions system field IDs for older field maps lacking an itemType).
//   - option: {"value": item}.
//   - anything else (e.g. labels / plain string arrays): the bare item.
func WrapArrayItems(jiraID, itemType string, items []string, resolve ResolveAccountID) ([]interface{}, error) {
	wrapped := make([]interface{}, len(items))
	for i, item := range items {
		w, err := wrapArrayItem(jiraID, itemType, item, resolve)
		if err != nil {
			return nil, err
		}
		wrapped[i] = w
	}
	return wrapped, nil
}

// WrapArrayItem wraps a single array element. It is used by the add/remove
// operations, which touch one element at a time.
func WrapArrayItem(jiraID, itemType, item string, resolve ResolveAccountID) (interface{}, error) {
	return wrapArrayItem(jiraID, itemType, item, resolve)
}

func wrapArrayItem(jiraID, itemType, item string, resolve ResolveAccountID) (interface{}, error) {
	switch {
	case itemType == "user":
		accountID := item
		if resolve != nil {
			id, err := resolve(item)
			if err != nil {
				return nil, fmt.Errorf("resolving user %q: %w", item, err)
			}
			accountID = id
		}
		return map[string]string{"accountId": accountID}, nil
	case itemType == "component" || itemType == "version" || jiraID == "components" || jiraID == "fixVersions":
		return map[string]string{"name": item}, nil
	case itemType == "option":
		return map[string]string{"value": item}, nil
	default:
		return item, nil
	}
}
