package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/sthadka/jai/internal/db"
	"github.com/sthadka/jai/internal/jira"
	"github.com/sthadka/jai/internal/jql"
	"github.com/sthadka/jai/internal/output"
)

var (
	// queryJQL runs the arg as a LIVE JQL query against the Jira API.
	queryJQL string
	// querySQL forces the positional arg to be interpreted as local SQL.
	querySQL bool
	// queryLocalJQL forces the positional arg to be translated as local JQL.
	queryLocalJQL bool
)

var queryCmd = &cobra.Command{
	Use:   "query <sql-or-jql>",
	Short: "Run SQL or JQL against the local DB, or pass --jql for a live Jira query",
	Long: `Execute a query against the local SQLite database, or against Jira live.

The positional argument is auto-detected:
  - Leading token SELECT or WITH  -> SQL, run on the local DB (as today).
  - Anything else                 -> JQL, translated to local SQL and run on
                                     the local DB (offline, as of last sync).

Local JQL (bare arg or --local-jql) is evaluated against the local snapshot:
only synced projects and fields are visible, and it supports the v1 JQL subset
(history operators such as WAS/CHANGED and unsupported functions are rejected
with a clear message).

--jql runs the full, real-time query against the live Jira API instead; its
results may differ from the local translation.

Use --sql or --local-jql to override auto-detection explicitly.`,
	// With --jql the positional arg is not required.
	Args: func(cmd *cobra.Command, args []string) error {
		if queryJQL != "" {
			return nil
		}
		return cobra.ExactArgs(1)(cmd, args)
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		arg := ""
		if len(args) > 0 {
			arg = args[0]
		}
		route, err := resolveQueryRoute(arg, queryJQL, querySQL, queryLocalJQL)
		if err != nil {
			return err
		}
		if route == routeLiveJQL {
			if g.rawOut {
				return runJQLQueryRaw(queryJQL)
			}
			return runJQLQuery(queryJQL)
		}
		if g.rawOut {
			return fmt.Errorf("--raw is only supported with --jql")
		}
		if route == routeSQL {
			return runSQLQuery(arg)
		}
		return runLocalJQLQuery(arg)
	},
}

// queryRoute selects which backend a `jai query` invocation runs against.
type queryRoute int

const (
	// routeLiveJQL runs the argument as a live, real-time query via the Jira API.
	routeLiveJQL queryRoute = iota
	// routeSQL runs the argument as SQL against the local DB.
	routeSQL
	// routeLocalJQL translates the argument as JQL and runs it on the local DB.
	routeLocalJQL
)

// resolveQueryRoute decides how to interpret a `jai query` invocation (design
// §16.1/§16.2). --jql always means live and never consults the local translator;
// otherwise --sql / --local-jql force a path, and a bare argument is SQL when its
// leading token is SELECT/WITH and local JQL otherwise.
func resolveQueryRoute(arg, liveJQL string, forceSQL, forceLocalJQL bool) (queryRoute, error) {
	if liveJQL != "" {
		return routeLiveJQL, nil
	}
	if forceSQL && forceLocalJQL {
		return 0, fmt.Errorf("--sql and --local-jql are mutually exclusive")
	}
	switch {
	case forceSQL:
		return routeSQL, nil
	case forceLocalJQL:
		return routeLocalJQL, nil
	case leadingTokenIsSQL(arg):
		return routeSQL, nil
	default:
		return routeLocalJQL, nil
	}
}

// leadingTokenIsSQL reports whether the query's leading token (case-insensitive)
// is SELECT or WITH, which marks it as SQL rather than JQL (design §16.2). JQL
// never begins with those keywords (it is `field OP value`).
func leadingTokenIsSQL(arg string) bool {
	t := strings.TrimLeft(arg, " \t\r\n")
	tok := t
	if end := strings.IndexAny(t, " \t\r\n("); end >= 0 {
		tok = t[:end]
	}
	switch strings.ToUpper(tok) {
	case "SELECT", "WITH":
		return true
	default:
		return false
	}
}

