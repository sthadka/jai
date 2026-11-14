# jai findings: Project `rank` ordering + `contributors` not populated

Context: **team-map** consumes jai as an MCP server (`jai serve --transport=http
--db=/data/jai/jai.db --config=/etc/jai/config.yaml --no-sync`). Its Jira sync
runs this query and stores the columns into its own Postgres `project` table:

```sql
SELECT key, summary, status, priority, assignee_email, contributors, components,
       created, due_date, target_start, target_end, updated, rank
FROM issues WHERE type = 'Project' ORDER BY key ASC
```

Two prod symptoms traced back to jai. **Neither is a team-map bug** — team-map
stores exactly what jai returns (verified byte-identical, see §1).

- Symptom A: the project list ordering in team-map does **not** match Jira's
  `ORDER BY Rank`.
- Symptom B: Jira Contributors are **not** shown — no engineers get assigned.

Data source of truth: Red Hat Jira `https://redhat.atlassian.net`, project ROX,
`issuetype = Project` (34 tickets). All commands below run against the jai CLI.

---

## 1. `rank` is NOT stale — jai already has Jira's exact value

A full sync does not change it, and that is correct: jai's stored `rank` equals
the live Jira value.

```
# live raw fetch (hits Jira API), bucket-0 example
$ jai get ROX-36341 --raw | jq -r '.fields.customfield_10019'
0|iu6esq:zv
# jai stored (issues.rank) == 0|iu6esq:zv   → identical
```

team-map's 34 stored ranks are byte-identical to jai's `issues.rank`
(`diff` empty), and jai's `issues.rank` is byte-identical to live Jira
`customfield_10019`. So `jai sync --full` reporting `0 updated` is accurate —
there is nothing to refresh at the value level.

**Implication:** the ordering problem is not staleness. It is that the raw
`customfield_10019` string cannot reproduce Jira's rank order (see §2).

---

## 2. `customfield_10019` spans two LexoRank buckets; raw-string sort ≠ Jira order

The 34 Project tickets currently carry a **mix of LexoRank buckets**: 19 in
bucket `0|…`, 15 in bucket `1|…`. This is a real, current Jira state (each value
was verified live), i.e. a partial/settled rebalance — not a jai artifact.

LexoRank background (Atlassian):
- rank string is `<bucket>|<decimal>[:...]`; the bucket digit (0/1/2) is a
  **rebalance generation**, not a primary sort key.
- Rebalancing rewrites ranks into the next bucket round-robin (0→1→2→0),
  preserving order; during/after it, issues can span two buckets.
- Rebalancing is **event-triggered** (rank length ≥128 chars → within 12h, ≥160
  → immediate; exhausted gaps; or manual admin) — **not** a periodic/every-few-days
  job. A mixed-bucket state can therefore persist indefinitely.

Jira's `ORDER BY Rank` produces a **merge** of the two buckets. Verified:
- within each bucket, `customfield_10019` is ascending in Jira's order
  (`bucket 0 ascending_within=True`, `bucket 1 ascending_within=True`);
- the buckets are **interleaved** down Jira's order:
  `1110111111100001100000000010100100`.

No raw-string interpretation of `customfield_10019` reproduces Jira's order —
tested and all `False`:

| sort key of cf10019            | matches Jira `ORDER BY Rank`? |
|--------------------------------|-------------------------------|
| full string (`0\|iu6esq:zv`)   | no |
| body after `\|`                | no |
| core (no bucket, no suffix)    | no |

Concrete counterexample (Jira `ORDER BY Rank ASC`, first rows, with cf10019):

```
 1 ROX-36454  1|hy3ggf:
 2 ROX-36456  1|hy3ggj:
 3 ROX-36979  1|hy3ggn:
 4 ROX-37038  0|iu6esq:i     ← bucket-0 value sits BETWEEN bucket-1 values
 5 ROX-36990  1|hy3gh3:
 ...
12 ROX-37079  0|iu6esq:uzr
15 ROX-36341  0|iu6esq:zv
16 ROX-37096  1|hy3gjj:      ← back to bucket 1
```

