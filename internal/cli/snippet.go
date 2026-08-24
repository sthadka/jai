package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/config"
	"github.com/sthadka/jai/internal/output"
	"gopkg.in/yaml.v3"
)

var snippetCmd = &cobra.Command{
	Use:   "snippet",
	Short: "Manage SQL query snippets",
	Long: `Manage SQL query snippets for common queries.

Snippets are reusable SQL fragments that can be used in queries.
Built-in snippets provide common patterns, and you can add your own.`,
}

var snippetListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all available snippets",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(g.cfgPath)
		if err != nil {
			if g.jsonOut {
				fmt.Println(string(output.Err("ConfigLoadError", err.Error())))
				return nil
			}
			return fmt.Errorf("loading config: %w", err)
		}

		if len(cfg.Snippets) == 0 {
			if g.jsonOut {
				fmt.Println(string(output.OK(map[string]interface{}{"snippets": []string{}})))
				return nil
			}
			fmt.Println("No snippets defined")
			return nil
		}

		// Sort snippet names for consistent output.
		names := make([]string, 0, len(cfg.Snippets))
		for name := range cfg.Snippets {
			names = append(names, name)
		}
		sort.Strings(names)

		if g.jsonOut {
			fmt.Println(string(output.OK(map[string]interface{}{"snippets": names})))
			return nil
		}

		// Human-readable output.
		fmt.Println("Available Snippets")
		fmt.Println(strings.Repeat("─", 40))
		for _, name := range names {
			fmt.Printf("  %s\n", name)
		}

		return nil
	},
}

var snippetShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show snippet SQL",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]

		cfg, err := config.Load(g.cfgPath)
		if err != nil {
			if g.jsonOut {
				fmt.Println(string(output.Err("ConfigLoadError", err.Error())))
				return nil
			}
			return fmt.Errorf("loading config: %w", err)
		}

		sql, ok := cfg.Snippets[name]
		if !ok {
			if g.jsonOut {
				fmt.Println(string(output.Err("SnippetNotFound", fmt.Sprintf("snippet '%s' not found", name))))
				return nil
			}
			return fmt.Errorf("snippet '%s' not found", name)
		}

		if g.jsonOut {
			fmt.Println(string(output.OK(map[string]interface{}{
				"name": name,
				"sql":  sql,
			})))
			return nil
		}

		// Human-readable output.
		fmt.Printf("Snippet: %s\n", name)
		fmt.Println(strings.Repeat("─", len(name)+9))
		fmt.Println(sql)

		return nil
	},
}

var snippetAddCmd = &cobra.Command{
	Use:   "add <name> <sql>",
	Short: "Add a user-defined snippet to config",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		sql := args[1]

		// Load existing config.
		cfgPath := g.cfgPath
		if cfgPath == "" {
			cfgPath = config.DefaultConfigPath()
		}

		data, err := os.ReadFile(cfgPath)
		if err != nil {
			if g.jsonOut {
				fmt.Println(string(output.Err("ConfigReadError", err.Error())))
				return nil
			}
			return fmt.Errorf("reading config file: %w", err)
		}

		// Parse YAML to preserve structure and comments.
		var doc yaml.Node
		if err := yaml.Unmarshal(data, &doc); err != nil {
			if g.jsonOut {
				fmt.Println(string(output.Err("ConfigParseError", err.Error())))
				return nil
			}
			return fmt.Errorf("parsing config file: %w", err)
		}

		// Load config to get current snippets.
		cfg, err := config.Load(cfgPath)
		if err != nil {
			if g.jsonOut {
				fmt.Println(string(output.Err("ConfigLoadError", err.Error())))
				return nil
			}
			return fmt.Errorf("loading config: %w", err)
		}

		if cfg.Snippets == nil {
			cfg.Snippets = make(map[string]string)
		}
		cfg.Snippets[name] = sql

		// Find or create snippets section in YAML.
		if err := addSnippetToYAML(&doc, name, sql); err != nil {
			if g.jsonOut {
				fmt.Println(string(output.Err("YAMLUpdateError", err.Error())))
				return nil
			}
			return fmt.Errorf("updating YAML: %w", err)
		}

		// Write back to config file.
		out, err := yaml.Marshal(&doc)
		if err != nil {
			if g.jsonOut {
				fmt.Println(string(output.Err("YAMLMarshalError", err.Error())))
				return nil
			}
			return fmt.Errorf("marshaling YAML: %w", err)
		}

		if err := os.WriteFile(cfgPath, out, 0644); err != nil {
			if g.jsonOut {
				fmt.Println(string(output.Err("ConfigWriteError", err.Error())))
				return nil
			}
			return fmt.Errorf("writing config file: %w", err)
		}

		if g.jsonOut {
			fmt.Println(string(output.OK(map[string]interface{}{
				"name": name,
				"sql":  sql,
			})))
			return nil
		}

		fmt.Printf("Added snippet '%s' to %s\n", name, cfgPath)
		return nil
	},
}

// addSnippetToYAML adds or updates a snippet in the YAML document tree.
func addSnippetToYAML(doc *yaml.Node, name, sql string) error {
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return fmt.Errorf("invalid YAML document structure")
	}

	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("root node is not a mapping")
	}

	// Find snippets key in the mapping.
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value == "snippets" {
			snippetsNode := root.Content[i+1]
			if snippetsNode.Kind != yaml.MappingNode {
				return fmt.Errorf("snippets node is not a mapping")
			}

			// Check if snippet already exists.
			for j := 0; j < len(snippetsNode.Content); j += 2 {
				if snippetsNode.Content[j].Value == name {
					// Update existing snippet.
					snippetsNode.Content[j+1].Value = sql
					snippetsNode.Content[j+1].Style = yaml.LiteralStyle
					return nil
				}
			}

			// Add new snippet.
			snippetsNode.Content = append(snippetsNode.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Value: name},
				&yaml.Node{Kind: yaml.ScalarNode, Value: sql, Style: yaml.LiteralStyle},
			)
			return nil
		}
	}

	// Snippets section doesn't exist, create it.
	snippetsKey := &yaml.Node{Kind: yaml.ScalarNode, Value: "snippets"}
	snippetsValue := &yaml.Node{
		Kind: yaml.MappingNode,
		Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: name},
			{Kind: yaml.ScalarNode, Value: sql, Style: yaml.LiteralStyle},
		},
	}
	root.Content = append(root.Content, snippetsKey, snippetsValue)

	return nil
}

func init() {
	snippetCmd.AddCommand(snippetListCmd)
	snippetCmd.AddCommand(snippetShowCmd)
	snippetCmd.AddCommand(snippetAddCmd)
	rootCmd.AddCommand(snippetCmd)
}
