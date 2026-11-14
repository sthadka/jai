# jai Improvements: Lessons from the CIPOE Field Discovery

## What Happened

We spent a full research cycle concluding that "CIPOE `account_number` and `ebs_account_number` fields are empty across all 87K issues" and designing an elaborate name-matching strategy (25.4% match rate, 3-5 hours manual mapping). In reality, the data was sitting in `ebs_account_number_11128` — a different column that maps to the same Jira display name "EBS Account Number" — the whole time. One spot-check by a human found what the tooling obscured.

The root cause is not a single bug but a chain of small jai design decisions that compound into a discoverability trap for duplicate-named fields.

## The Chain of Problems

### 1. Silent name disambiguation hides the "real" field

When two Jira fields share a display name, `DiscoverFields` appends a numeric suffix to the second one:

```
"EBS Account Number" (customfield_11007, text)   → ebs_account_number        ← EMPTY
"EBS Account Number" (customfield_11128, array)   → ebs_account_number_11128  ← HAS DATA
```

The suffixed name `_11128` looks like an internal artifact, not the primary field. A user or AI agent querying `ebs_account_number` (the intuitive name) gets the empty text field and concludes the data doesn't exist. There's no warning that another field with the same Jira name exists and might have the actual data.

**Impact:** This is the core discoverability failure. The user's mental model is "I want EBS Account Number" → they find `ebs_account_number` → it's empty → they stop looking.

### 2. `jai fields` doesn't show the Jira name by default

The `jai fields` output shows `name | jira_id | type | fts`. It does NOT show `jira_name` (the Jira display name). So a user filtering for "ebs" sees:

```
ebs_account_number            customfield_11007   text    *
ebs_account_number_11128      customfield_11128   array   *
ebs_account_number_old_dont_use  customfield_11092  array  *
```

Without the Jira name column, you can't tell that `ebs_account_number` and `ebs_account_number_11128` share the same Jira display name "EBS Account Number." The suffixed one looks like a duplicate or legacy field, not the canonical source.

### 3. `jai schema db` doesn't show Jira names at all

The `schema db` command (designed for AI agents) returns `{name, type, custom}` per column. An AI agent sees `ebs_account_number` (text) and `ebs_account_number_11128` (text — SQLite doesn't distinguish array) with no Jira context. It will naturally query the unsuffixed name.

### 4. Array fields lose their structure on storage

`extractFieldValue` for arrays does `strings.Join(values, ",")`, storing `["123","456","789"]` as the text `123,456,789`. This means:

- **No `json_each()` support.** You can't write `SELECT ... FROM issues, json_each(ebs_account_number_11128)` because it's not JSON — it's comma-separated text. This is what caused the "malformed JSON" error during our analysis.
- **Ambiguous parsing.** A value containing a comma would be indistinguishable from two separate values (unlikely for account numbers, but a general problem).
- **Inconsistent format.** Some rows store as JSON arrays (from the raw JSON fallback path?), others as comma-separated text. Queries must handle both.

### 5. No "population" metadata for field discovery

There's no quick way to ask "which fields actually have data?" When investigating CIPOE, we queried `ebs_account_number` (empty), concluded the data wasn't there, and moved on. If `jai fields` or `jai schema db` showed a populated percentage or non-null count, the discrepancy between the two EBS fields would have been immediately visible:

```
ebs_account_number        text    0% populated
ebs_account_number_11128  array   99.7% populated   ← this would have caught our eye
```

## Recommendations

### R1. Show Jira name in `jai fields` output (low effort, high impact)

Add `jira_name` as a column in the `jai fields` table output. This immediately surfaces that `ebs_account_number` and `ebs_account_number_11128` are both "EBS Account Number" in Jira.

```
name                          jira_name               jira_id              type
ebs_account_number            EBS Account Number      customfield_11007    text
ebs_account_number_11128      EBS Account Number      customfield_11128    array
```

Same for `jai schema db` — include `jira_name` in the JSON output so AI agents can see the mapping.

### R2. Warn on name collisions during field discovery (low effort)

When `DiscoverFields` appends a suffix due to a name collision, emit a visible warning:

```
warning: field "EBS Account Number" (customfield_11128) renamed to ebs_account_number_11128 
         (name collision with customfield_11007)
```

This at least makes the disambiguation visible at sync time.

### R3. Store arrays as JSON, not comma-separated text (medium effort)

Change `extractFieldValue` for the `array` case to store JSON arrays instead of comma-joined strings:

```go
// Before:
return strings.Join(strs, ",")

// After:
jsonBytes, _ := json.Marshal(strs)
return string(jsonBytes)
```

This enables `json_each()` in SQL queries, which is the natural way to search within array fields:

```sql
SELECT i.key, j.value as ebs_num
FROM issues i, json_each(i.ebs_account_number_11128) j
WHERE i.project = 'CIPOE' AND j.value = '5745596'
```

**Migration:** Existing data would need a one-time conversion. Could be done as a migration step during the next sync, or as a `jai migrate` command.

### R4. Add a `jai fields --stats` or `jai fields --populated` flag (medium effort)

Show non-null counts or percentages per field, optionally scoped by project:

```
jai fields --filter ebs --stats --project CIPOE

name                          type    populated    sample
ebs_account_number            text    0/87488 (0%)
ebs_account_number_11128      array   87219/87488 (99.7%)   1064562,10773883,...
ebs_account_number_old_dont_use array  0/87488 (0%)
```

This is the single most useful discovery aid. It answers "does this field have data?" without writing SQL.

### R5. Add `jira_name` to `jai schema db` output (low effort)

The `schema db` command is designed for AI agents. Currently it returns:

```json
{"name": "ebs_account_number_11128", "type": "text", "custom": true}
```

Adding `jira_name` lets AI agents understand what the field actually represents:

```json
{"name": "ebs_account_number_11128", "type": "text", "custom": true, "jira_name": "EBS Account Number"}
```

## Priority

| # | Recommendation | Effort | Impact | Priority |
|---|---------------|--------|--------|----------|
| R1 | Show Jira name in `jai fields` | Low | High | Do first |
| R5 | Add `jira_name` to `schema db` | Low | High | Do first |
| R2 | Warn on name collisions | Low | Medium | Do first |
| R4 | `fields --stats` population counts | Medium | High | Do second |
| R3 | Store arrays as JSON | Medium | Medium | Do third (breaking change, needs migration) |

R1, R2, and R5 are quick wins that prevent this class of problem from recurring. R4 is the highest-impact feature for field exploration. R3 is the most principled fix but requires a data migration.
