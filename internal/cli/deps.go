package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/output"
)

var (
	depsDepth   int
	depsProject string
)

var depsCmd = &cobra.Command{
	Use:   "deps <issue-key>",
	Short: "Show issue dependencies",
	Long: `Show issue link dependencies in tree format.

Single issue mode:
  jai deps ROX-123 [--depth N]    # show tree (default depth=1, max=5)

Project mode:
  jai deps --project ROX          # show cross-project summary`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if depsProject != "" {
			return runDepsProject(depsProject)
		}

		if len(args) == 0 {
			return fmt.Errorf("issue key required (or use --project)")
		}

		issueKey := strings.ToUpper(args[0])
		return runDepsTree(issueKey, depsDepth)
	},
}

func init() {
	depsCmd.Flags().IntVar(&depsDepth, "depth", 1, "recursion depth (max 5)")
	depsCmd.Flags().StringVar(&depsProject, "project", "", "show cross-project summary for a project")
	rootCmd.AddCommand(depsCmd)
}

// linkSummary represents a link summary in project mode.
type linkSummary struct {
	TargetProject string `json:"target_project"`
	LinkType      string `json:"link_type"`
	Count         int    `json:"count"`
}

// runDepsTree shows a tree view of dependencies for a single issue.
func runDepsTree(issueKey string, depth int) error {
	if depth < 1 {
		depth = 1
	}
	if depth > 5 {
		depth = 5
	}

	// Get the root issue.
	issue, err := g.db.GetIssue(issueKey)
	if err != nil {
		return fmt.Errorf("fetching issue: %w", err)
	}
	if issue == nil {
		return fmt.Errorf("issue %s not found", issueKey)
	}

	// Build the tree.
	tree := buildDepsTree(issueKey, depth, make(map[string]bool))

	if g.jsonOut {
		fmt.Println(string(output.OK(tree)))
		return nil
	}

	// Human output: print tree.
	summary := ""
	status := ""
	if s, ok := issue["summary"].(string); ok {
		summary = s
	}
	if st, ok := issue["status"].(string); ok {
		status = st
	}
	fmt.Printf("%s: %s (%s)\n", issueKey, summary, status)
	printDepsTree(tree.Links, "", true)

	// Check for unsynced projects.
	unsynced := findUnsyncedProjects(tree)
	if len(unsynced) > 0 {
		fmt.Println()
		for _, proj := range unsynced {
			fmt.Printf("⚠ Target project %s is not synced locally. Add it: jai init --add-source %s\n", proj, proj)
		}
	}

	return nil
}

// runDepsProject shows a cross-project dependency summary.
func runDepsProject(project string) error {
	query := `
		SELECT l.linked_project, l.link_type, COUNT(*) as count
		FROM issue_links l
		JOIN issues i ON l.issue_key = i.key
		WHERE i.project = ?
		  AND l.linked_project != ''
		  AND l.linked_project != i.project
		GROUP BY l.linked_project, l.link_type
		ORDER BY l.linked_project, count DESC
	`

	rows, err := g.db.Query(query, project)
	if err != nil {
		return fmt.Errorf("querying links: %w", err)
	}
	defer rows.Close()

	var results []linkSummary

	for rows.Next() {
		var ls linkSummary
		if err := rows.Scan(&ls.TargetProject, &ls.LinkType, &ls.Count); err != nil {
			continue
		}
		results = append(results, ls)
	}

	if g.jsonOut {
		type response struct {
			Project      string        `json:"project"`
			Links        []linkSummary `json:"links"`
			UnsyncedProjects []string  `json:"unsynced_projects"`
		}
		unsynced := findUnsyncedProjectsFromResults(results)
		fmt.Println(string(output.OK(response{
			Project:          project,
			Links:            results,
			UnsyncedProjects: unsynced,
		})))
		return nil
	}

	// Human output.
	if len(results) == 0 {
		fmt.Printf("No cross-project dependencies for %s\n", project)
		return nil
	}

	fmt.Printf("Cross-project dependencies for %s:\n", project)

	// Group by target project.
	byProject := make(map[string][]linkSummary)
	for _, r := range results {
		byProject[r.TargetProject] = append(byProject[r.TargetProject], r)
	}

	for targetProj, links := range byProject {
		totalCount := 0
		linkTypes := make(map[string]int)
		for _, l := range links {
			totalCount += l.Count
			linkTypes[l.LinkType] = l.Count
		}

		// Format link type breakdown.
		var parts []string
		for lt, count := range linkTypes {
			parts = append(parts, fmt.Sprintf("%d %s", count, lt))
		}

		fmt.Printf("  %s → %s: %d links (%s)\n", project, targetProj, totalCount, strings.Join(parts, ", "))
	}

	// Check for unsynced projects.
	unsynced := findUnsyncedProjectsFromResults(results)
	if len(unsynced) > 0 {
		fmt.Println()
		for _, proj := range unsynced {
			fmt.Printf("⚠ Target project %s is not synced locally. Add it: jai init --add-source %s\n", proj, proj)
		}
	}

	return nil
}

