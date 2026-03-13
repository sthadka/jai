package cli

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"
	"github.com/syethadk/jai/internal/output"
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
				fmt.Println(string(output.Err("QueryError", err.Error())))
				return nil
			}
			return err
		}

		if len(results.Rows) == 0 {
			msg := fmt.Sprintf("issue %s not found in local database (try: jai sync)", key)
			if g.jsonOut {
				fmt.Println(string(output.Err("NotFoundError", msg)))
				return nil
			}
			return fmt.Errorf("%s", msg)
		}

		if g.jsonOut {
			// Build map from first row.
			data := make(map[string]interface{}, len(results.Columns))
			for i, col := range results.Columns {
				data[col] = results.Rows[0][i]
			}
			// Apply --fields filter.
			if g.fields != "" {
				data = output.FilterFields(data, output.ParseFields(g.fields))
			}
			fmt.Println(string(output.OK(data)))
			return nil
		}

		// Human output: key-value pairs.
		row := results.Rows[0]
		fields := make(map[string]interface{}, len(results.Columns))
		for i, col := range results.Columns {
			fields[col] = row[i]
		}

		fmt.Printf("  %-22s %s\n", "Key:", output.ValueStr(fields["key"]))
		fmt.Printf("  %-22s %s\n", "Summary:", output.ValueStr(fields["summary"]))
		fmt.Println()

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
			if v == nil || output.ValueStr(v) == "" {
				continue
			}
			fmt.Print(output.KV(toTitle(k), v))
		}

		return nil
	},
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
