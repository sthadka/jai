package jql

import "fmt"

// duckdbEmitter produces the DuckDB leaf spellings. The dashboard ATTACHes the
// SQLite file under the `jai` alias, so the physical table is `jai.issues`.
// Columns are read as VARCHAR (sqlite_all_varchar), so date/number predicates
// cast explicitly (design §8.8, §16.3).
type duckdbEmitter struct{}

// tableName returns the ATTACH-qualified issues table.
func (duckdbEmitter) tableName() string { return "jai.issues" }

// numericCompare casts the column to DOUBLE for an ordered numeric comparison.
func (duckdbEmitter) numericCompare(col, op, num string) string {
	return fmt.Sprintf("TRY_CAST(%s AS DOUBLE) %s %s", col, op, num)
}

// dateCompareRelative compares a timestamp column against engine-side date math,
// e.g. `TRY_CAST(created AS TIMESTAMP) < (CURRENT_TIMESTAMP - INTERVAL 90 DAY)`.
func (duckdbEmitter) dateCompareRelative(col, op string, ds *DateSpec) string {
	amt, unit, _ := intervalUnit(ds.Unit, ds.Amount)
	rhs := fmt.Sprintf("(CURRENT_TIMESTAMP %s INTERVAL %d %s)", intervalSign(ds), amt, unit)
	return fmt.Sprintf("TRY_CAST(%s AS TIMESTAMP) %s %s", col, op, rhs)
}

// dateCompareLiteral compares a timestamp column against a concrete timestamp
// literal (resolved now()/startOf* anchor or an absolute date).
func (duckdbEmitter) dateCompareLiteral(col, op, ts string) string {
	return fmt.Sprintf("TRY_CAST(%s AS TIMESTAMP) %s TIMESTAMP %s", col, op, quote(ts))
}

// textContains renders a case-insensitive substring match via ILIKE.
func (duckdbEmitter) textContains(col, pat string, negate bool) string {
	like := "ILIKE"
	if negate {
		like = "NOT ILIKE"
	}
	return fmt.Sprintf("%s %s %s", col, like, quote("%"+pat+"%"))
}

// arrayContains tests membership in a JSON-array column by casting it to a
// VARCHAR list and using list_contains.
func (duckdbEmitter) arrayContains(col, val string) string {
	return fmt.Sprintf("list_contains(CAST(%s AS JSON)::VARCHAR[], %s)", col, quote(val))
}
