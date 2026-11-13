package jql

import "testing"

func TestLexBasicTokens(t *testing.T) {
	toks, err := lex(`project = ROX and created < -90d`)
	if err != nil {
		t.Fatalf("lex error: %v", err)
	}
	want := []tokenKind{tIdent, tEq, tIdent, tAnd, tIdent, tLt, tRelDate, tEOF}
	if len(toks) != len(want) {
		t.Fatalf("got %d tokens, want %d: %+v", len(toks), len(want), toks)
	}
	for i, w := range want {
		if toks[i].kind != w {
			t.Errorf("token %d: kind %d, want %d (text %q)", i, toks[i].kind, w, toks[i].text)
		}
	}
}

func TestLexMultiCharOperators(t *testing.T) {
	cases := map[string]tokenKind{
		"!=": tNeq,
		">=": tGte,
		"<=": tLte,
		"!~": tNContains,
		"~":  tContains,
		">":  tGt,
		"<":  tLt,
		"=":  tEq,
	}
	for src, want := range cases {
		toks, err := lex("a " + src + " b")
		if err != nil {
			t.Fatalf("lex %q: %v", src, err)
		}
		if toks[1].kind != want {
			t.Errorf("operator %q lexed as kind %d, want %d", src, toks[1].kind, want)
		}
	}
}

func TestLexQuotedMultiWord(t *testing.T) {
	toks, err := lex(`status = "To Do"`)
	if err != nil {
		t.Fatalf("lex error: %v", err)
	}
	if toks[2].kind != tString || toks[2].text != "To Do" {
		t.Fatalf("got %+v, want tString 'To Do'", toks[2])
	}
}

func TestLexSingleQuoteAndEscape(t *testing.T) {
	toks, err := lex(`summary ~ 'a \'quoted\' word'`)
	if err != nil {
		t.Fatalf("lex error: %v", err)
	}
	if toks[2].text != `a 'quoted' word` {
		t.Fatalf("unescaped text = %q", toks[2].text)
	}
}

func TestLexGUIDAndCustomFieldAndAccountID(t *testing.T) {
	src := `Team != ec74d716-af36-4b3c-950f-f79213d08f71-1819 and cf[10234] = 5 and assignee = 557058:abc-def`
	toks, err := lex(src)
	if err != nil {
		t.Fatalf("lex error: %v", err)
	}
	// GUID is a single ident token.
	if toks[2].kind != tIdent || toks[2].text != "ec74d716-af36-4b3c-950f-f79213d08f71-1819" {
		t.Errorf("GUID token = %+v", toks[2])
	}
	// cf[10234] is a single ident token.
	if toks[4].kind != tIdent || toks[4].text != "cf[10234]" {
		t.Errorf("cf token = %+v", toks[4])
	}
	// account id is a single ident token.
	if toks[10].kind != tIdent || toks[10].text != "557058:abc-def" {
		t.Errorf("account id token = %+v", toks[10])
	}
}

func TestLexNumberAndRelativeDates(t *testing.T) {
	toks, _ := lex(`x = 10000 y < -2w z > -3M w > -1y`)
	if toks[2].kind != tNumber || toks[2].text != "10000" {
		t.Errorf("number token = %+v", toks[2])
	}
	for _, i := range []int{5, 8, 11} {
		if toks[i].kind != tRelDate {
			t.Errorf("token %d = %+v, want tRelDate", i, toks[i])
		}
	}
}

func TestLexCaseInsensitiveKeywords(t *testing.T) {
	toks, _ := lex(`a In (x) And b Is Not Empty Order By c Desc`)
	wantKinds := []tokenKind{tIdent, tIn, tLParen, tIdent, tRParen, tAnd, tIdent, tIs, tNot, tEmpty, tOrder, tBy, tIdent, tDesc, tEOF}
	if len(toks) != len(wantKinds) {
		t.Fatalf("got %d tokens, want %d", len(toks), len(wantKinds))
	}
	for i, w := range wantKinds {
		if toks[i].kind != w {
			t.Errorf("token %d kind %d, want %d", i, toks[i].kind, w)
		}
	}
}

func TestLexOffsets(t *testing.T) {
	toks, _ := lex(`project = ROX`)
	if toks[0].off != 0 {
		t.Errorf("field offset = %d, want 0", toks[0].off)
	}
	if toks[1].off != 8 {
		t.Errorf("op offset = %d, want 8", toks[1].off)
	}
	if toks[2].off != 10 {
		t.Errorf("value offset = %d, want 10", toks[2].off)
	}
}

func TestLexErrorsHaveOffset(t *testing.T) {
	if _, err := lex(`a = "unterminated`); err == nil {
		t.Fatal("expected unterminated string error")
	}
	if _, err := lex(`a ! b`); err == nil {
		t.Fatal("expected error for lone '!'")
	}
}