func runSQLQuery(sql string) error {
	results, err := g.query.Execute(sql)
	if err != nil {
		if g.jsonOut {
			fmt.Println(string(output.Err("QueryError", err.Error())))
			return nil
		}
		return err
	}

	cols, rows := results.Columns, results.Rows
	if g.fields != "" {
		cols, rows = output.FilterColumns(cols, rows, output.ParseFields(g.fields))
	}

	// --json flag takes precedence over --format for backward compatibility.
	if g.jsonOut {
		syncAge := GetSyncAge()
		fmt.Println(string(output.OKQueryWithMeta(cols, rows, len(rows), syncAge)))
		return nil
	}

	// Use --format flag.
	switch g.format {
	case "json":
		syncAge := GetSyncAge()
		fmt.Println(string(output.OKQueryWithMeta(cols, rows, len(rows), syncAge)))
	case "csv":
		fmt.Print(output.CSV(cols, rows))
	case "tsv":
		fmt.Print(output.TSV(cols, rows))
	case "markdown":
		fmt.Print(output.Markdown(cols, rows))
	default: // "table"
		fmt.Print(output.Table(cols, rows))
	}

	return nil
}

// jqlColumnRe whitelists physical column names before they are interpolated into
// the IDResolver lookup SQL. All column names come from the local field_map, but
// this is the defense-in-depth guard that keeps the query injection-free.
var jqlColumnRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// runLocalJQLQuery translates a JQL string into local SQLite via the shared
// internal/jql translator and runs the compiled SQL through the existing SQL
// path, so --format/--fields and the --json envelope behave identically to a
// bare SQL query (design §16.3/§16.4).
func runLocalJQLQuery(jqlStr string) error {
	fieldMap, err := buildJQLFieldMap()
	if err != nil {
		return jqlLocalError(err)
	}

	opts := jql.Options{
		Dialect:    jql.SQLite,
		FieldMap:   fieldMap,
		IDResolver: jqlIDResolver(fieldMap),
		Now:        time.Now(),
	}
	if g.cfg != nil {
		opts.CurrentUser = g.cfg.Jira.Email
	}
	// The shared default projection names columns (e.g. team) that may not exist
	// in this snapshot's issues table; restrict it to columns actually present so
	// the compiled SELECT is valid SQLite (design §16.4).
	cols, err := localJQLSelectColumns()
	if err != nil {
		return jqlLocalError(err)
	}
	opts.SelectColumns = cols

	res, err := jql.Translate(jqlStr, opts)
	if err != nil {
		return jqlLocalError(err)
	}
	return runSQLQuery(res.SQL)
}

// jqlLocalError reports a local-JQL translation failure, honoring the --json
// error envelope the same way runSQLQuery does for SQL errors.
func jqlLocalError(err error) error {
	if g.jsonOut {
		fmt.Println(string(output.Err("JQLError", err.Error())))
		return nil
	}
	return err
}

// localJQLDefaultColumns is the issue-list projection for translated local JQL
// (design §8.1/§16.4). It is filtered against the actual issues table so columns
// absent from a given snapshot (e.g. team) are dropped rather than erroring.
var localJQLDefaultColumns = []string{"key", "type", "priority", "status", "team", "created", "summary"}

// localJQLSelectColumns returns the default projection restricted to columns that
// exist in the local issues table.
func localJQLSelectColumns() ([]string, error) {
	existing, err := issuesColumnSet()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(localJQLDefaultColumns))
	for _, c := range localJQLDefaultColumns {
		if existing[c] {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		out = []string{"key"}
	}
	return out, nil
}

