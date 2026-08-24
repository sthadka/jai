# Token Efficiency & Config Management

Optimization strategies and configuration rules.

## Contents
- [Token Efficiency Strategies](#token-efficiency-strategies)
- [Config Management](#config-management)

## Token Efficiency Strategies

### 1. Use --fields to limit columns
BAD:  `jai query "SELECT * FROM issues WHERE ..." --json`          (all 50+ columns)
GOOD: `jai query "SELECT key, summary, status FROM issues WHERE ..." --json`  (3 columns)

### 2. Use --json always
Human output is pretty-printed with box-drawing characters — much larger than JSON.

### 3. Use COUNT/GROUP BY before fetching details
First: `jai query "SELECT COUNT(*) FROM issues WHERE ..." --json`  (1 row)
Then:  `jai query "SELECT key, summary FROM issues WHERE ... LIMIT 10" --json`  (10 rows)

### 4. Use template variables
BAD:  `WHERE assignee = 'john.doe@company.com' AND updated >= '2026-08-17'`
GOOD: `WHERE assignee = '{{me}}' AND updated >= '{{week_ago}}'`

### 5. Use snippets for repeated patterns
If a user has defined snippets, use them: {{my_team_bugs}}, {{sprint_items}}, etc.

### 6. Use FTS for keyword search, not LIKE
BAD:  `WHERE summary LIKE '%login%' OR description LIKE '%login%'`
GOOD: `jai search "login" --json`

### 7. Use SQL JOINs, not multiple commands
BAD:  `jai get PROJ-1 --json; jai get PROJ-2 --json; jai get PROJ-3 --json`
GOOD: `jai query "SELECT key, summary FROM issues WHERE key IN ('PROJ-1','PROJ-2','PROJ-3')" --json`

### 8. Use bulk set, not individual sets
BAD:  `jai set PROJ-1 priority High; jai set PROJ-2 priority High; jai set PROJ-3 priority High`
GOOD: `jai set PROJ-1,PROJ-2,PROJ-3 priority High`
BEST: `jai set --query "SELECT key FROM issues WHERE ..." priority High`

### 9. Limit results
Always use LIMIT when exploring. Default to LIMIT 20 unless the user wants everything.

### 10. Cache schema knowledge
Run `jai schema db` once per session. Don't re-run it for every query.

## Config Management

You can read and modify jai's config file at `~/.config/jai/config.yaml` (or the path from `jai status --json`).

### When to modify config:
- User asks to track a new Jira project → add a sync_source, then run `jai sync`
- User repeatedly asks for the same report → create a named view
- A useful SQL pattern emerges → save it as a snippet
- User wants a standard issue template → add a template
- Cross-project dependency query reveals unsynced projects → suggest adding sync source

### Safety rules:
- NEVER modify the `jira.token` or `jira.email` fields — these contain credentials
- ALWAYS read the config first to understand the existing structure
- ALWAYS preserve existing entries — add, don't replace
- After modifying config, run `jai sync` if you added a sync source
- After modifying config, verify with `jai status --json` or `jai view <name> --json`

### Config location:
- Default: `~/.config/jai/config.yaml`
- Custom: check `JAI_CONFIG` env var or `--config` flag
- The config path is shown in `jai status --json` output
