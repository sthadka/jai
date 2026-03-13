package cli

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/spf13/cobra"
)

var getCmd = &cobra.Command{
	Use:   "get <key>",
	Short: "Fetch a single issue from the local database",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := args[0]

		results, err := g.query.Execute("SELECT * FROM issues WHERE key = ?", key)
		if err != nil {
			if g.jsonOut {
				jsonError("QueryError", err.Error())
			}
			return err
		}

		if len(results.Rows) == 0 {
			msg := fmt.Sprintf("issue %s not found in local database (try: jai sync)", key)
			if g.jsonOut {
				jsonError("NotFoundError", msg)
			}
			return fmt.Errorf("%s", msg)
		}

		if g.jsonOut {
			data, err := results.SingleJSON()
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			return nil
		}

		// Human output: key-value pairs.
		row := results.Rows[0]
		// Build map.
		fields := make(map[string]interface{}, len(results.Columns))
		for i, col := range results.Columns {
			fields[col] = row[i]
		}

		// Print key and summary first.
		printField("Key", fields["key"])
		printField("Summary", fields["summary"])
		fmt.Println()

		// Print remaining fields in alphabetical order, skipping large/internal ones.
		skip := map[string]bool{"key": true, "summary": true, "raw_json": true, "comments_text": true}
		keys := make([]string, 0, len(fields))
		for k := range fields {
			if !skip[k] {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := fields[k]
			if v == nil || fmt.Sprintf("%v", v) == "" {
				continue
			}
			printField(toTitle(k), v)
		}

		return nil
	},
}

func printField(label string, val interface{}) {
	if val == nil {
		return
	}
	var s string
	switch v := val.(type) {
	case []byte:
		s = string(v)
	case string:
		s = v
	default:
		b, _ := json.Marshal(v)
		s = string(b)
	}
	if s == "" || s == "null" {
		return
	}
	fmt.Printf("  %-20s %s\n", label+":", s)
}

func toTitle(s string) string {
	result := make([]byte, 0, len(s))
	capitalize := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '_' {
			result = append(result, ' ')
			capitalize = true
			continue
		}
		if capitalize {
			if c >= 'a' && c <= 'z' {
				c -= 32
			}
			capitalize = false
		}
		result = append(result, c)
	}
	return string(result)
}

func init() {
	rootCmd.AddCommand(getCmd)
}
