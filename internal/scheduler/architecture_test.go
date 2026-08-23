package scheduler

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestSchedulerCannotWriteCanonicalHistoryOrCrossTransportBoundary(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve scheduler package")
	}
	files, err := filepath.Glob(filepath.Join(filepath.Dir(current), "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	forbidden := map[string]bool{
		"syncgate/internal/projector":       true,
		"syncgate/internal/resultintake":    true,
		"syncgate/internal/workhistory":     true,
		"syncgate/internal/integrationgate": true,
		"syncgate/internal/sync":            true,
		"syncgate/internal/transport":       true,
		"syncgate/internal/transport/tcp":   true,
	}
	set := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(set, file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range parsed.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, item := range general.Specs {
				spec, ok := item.(*ast.ImportSpec)
				if !ok {
					continue
				}
				path, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				if forbidden[path] {
					t.Fatalf("scheduler must not import %s", path)
				}
			}
		}
	}
}
