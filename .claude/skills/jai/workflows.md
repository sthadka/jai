# Workflow Recipes

Ready-made SQL patterns for common Jira tasks.

## Contents
1. [Sprint Planning / Grooming](#1-sprint-planning--grooming)
2. [Status Reporting / Standup Prep](#2-status-reporting--standup-prep)
3. [Bug Triage with Duplicate Detection](#3-bug-triage-with-duplicate-detection)
4. [Spec-to-Tickets Breakdown](#4-spec-to-tickets-breakdown)
5. [Stale Issue Cleanup](#5-stale-issue-cleanup)
6. [Cross-Team Dependency Mapping](#6-cross-team-dependency-mapping)
7. [Workload Analysis](#7-workload-analysis)
8. [Release Readiness Check](#8-release-readiness-check)
9. [Retrospective Data Gathering](#9-retrospective-data-gathering)

## 1. Sprint Planning / Grooming

**Step 1: Current sprint state**
```bash
jai query "SELECT key, summary, status, priority, assignee, story_points
  FROM issues
  WHERE sprint IS NOT NULL AND status NOT IN ('Done', 'Closed')
  ORDER BY priority, story_points DESC" --json --fields key,summary,status,priority,assignee,story_points
```

**Step 2: Capacity check**
```bash
jai query "SELECT assignee, COUNT(*) as issues, SUM(story_points) as points
  FROM issues
  WHERE sprint IS NOT NULL AND status NOT IN ('Done', 'Closed')
  GROUP BY assignee
  ORDER BY points DESC" --json
```

**Step 3: Unestimated items**
```bash
jai query "SELECT key, summary, issue_type, priority
  FROM issues
  WHERE sprint IS NOT NULL AND (story_points IS NULL OR story_points = 0)
  AND status NOT IN ('Done', 'Closed')" --json
```

**Step 4: Backlog candidates (ready, unassigned, prioritized)**
```bash
jai query "SELECT key, summary, priority, story_points, issue_type
  FROM issues
  WHERE sprint IS NULL AND status IN ('To Do', 'Open', 'Backlog')
  AND priority IN ('Highest', 'High', 'Medium')
  ORDER BY CASE priority
    WHEN 'Highest' THEN 1 WHEN 'High' THEN 2 WHEN 'Medium' THEN 3 ELSE 4 END,
    created ASC
  LIMIT 20" --json
```

## 2. Status Reporting / Standup Prep

**Yesterday's completions**
```bash
jai query "SELECT key, summary, assignee, status
  FROM issues
  WHERE status IN ('Done', 'Closed') AND updated >= '{{yesterday}}'
  ORDER BY assignee" --json
```

**In-progress work**
```bash
jai query "SELECT key, summary, assignee, status, updated
  FROM issues
  WHERE status IN ('In Progress', 'In Review', 'In QA')
  AND assignee IN ({{team}})
  ORDER BY assignee, updated DESC" --json
```

**Blockers (stale in-progress)**
```bash
jai query "SELECT key, summary, assignee, status, updated,
    CAST(julianday('now') - julianday(updated) AS INTEGER) as days_stale
  FROM issues
  WHERE status = 'In Progress'
  AND updated < '{{week_ago}}'
  ORDER BY updated ASC" --json
```

**Newly created (incoming work)**
```bash
jai query "SELECT key, summary, priority, reporter, created
  FROM issues
  WHERE created >= '{{yesterday}}'
  ORDER BY priority, created DESC" --json
```

## 3. Bug Triage with Duplicate Detection

**Step 1: Search for potential duplicates**
```bash
jai search "<bug summary keywords>" --json --limit 10
```

**Step 2: If FTS finds candidates, check details**
```bash
jai query "SELECT key, summary, status, resolution, description
  FROM issues
  WHERE key IN ('PROJ-1', 'PROJ-2')
  AND issue_type = 'Bug'" --json
```

**Step 3: Get open bugs by component/area**
```bash
jai query "SELECT key, summary, status, priority, components, labels
  FROM issues
  WHERE issue_type = 'Bug' AND status NOT IN ('Done', 'Closed', 'Resolved')
  ORDER BY priority, created DESC" --json
```

**Step 4: If not a duplicate, create the bug**
```bash
jai create PROJ --type=Bug --summary="<summary>" --description="<description>" \
  --priority=High --labels=triaged --components=<component>
```

**Step 5: If duplicate found, link and comment**
```bash
jai link NEW-KEY EXISTING-KEY --type=Duplicate
jai comment EXISTING-KEY "Duplicate report received. Additional context: ..."
```

## 4. Spec-to-Tickets Breakdown

**Step 1: Discover project conventions**
```bash
jai schema values issue_type --json
jai schema values priority --json
jai schema templates --json
jai fields --json --filter "required"
```

**Step 2: Check for existing epic/feature**
```bash
jai query "SELECT key, summary, status FROM issues
  WHERE issue_type = 'Epic' AND summary LIKE '%<feature name>%'" --json
```

**Step 3: Create the epic (if not exists)**
```bash
jai create PROJ --type=Epic --summary="<epic title>" --description="<spec summary>"
```

**Step 4: Create child tickets (one per spec section/requirement)**
```bash
jai create PROJ --type=Story --summary="<story 1>" --description="..." --parent=PROJ-100
jai create PROJ --type=Story --summary="<story 2>" --description="..." --parent=PROJ-100
# ... repeat for each story
```

**Step 5: Add dependencies between tickets**
```bash
jai link PROJ-102 PROJ-101 --type="Blocks"
```

**Step 6: Verify the structure**
```bash
jai query "SELECT key, summary, issue_type, status, parent_key
  FROM issues WHERE parent_key = 'PROJ-100'
  ORDER BY key" --json
```

## 5. Stale Issue Cleanup

**Find stale issues (no update in 30+ days, still open)**
```bash
jai query "SELECT key, summary, status, assignee, updated,
    CAST(julianday('now') - julianday(updated) AS INTEGER) as days_stale
  FROM issues
  WHERE status NOT IN ('Done', 'Closed', 'Resolved')
  AND updated < '{{month_ago}}'
  ORDER BY updated ASC
  LIMIT 50" --json
```

**Group by assignee for accountability**
```bash
jai query "SELECT assignee, COUNT(*) as stale_count,
    MIN(updated) as oldest_update
  FROM issues
  WHERE status NOT IN ('Done', 'Closed', 'Resolved')
  AND updated < '{{month_ago}}'
  GROUP BY assignee
  ORDER BY stale_count DESC" --json
```

**For each stale issue, add a nudge comment or close**
```bash
jai comment PROJ-OLD "This issue has been inactive for 30+ days. Is it still relevant?"
# OR
jai transition PROJ-OLD "Closed"
jai comment PROJ-OLD "Auto-closed due to 30+ days of inactivity."
```

## 6. Cross-Team Dependency Mapping

**Find all issues with cross-project links**
```bash
jai query "SELECT i.key, i.summary, i.status, i.assignee, i.project
  FROM issues i
  WHERE i.key IN (
    SELECT DISTINCT key FROM issues WHERE description LIKE '%OTHERPROJ-%'
  )
  ORDER BY i.project, i.key" --json
```

**Find blocked issues**
```bash
jai query "SELECT key, summary, status, assignee, project, labels
  FROM issues
  WHERE status = 'Blocked' OR labels LIKE '%blocked%'
  ORDER BY priority, project" --json
```

**Issues assigned across teams**
```bash
jai query "SELECT project, assignee, COUNT(*) as count
  FROM issues
  WHERE status NOT IN ('Done', 'Closed')
  GROUP BY project, assignee
  HAVING count > 3
  ORDER BY project, count DESC" --json
```

## 7. Workload Analysis

**Current load by person**
```bash
jai query "SELECT assignee,
    COUNT(*) as total,
    SUM(CASE WHEN status = 'In Progress' THEN 1 ELSE 0 END) as in_progress,
    SUM(CASE WHEN priority IN ('Highest', 'High') THEN 1 ELSE 0 END) as high_pri,
    SUM(COALESCE(story_points, 0)) as total_points
  FROM issues
  WHERE status NOT IN ('Done', 'Closed', 'Resolved')
  AND assignee IS NOT NULL
  GROUP BY assignee
  ORDER BY total DESC" --json
```

**Overloaded (more than 5 in-progress items)**
```bash
jai query "SELECT assignee, COUNT(*) as wip
  FROM issues
  WHERE status = 'In Progress' AND assignee IS NOT NULL
  GROUP BY assignee
  HAVING wip > 5
  ORDER BY wip DESC" --json
```

**Completion velocity (last 2 weeks)**
```bash
jai query "SELECT assignee,
    COUNT(*) as completed,
    SUM(COALESCE(story_points, 0)) as points_completed
  FROM issues
  WHERE status IN ('Done', 'Closed')
  AND updated >= '{{weeks_ago:2}}'
  AND assignee IS NOT NULL
  GROUP BY assignee
  ORDER BY completed DESC" --json
```

## 8. Release Readiness Check

**Issues targeted for release version**
```bash
jai query "SELECT key, summary, status, priority, assignee, issue_type
  FROM issues
  WHERE fix_versions LIKE '%v2.1%'
  ORDER BY CASE status
    WHEN 'Done' THEN 3 WHEN 'In Progress' THEN 2 ELSE 1 END,
    priority" --json
```

**Completion summary**
```bash
jai query "SELECT status, COUNT(*) as count
  FROM issues
  WHERE fix_versions LIKE '%v2.1%'
  GROUP BY status
  ORDER BY count DESC" --json
```

**Open blockers for the release**
```bash
jai query "SELECT key, summary, priority, assignee, status
  FROM issues
  WHERE fix_versions LIKE '%v2.1%'
  AND status NOT IN ('Done', 'Closed')
  AND priority IN ('Highest', 'High', 'Blocker')
  ORDER BY priority" --json
```

**Unresolved bugs in release scope**
```bash
jai query "SELECT key, summary, priority, assignee, status
  FROM issues
  WHERE fix_versions LIKE '%v2.1%'
  AND issue_type = 'Bug'
  AND status NOT IN ('Done', 'Closed', 'Resolved')
  ORDER BY priority" --json
```

## 9. Retrospective Data Gathering

**Sprint velocity over time (completed in last sprint)**
```bash
jai query "SELECT issue_type, COUNT(*) as count,
    SUM(COALESCE(story_points, 0)) as points
  FROM issues
  WHERE status IN ('Done', 'Closed')
  AND updated >= '{{weeks_ago:2}}'
  GROUP BY issue_type" --json
```

**Bug ratio**
```bash
jai query "SELECT
    SUM(CASE WHEN issue_type = 'Bug' THEN 1 ELSE 0 END) as bugs,
    SUM(CASE WHEN issue_type != 'Bug' THEN 1 ELSE 0 END) as features,
    ROUND(100.0 * SUM(CASE WHEN issue_type = 'Bug' THEN 1 ELSE 0 END) / COUNT(*), 1) as bug_pct
  FROM issues
  WHERE status IN ('Done', 'Closed')
  AND updated >= '{{weeks_ago:2}}'" --json
```

**Cycle time (created to closed, last sprint)**
```bash
jai query "SELECT key, summary,
    CAST(julianday(updated) - julianday(created) AS INTEGER) as cycle_days
  FROM issues
  WHERE status IN ('Done', 'Closed')
  AND updated >= '{{weeks_ago:2}}'
  ORDER BY cycle_days DESC" --json
```

**Carry-over (started but not finished)**
```bash
jai query "SELECT key, summary, status, assignee, story_points
  FROM issues
  WHERE status = 'In Progress'
  AND updated < '{{week_ago}}'
  ORDER BY updated ASC" --json
```