A plain `ORDER BY rank` (what any naive consumer does, incl. team-map today)
groups all `0|` then all `1|`, so e.g. ROX-36454 (Jira #1) lands at position 20.

### What jai should do for rank

The stored LexoRank string is **insufficient** to reconstruct Jira's order once
issues span buckets. Options, best first:

1. **Expose Jira's authoritative order as an ordinal.** At sync, run the source
   JQL with `ORDER BY Rank ASC` and persist each issue's 0-based position (e.g. a
   `rank_ordinal INTEGER` column, or a normalized monotonic sort key). Consumers
   order by that ordinal. This delegates ordering to Jira (the only component that
   knows the cross-bucket comparator) and is robust to rebalances.
2. If you keep only the raw string, **document that it is single-bucket-only** and
   provide a comparator/materialized sort key that implements LexoRank's
   cross-bucket semantics. Re-deriving that from the snapshot string is fragile;
   option 1 is preferred.

"Does Jira expose a reusable sort algorithm?" — LexoRank is a published algorithm
(Atlassian `LexoRank` Java class; several OSS ports), but its documented guarantee
is **within one active bucket**; cross-bucket ordering during rebalance is
Jira-internal. The portable answer for external consumers is option 1: ask Jira
to sort (`ORDER BY Rank`) and store the resulting order.

---

## 3. `contributors` (and target dates) never populated despite Jira having values

team-map resolves engineers from the `contributors` column. It is **empty for all
34 Project rows** even after `jai sync --full`, though Jira clearly has the field
set.

```
# live raw — Jira HAS contributors (array of users)
$ jai get ROX-36440 --raw | jq '.fields.customfield_10466 | map(.emailAddress)'
[ "jvmartin@redhat.com", "ksanchet@redhat.com", ... ]

# but jai stores empty
$ jai query "SELECT count(*) FILTER (WHERE contributors IS NOT NULL AND contributors<>''), \
             count(*) FROM issues WHERE type='Project'" --db=/data/jai/jai.db --no-sync
0 | 34
```

Field IS mapped in `field_map` (so column exists), yet not extracted:

```
$ jai query "SELECT jira_id,name,type FROM field_map WHERE jira_id='customfield_10466'"
customfield_10466 | contributors | array
```

`target_start` / `target_end` are likewise empty. Note the field-type pattern:
`rank` (scalar `text`) extracts fine; `contributors` (**array of user objects**)
does not. Suspected jai gap: extraction/serialization for user-array (and
possibly date) custom fields into their columns.

### What jai should do for contributors

- Populate the `contributors` column from `customfield_10466` (array of users),
  serialized so a consumer can read member identities — recommended: a
  stable, delimited list of `emailAddress` (fall back to `accountId`/`displayName`
  when email is absent/hidden). team-map keys engineers by email.
- Verify the same extraction path for other array/user and date custom fields
  (`target_start`/`target_end`).

---

## Reproduction quick-reference

```bash
# live vs stored rank (identical → not stale)
jai get ROX-36341 --raw | jq -r '.fields.customfield_10019'      # 0|iu6esq:zv

# Jira's authoritative order (bucket-interleaved)
jai query --jql "project = ROX AND issuetype = Project ORDER BY Rank ASC" --fields key

# stored ranks show two buckets
jai query "SELECT substr(rank,1,2) b, count(*) FROM issues WHERE type='Project' \
           GROUP BY 1" --db=/data/jai/jai.db --no-sync                # 0| ->19, 1| ->15

# contributors present live, empty in db
jai get ROX-36440 --raw | jq '.fields.customfield_10466 | length'    # >0
jai query "SELECT contributors FROM issues WHERE key='ROX-36440'" --db=/data/jai/jai.db --no-sync   # empty
```

## Summary for the jai fix

1. **rank ordering:** raw `customfield_10019` cannot reproduce Jira's order across
   LexoRank buckets (current ROX state: 19×`0|`, 15×`1|`). Expose an ordinal
   captured from Jira `ORDER BY Rank` (preferred) or a cross-bucket sort key.
   The value itself is already correct/current — this is a sort-representation
   gap, not a sync-freshness gap.
2. **contributors:** populate `customfield_10466` (user array) into the
   `contributors` column (delimited emails); audit array/user + date custom-field
   extraction generally (`target_start`/`target_end` also empty).
