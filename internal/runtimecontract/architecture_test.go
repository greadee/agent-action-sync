package runtimecontract

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

func TestExecutionPackagesDoNotBridgeSyncOrPortableAuthority(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve architecture test path")
	}
	internalRoot := filepath.Dir(filepath.Dir(currentFile))
	executionPackages := []string{"runtimecontract", "codexruntime", "computenode", "workspace", "resultintake"}
	for _, name := range executionPackages {
		imports := packageImports(t, filepath.Join(internalRoot, name))
		for _, forbidden := range []string{"syncgate/internal/sync", "syncgate/internal/transport", "syncgate/internal/transport/tcp"} {
			if imports[forbidden] {
				t.Fatalf("%s must not import %s", name, forbidden)
			}
		}
	}
	for _, name := range []string{"project", "sync"} {
		imports := packageImports(t, filepath.Join(internalRoot, name))
		for _, forbidden := range executionPackages {
			if imports["syncgate/internal/"+forbidden] {
				t.Fatalf("%s must not import execution package %s", name, forbidden)
			}
		}
	}
}

func packageImports(t *testing.T, directory string) map[string]bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(directory, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]bool{}
	set := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(set, file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range parsed.Decls {
			declaration, ok := spec.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, item := range declaration.Specs {
				importSpec, ok := item.(*ast.ImportSpec)
				if !ok {
					continue
				}
				value, err := strconv.Unquote(importSpec.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				result[value] = true
			}
		}
	}
	return result
}
