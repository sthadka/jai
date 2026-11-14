# Testing changelog sync locally

Build first:

```bash
go build -tags fts5 -o jai ./cmd/jai
```

## 1. Sync issues (if not already done)

```bash
./jai sync
```

## 2. Sync changelogs

```bash
# All sources
./jai sync --changelogs

# Single source (tests multi-project filter fix)
./jai sync --changelogs --source my-project
```

Expected: spinner shows progress like `⠹ changelogs 42/180 issues`, then a final `✓ changelogs 180/180 issues synced`.

## 3. Verify data landed

```bash
# Count entries
./jai query "SELECT count(*) as total FROM changelog"

# Status transitions
./jai query "SELECT issue_key, from_string, to_string, changed_at FROM changelog WHERE field='status' ORDER BY changed_at DESC LIMIT 10"

# All field changes for one issue
./jai query "SELECT field, from_string, to_string, author, changed_at FROM changelog WHERE issue_key='ROX-12345' ORDER BY changed_at"
```

## 4. Verify incremental sync skips already-synced issues

```bash
# Run again — should show 0/N or very few issues to sync
./jai sync --changelogs
```

## 5. Verify migration v7 created all 3 indexes

```bash
./jai query "SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='changelog'"
```

Expected: `idx_changelog_issue`, `idx_changelog_time`, `idx_changelog_field_to`.
