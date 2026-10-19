package cmd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestForkUpdateCheckOff(t *testing.T) {
	assert.False(t, forkUpdateCheck())
}

// TestUpdaterGuardedByFork fails when the update check is started without asking
// forkUpdateCheck: evcc's condition alone lets it run for the fork's version.
func TestUpdaterGuardedByFork(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "root.go", nil, 0)
	require.NoError(t, err)

	mentions := func(n ast.Node, name string) bool {
		found := false
		ast.Inspect(n, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == name {
				found = true
			}
			return !found
		})
		return found
	}

	checked := 0
	ast.Inspect(f, func(n ast.Node) bool {
		ifs, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}

		// the if whose body starts the updater, not an enclosing one
		for _, st := range ifs.Body.List {
			if es, ok := st.(*ast.GoStmt); ok && mentions(es.Call.Fun, "updater") && mentions(es.Call.Fun, "Run") {
				checked++
				assert.True(t, mentions(ifs.Cond, "forkUpdateCheck"), "the condition of the update check does not ask forkUpdateCheck")
			}
		}

		return true
	})

	assert.Equal(t, 1, checked, "expected exactly one update check in root.go")
}
