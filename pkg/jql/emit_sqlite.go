package jql

import "fmt"

// sqliteEmitter produces the SQLite leaf spellings used by the `jai query` CLI.
// SQLite runs directly against jai.db, so the table is the unqualified `issues`.
// SQLite's typing is lax (CAST AS REAL), LIKE is case-insensitive for ASCII by
// default, and JSON arrays are walked with json_each (design §8.3, §16.3).
type sqliteEmitter struct{}

// tableName returns the unqualified issues table.
func (sqliteEmitter) tableName() string { return "issues" }

// numericCompare casts the column to REAL for an ordered numeric comparison.
func (sqliteEmitter) numericCompare(col, op, num string) string {
	return fmt.Sprintf("CAST(%s AS REAL) %s %s", col, op, num)
}

// dateCompareRelative compares the column against SQLite's datetime() date math,
// e.g. `created < datetime('now','-90 day')`. SQLite stores ISO-8601 text, so a
// direct lexical comparison is correct.
func (sqliteEmitter) dateCompareRelative(col, op string, ds *DateSpec) string {
	amt, _, unit := intervalUnit(ds.Unit, ds.Amount)
	modifier := fmt.Sprintf("%s%d %s", intervalSign(ds), amt, unit)
	return fmt.Sprintf("%s %s datetime('now',%s)", col, op, quote(modifier))
}

// dateCompareLiteral compares the column against a concrete timestamp literal
// (resolved now()/startOf* anchor or an absolute date).
func (sqliteEmitter) dateCompareLiteral(col, op, ts string) string {
	return fmt.Sprintf("%s %s %s", col, op, quote(ts))
}

// textContains renders a substring match via LIKE (case-insensitive for ASCII).
func (sqliteEmitter) textContains(col, pat string, negate bool) string {
	like := "LIKE"
	if negate {
		like = "NOT LIKE"
	}
	return fmt.Sprintf("%s %s %s", col, like, quote("%"+pat+"%"))
}

// arrayContains tests membership in a JSON-array column by walking it with
// json_each.
func (sqliteEmitter) arrayContains(col, val string) string {
	return fmt.Sprintf("EXISTS (SELECT 1 FROM json_each(%s) WHERE value = %s)", col, quote(val))
}
