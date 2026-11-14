# Manual Validation: RIT Gap Features

Prereqs: a synced jai database with at least a few issues. Run `jai status` to confirm.

## 1. Array Field Operations (--add / --remove)

```bash
# Pick a test issue
KEY=ROX-123

# Check current labels
jai get $KEY --fields key,labels --json

# Add a label
jai set $KEY --add labels rit-test-label
jai get $KEY --fields key,labels --json
# Expect: "rit-test-label" appended to existing labels

# Add another
jai set $KEY --add labels rit-test-label-2
jai get $KEY --fields key,labels --json
# Expect: both new labels present, originals preserved

# Remove one
jai set $KEY --remove labels rit-test-label
jai get $KEY --fields key,labels --json
# Expect: "rit-test-label" gone, "rit-test-label-2" still there

# Error case: --add on a scalar field
jai set $KEY --add priority High
# Expect: error "priority is not an array field"

# Push to Jira and verify
jai push
# Check the issue in Jira web UI to confirm labels are correct
```

## 2. Array Field Serialization (scalar set on array field)

```bash
# Set labels as comma-separated (replaces all labels)
jai set $KEY labels "bug,security,rit-test"
jai get $KEY --fields key,labels --json
# Expect: labels stored as ["bug","security","rit-test"]

# Push and verify in Jira
jai push
```

## 3. Transition Command

```bash
# List available transitions
jai transition $KEY --list
jai transition $KEY --list --json

# Execute a transition (pick one from the list above)
jai transition $KEY "In Progress"
# Expect: immediate push (no separate jai push needed)

# Verify status changed
jai get $KEY --fields key,status --json

# Case insensitive match
jai transition $KEY "done"

# Bad transition name
jai transition $KEY "NonExistentStatus"
# Expect: error listing available transitions
```

## 4. Bulk Set (--query and comma-separated keys)

```bash
# Comma-separated keys
jai set ROX-1,ROX-2,ROX-3 priority Major
# Expect: "queued 3 changes (pending sync)"

# JSON output
jai set ROX-1,ROX-2 priority Minor --json
# Expect: {"ok":true,"data":{"count":2,"keys":["ROX-1","ROX-2"]}}

# Query-based bulk set
jai set --query "SELECT key FROM issues WHERE type = 'Bug' LIMIT 5" priority Major
# Expect: "queued N changes (pending sync)"

# Zero results
jai set --query "SELECT key FROM issues WHERE key = 'NONEXISTENT'" priority Major
# Expect: clear "no matching issues" message

# Bad SQL
jai set --query "SELECT bad_col FROM nope" priority Major
# Expect: clear SQL error

# Don't forget to push
jai push
```

## 5. Link Command

```bash
# Create a default "Relates" link
jai link ROX-1 ROX-2
jai link ROX-1 ROX-2 --json

# Create a typed link
jai link ROX-1 ROX-3 --type "blocks"

# List available link types
jai link --list-types
jai link --list-types --json

# Verify in Jira web UI that links appear on the issues
```

## Cleanup

```bash
# Remove test labels
jai set $KEY --remove labels rit-test-label-2
jai set $KEY --remove labels rit-test
jai push
```
