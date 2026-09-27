package query

import "testing"

// FuzzParse exercises the Lucene query parser with arbitrary strings. It
// checks that parsing does not panic and that serialising and re-parsing the
// AST also does not panic. The corpus includes representative query-string
// examples from the project tests and from real-world Lucene syntax.
func FuzzParse(f *testing.F) {
	seeds := []string{
		"", // empty input
		"asn:AS13335",
		`asn:AS13335 AND (page.url.keyword:*lidl* OR task.url.keyword:*lidl* OR filename.keyword:*lidl*)`,
		"page.url.keyword:*lidl*",
		"name:John*",
		"age:[18 TO 99]",
		`title:"quick brown"`,
		"*:*",
		"+must:term -not:term",
		"NOT field:value",
		"field:test",
		"127.0.0.1",
		"t-est",
		"t+est",
		"33",
		"cat-dog",
		"watex~",
		"field:>5",
		"field:>=5",
		"field:-5",
		"field:<-5",
		`name:marty\\:couchbase`,
		`marty\\ couchbase`,
		`\\+marty`,
		`\\-marty`,
		`"what does \\"quote\\" mean"`,
		`can\\ i\\ escap\\e`,
		"   what",
		`3.0\\:`,
		"age:65^10",
		`title:"quick brown"^5`,
		"(foo bar)",
		"((a OR b) AND c)",
		`status:"200" OR method:"GET"`,
		"+@timestamp:[now-14d TO now]",
		"asn:AS_A OR asn:AS_B",
		`page.url.keyword:"https://accept.example.com/one"`,
		"field:(value1 value2)",
		"name:Mart*",
		"_exists_:title",
		"_missing_:title",
		"field:*",
		"title:search engine",
		"user.name:John",
		"foo AND bar",
		"foo OR bar",
		"foo bar",
		"-foo",
		"+foo",
		"NOT foo",
		"*",
		"?",
		"a*b?c",
	}

	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, input string) {
		node, err := Parse(input)
		if err != nil {
			return
		}

		// Serialising the AST must not panic, even for weird inputs.
		s := node.String()

		// Re-parsing what we serialised must also not panic.
		_, _ = Parse(s)
	})
}