// issuesColumnSet reads the physical column names of the issues table.
func issuesColumnSet() (map[string]bool, error) {
	rows, err := g.db.Query("PRAGMA table_info(issues)")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols := map[string]bool{}
	for rows.Next() {
		var (
			cid       int
			name, typ string
			notNull   int
			dfltValue interface{}
			pk        int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dfltValue, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

// buildJQLFieldMap builds the translator's field map from the local field_map,
// mapping both the canonical Name and the Jira display name (JiraName),
// case-insensitively, to the physical column (design §16.3).
func buildJQLFieldMap() (jql.FieldMap, error) {
	mappings, err := g.db.AllFieldMappings()
	if err != nil {
		return nil, err
	}
	fm := make(jql.FieldMap, len(mappings)*2)
	for _, m := range mappings {
		if m.Name == "" || !jqlColumnRe.MatchString(m.Name) {
			continue
		}
		def := jql.FieldDef{
			Column: m.Name,
			Kind:   jqlFieldKind(m.Type),
			Entity: jqlIsEntity(m),
		}
		fm[strings.ToLower(m.Name)] = def
		if m.JiraName != "" {
			fm[strings.ToLower(m.JiraName)] = def
		}
	}
	return fm, nil
}

// jqlFieldKind maps a field_map storage type to the translator's FieldKind.
func jqlFieldKind(t string) jql.FieldKind {
	switch strings.ToLower(t) {
	case "datetime", "date":
		return jql.KindDate
	case "number":
		return jql.KindNumber
	case "array":
		return jql.KindArray
	default:
		return jql.KindString
	}
}

// jqlIsEntity reports whether a field's values may be opaque entity ids (account
// ids / GUIDs) that need id → display-name resolution: the user-typed fields.
func jqlIsEntity(m *db.FieldMapping) bool {
	return strings.EqualFold(m.Type, "user") || strings.EqualFold(m.ItemType, "user")
}

// jqlIDResolver resolves an entity id to its display name against the local
// snapshot. It finds the id anywhere in the stored raw_json and reads back the
// field's column; a unique value wins, zero or ambiguous (>1) matches fail so
// the resolver can reject the id with guidance (design §8.5/§16.3).
func jqlIDResolver(fm jql.FieldMap) jql.IDResolver {
	return func(field, id string) (string, bool, error) {
		def, ok := fm[strings.ToLower(field)]
		if !ok || !jqlColumnRe.MatchString(def.Column) {
			return "", false, nil
		}
		col := def.Column
		q := "SELECT DISTINCT " + col + " FROM issues WHERE raw_json LIKE '%'||?||'%' AND " +
			col + " IS NOT NULL AND " + col + " <> '' LIMIT 2"
		rows, err := g.db.Query(q, id)
		if err != nil {
			return "", false, err
		}
		defer rows.Close()

		var names []string
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				return "", false, err
			}
			names = append(names, n)
		}
		if err := rows.Err(); err != nil {
			return "", false, err
		}
		if len(names) != 1 {
			return "", false, nil
		}
		return names[0], true, nil
	}
}

// jqlColumns defines the default columns returned for a live JQL query.
var jqlColumns = []string{"key", "summary", "status", "priority", "assignee", "updated"}

// jqlAPIFields are the Jira field IDs requested from the API by default.
var jqlAPIFields = []string{"summary", "status", "priority", "assignee", "updated"}

// jqlColumnsToAPIFields maps requested output columns to the Jira API field IDs
// that must be fetched to populate them. Without this, a --fields selection
// (e.g. "description") would request only the default fields and the column
// would come back null.
func jqlColumnsToAPIFields(cols []string) []string {
	set := make(map[string]bool)
	for _, col := range cols {
		switch col {
		case "key":
			// Always returned by the API.
			continue
		case "summary", "status", "priority", "assignee", "reporter", "created", "updated", "labels", "parent", "description":
			set[col] = true
		case "type", "issuetype":
			set["issuetype"] = true
		case "project":
			set["project"] = true
		case "resolved", "resolution_date":
			set["resolutiondate"] = true
		}
	}
	fields := make([]string, 0, len(set))
	for f := range set {
		fields = append(fields, f)
	}
	if len(fields) == 0 {
		return jqlAPIFields
	}
	return fields
}

func runJQLQuery(jql string) error {
	cols := jqlColumns
	apiFields := jqlAPIFields
	if g.fields != "" {
		cols = output.ParseFields(g.fields)
		apiFields = jqlColumnsToAPIFields(cols)
	}

	var rows [][]interface{}
	for page, err := range g.jira.SearchAll(context.Background(), jql, apiFields) {
		if err != nil {
			if g.jsonOut {
				fmt.Println(string(output.Err("JQLError", err.Error())))
				return nil
			}
			return fmt.Errorf("jql query: %w", err)
		}
		for _, issue := range page {
			row, err := jqlIssueToRow(issue, cols)
			if err != nil {
				continue
			}
			rows = append(rows, row)
		}
	}

	// --json flag takes precedence over --format for backward compatibility.
	if g.jsonOut {
		syncAge := GetSyncAge()
		fmt.Println(string(output.OKQueryWithMeta(cols, rows, len(rows), syncAge)))
		return nil
	}

	// Use --format flag.
	switch g.format {
	case "json":
		syncAge := GetSyncAge()
		fmt.Println(string(output.OKQueryWithMeta(cols, rows, len(rows), syncAge)))
	case "csv":
		fmt.Print(output.CSV(cols, rows))
	case "tsv":
		fmt.Print(output.TSV(cols, rows))
	case "markdown":
		fmt.Print(output.Markdown(cols, rows))
	default: // "table"
		fmt.Print(output.Table(cols, rows))
	}

	return nil
}

func runJQLQueryRaw(jql string) error {
	var issues []*jira.Issue
	for page, err := range g.jira.SearchAll(context.Background(), jql, []string{"*all"}) {
		if err != nil {
			if g.jsonOut {
				fmt.Println(string(output.Err("JQLError", err.Error())))
				return nil
			}
			return fmt.Errorf("jql query: %w", err)
		}
		issues = append(issues, page...)
	}

	if g.jsonOut {
		b, _ := json.Marshal(issues)
		result := map[string]interface{}{
			"ok":    true,
			"data":  json.RawMessage(b),
			"count": len(issues),
		}
		out, _ := json.Marshal(result)
		fmt.Println(string(out))
		return nil
	}

	b, _ := json.Marshal(issues)
	fmt.Println(string(b))
	return nil
}

// jqlIssueToRow extracts the requested columns from a live Jira issue.
func jqlIssueToRow(issue *jira.Issue, cols []string) ([]interface{}, error) {
	var fields jira.IssueFields
	if err := json.Unmarshal(issue.Fields, &fields); err != nil {
		return nil, err
	}

	get := func(col string) interface{} {
		switch col {
		case "key":
			return issue.Key
		case "summary":
			return fields.Summary
		case "status":
			if fields.Status != nil {
				return fields.Status.Name
			}
		case "priority":
			if fields.Priority != nil {
				return fields.Priority.Name
			}
		case "assignee":
			if fields.Assignee != nil {
				return fields.Assignee.DisplayName
			}
		case "reporter":
			if fields.Reporter != nil {
				return fields.Reporter.DisplayName
			}
		case "type", "issuetype":
			if fields.IssueType != nil {
				return fields.IssueType.Name
			}
		case "project":
			if fields.Project != nil {
				return fields.Project.Key
			}
		case "created":
			return fields.Created
		case "updated":
			return fields.Updated
		case "resolved":
			return fields.ResolutionDate
		case "labels":
			return strings.Join(fields.Labels, ", ")
		case "parent":
			if fields.Parent != nil {
				return fields.Parent.Key
			}
		case "description":
			if md := jira.ADFToMarkdown(fields.Description); md != "" {
				return md
			}
		}
		return nil
	}

	row := make([]interface{}, len(cols))
	for i, col := range cols {
		row[i] = get(col)
	}
	return row, nil
}

func init() {
	queryCmd.Flags().StringVar(&queryJQL, "jql", "", "run a live, real-time JQL query against the Jira API (bypasses the local DB)")
	queryCmd.Flags().BoolVar(&querySQL, "sql", false, "force the argument to be interpreted as local SQL")
	queryCmd.Flags().BoolVar(&queryLocalJQL, "local-jql", false, "force the argument to be translated as local JQL (offline, as of last sync)")
	rootCmd.AddCommand(queryCmd)
}