// depsTreeNode represents a node in the dependency tree.
type depsTreeNode struct {
	Key     string          `json:"key"`
	Summary string          `json:"summary"`
	Status  string          `json:"status"`
	Project string          `json:"project"`
	Links   []depsTreeLink  `json:"links"`
}

// depsTreeLink represents a link in the dependency tree.
type depsTreeLink struct {
	Type      string        `json:"type"`
	Direction string        `json:"direction"` // "→" or "←"
	Target    depsTreeNode  `json:"target"`
}

// buildDepsTree recursively builds a dependency tree.
func buildDepsTree(issueKey string, depth int, visited map[string]bool) depsTreeNode {
	if visited[issueKey] {
		return depsTreeNode{Key: issueKey, Summary: "(circular)", Status: "", Links: nil}
	}
	visited[issueKey] = true

	issue, _ := g.db.GetIssue(issueKey)
	node := depsTreeNode{Key: issueKey}
	if issue != nil {
		if s, ok := issue["summary"].(string); ok {
			node.Summary = s
		}
		if st, ok := issue["status"].(string); ok {
			node.Status = st
		}
		if p, ok := issue["project"].(string); ok {
			node.Project = p
		}
	}

	if depth <= 0 {
		return node
	}

	// Fetch links.
	query := `
		SELECT link_type, direction, linked_key, linked_summary, linked_status, linked_project
		FROM issue_links
		WHERE issue_key = ?
	`
	rows, err := g.db.Query(query, issueKey)
	if err != nil {
		return node
	}
	defer rows.Close()

	for rows.Next() {
		var linkType, direction, linkedKey, linkedSummary, linkedStatus, linkedProject string
		if err := rows.Scan(&linkType, &direction, &linkedKey, &linkedSummary, &linkedStatus, &linkedProject); err != nil {
			continue
		}

		// Build child node recursively.
		childNode := buildDepsTree(linkedKey, depth-1, visited)
		if childNode.Summary == "" {
			childNode.Summary = linkedSummary
		}
		if childNode.Status == "" {
			childNode.Status = linkedStatus
		}
		if childNode.Project == "" {
			childNode.Project = linkedProject
		}

		dirSymbol := "→"
		if direction == "inward" {
			dirSymbol = "←"
		}

		node.Links = append(node.Links, depsTreeLink{
			Type:      linkType,
			Direction: dirSymbol,
			Target:    childNode,
		})
	}

	return node
}

// printDepsTree prints the dependency tree in human-readable format.
func printDepsTree(links []depsTreeLink, prefix string, isLast bool) {
	for i, link := range links {
		isLastLink := i == len(links)-1
		branch := "├── "
		if isLast && isLastLink {
			branch = "└── "
		} else if isLastLink {
			branch = "└── "
		}

		statusIcon := ""
		if strings.Contains(strings.ToLower(link.Target.Status), "done") ||
			strings.Contains(strings.ToLower(link.Target.Status), "closed") {
			statusIcon = " ✓"
		} else if link.Target.Project != "" {
			// Check if target project is synced.
			if !isProjectSynced(link.Target.Project) {
				statusIcon = " ⚠️"
			}
		}

		fmt.Printf("%s%s%s %s %s: %s (%s)%s\n",
			prefix, branch, link.Type, link.Direction,
			link.Target.Key, link.Target.Summary, link.Target.Status, statusIcon)

		// Recurse to child links.
		if len(link.Target.Links) > 0 {
			childPrefix := prefix
			if isLast && isLastLink {
				childPrefix += "    "
			} else {
				childPrefix += "│   "
			}
			printDepsTree(link.Target.Links, childPrefix, true)
		}
	}
}

// findUnsyncedProjects finds projects referenced in links that are not synced locally.
func findUnsyncedProjects(tree depsTreeNode) []string {
	seen := make(map[string]bool)
	var unsynced []string

	var walk func(node depsTreeNode)
	walk = func(node depsTreeNode) {
		if node.Project != "" && !seen[node.Project] {
			seen[node.Project] = true
			if !isProjectSynced(node.Project) {
				unsynced = append(unsynced, node.Project)
			}
		}
		for _, link := range node.Links {
			walk(link.Target)
		}
	}

	walk(tree)
	return unsynced
}

// findUnsyncedProjectsFromResults finds unsynced projects from link summary results.
func findUnsyncedProjectsFromResults(results []linkSummary) []string {
	seen := make(map[string]bool)
	var unsynced []string

	for _, r := range results {
		if !seen[r.TargetProject] {
			seen[r.TargetProject] = true
			if !isProjectSynced(r.TargetProject) {
				unsynced = append(unsynced, r.TargetProject)
			}
		}
	}

	return unsynced
}

// isProjectSynced checks if a project is in the sync sources.
func isProjectSynced(project string) bool {
	for _, src := range g.cfg.SyncSources {
		for _, p := range src.Projects {
			if p == project {
				return true
			}
		}
	}
	return false
}
