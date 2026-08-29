package gateway

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestUsageRecordLiteralsCarryCacheDetail asserts that every storage.UsageRecord
// built in this package sets the cache breakdown.
//
// This is a source-level check rather than a behavioural one on purpose. There
// are three places that record usage — the non-streaming handler, the streaming
// handler and the telemetry logger — and the defects fixed in #59 and #60 were
// both "the non-streaming path was corrected and the streaming path was not".
// A behavioural test only covers the paths someone thought to exercise; this one
// finds the literals, so a fourth call site added later cannot omit the fields
// quietly. It fails with the file and line, which is what makes it actionable.
func TestUsageRecordLiteralsCarryCacheDetail(t *testing.T) {
	required := []string{"CachedTokens", "CacheWrite5mTokens", "CacheWrite1hTokens"}

	// The files are enumerated here rather than with parser.ParseDir, which is
	// deprecated and, more to the point, drops files whose build tags exclude
	// them. A call site behind a tag still writes usage rows, so it has to be
	// checked like any other.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("listing package source: %v", err)
	}

	fset := token.NewFileSet()
	found := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}

		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			sel, ok := lit.Type.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "UsageRecord" {
				return true
			}
			if ident, ok := sel.X.(*ast.Ident); !ok || ident.Name != "storage" {
				return true
			}
			found++

			set := map[string]bool{}
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if key, ok := kv.Key.(*ast.Ident); ok {
					set[key.Name] = true
				}
			}
			for _, field := range required {
				if !set[field] {
					t.Errorf("%s: storage.UsageRecord literal does not set %s; "+
						"the row it writes cannot be repriced from its own contents",
						fset.Position(lit.Pos()), field)
				}
			}
			return true
		})
	}

	// Without this the test passes by finding nothing — a rename of the struct,
	// or a move to a helper in another package, would make it silently vacuous.
	if found < 3 {
		t.Fatalf("found %d storage.UsageRecord literals, expected at least 3 "+
			"(non-streaming handler, streaming handler, telemetry logger); "+
			"if a call site moved, point this test at it rather than lowering the bound", found)
	}
}
