package plain

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	modulePath = "github.com/niekcandaele/sitrep"
	plainPath  = modulePath + "/internal/render/plain"
	tuiPath    = modulePath + "/internal/tui"
	modelPath  = modulePath + "/internal/model"
)

var contextualPolicy = []string{"ShowsNativeStatus", "StatusField"}

type sourceGraph struct {
	imports      map[string]map[string]struct{}
	declarations map[string]map[string]struct{}
}

func TestSharedTerminalShapingArchitecture(t *testing.T) {
	production := productionSourceGraph(t)
	if err := production.architectureError(); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		mutate func(sourceGraph) sourceGraph
	}{
		{
			name: "rejects removal of the TUI shared-layer edge",
			mutate: func(graph sourceGraph) sourceGraph {
				delete(graph.imports, tuiPath)
				return graph
			},
		},
		{
			name: "rejects a reverse shared-layer-to-TUI edge",
			mutate: func(graph sourceGraph) sourceGraph {
				graph.addImport(plainPath, tuiPath)
				return graph
			},
		},
		{
			name: "rejects display policy relocated into model",
			mutate: func(graph sourceGraph) sourceGraph {
				graph.addImport(modelPath, plainPath)
				for _, name := range contextualPolicy {
					delete(graph.declarations[plainPath], name)
					graph.addDeclaration(modelPath, name)
				}
				return graph
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.mutate(production.clone()).architectureError(); err == nil {
				t.Fatal("architecture assertion accepted the mutated graph")
			}
		})
	}
}

func productionSourceGraph(t *testing.T) sourceGraph {
	t.Helper()

	graph := sourceGraph{
		imports:      make(map[string]map[string]struct{}),
		declarations: make(map[string]map[string]struct{}),
	}
	internalRoot := filepath.Join(repositoryRoot(t), "internal")
	if err := filepath.WalkDir(internalRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		relativeDirectory, err := filepath.Rel(filepath.Dir(internalRoot), filepath.Dir(path))
		if err != nil {
			return err
		}
		packagePath := modulePath + "/" + filepath.ToSlash(relativeDirectory)
		graph.addPackage(packagePath)
		for _, imported := range file.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return fmt.Errorf("unquote import in %s: %w", path, err)
			}
			graph.addImport(packagePath, importPath)
		}
		for _, declaration := range file.Decls {
			if function, ok := declaration.(*ast.FuncDecl); ok && function.Recv == nil {
				graph.addDeclaration(packagePath, function.Name.Name)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return graph
}

func repositoryRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repository root containing go.mod")
		}
		dir = parent
	}
}

func (g sourceGraph) architectureError() error {
	var violations []string
	if !g.reaches(tuiPath, plainPath) {
		violations = append(violations, "internal/tui does not reach internal/render/plain")
	}
	if g.reaches(plainPath, tuiPath) {
		violations = append(violations, "internal/render/plain reaches internal/tui")
	}
	if g.reaches(modelPath, plainPath) {
		violations = append(violations, "internal/model reaches internal/render/plain")
	}
	for _, name := range contextualPolicy {
		if !g.declares(plainPath, name) {
			violations = append(violations, name+" is not declared in internal/render/plain")
		}
		if g.declares(modelPath, name) {
			violations = append(violations, name+" is declared in internal/model")
		}
	}
	if len(violations) == 0 {
		return nil
	}
	return fmt.Errorf("shared terminal-shaping architecture violated: %s", strings.Join(violations, "; "))
}

func (g sourceGraph) reaches(from, to string) bool {
	seen := map[string]struct{}{from: {}}
	for pending := []string{from}; len(pending) > 0; {
		current := pending[0]
		pending = pending[1:]
		if current == to {
			return true
		}
		for dependency := range g.imports[current] {
			if _, alreadySeen := seen[dependency]; !alreadySeen {
				seen[dependency] = struct{}{}
				pending = append(pending, dependency)
			}
		}
	}
	return false
}

func (g sourceGraph) declares(packagePath, name string) bool {
	_, ok := g.declarations[packagePath][name]
	return ok
}

func (g sourceGraph) addPackage(packagePath string) {
	if g.imports[packagePath] == nil {
		g.imports[packagePath] = make(map[string]struct{})
		g.declarations[packagePath] = make(map[string]struct{})
	}
}

func (g sourceGraph) addImport(from, to string) {
	g.addPackage(from)
	g.imports[from][to] = struct{}{}
}

func (g sourceGraph) addDeclaration(packagePath, name string) {
	g.addPackage(packagePath)
	g.declarations[packagePath][name] = struct{}{}
}

func (g sourceGraph) clone() sourceGraph {
	clone := sourceGraph{
		imports:      make(map[string]map[string]struct{}, len(g.imports)),
		declarations: make(map[string]map[string]struct{}, len(g.declarations)),
	}
	for packagePath, imports := range g.imports {
		clone.imports[packagePath] = maps.Clone(imports)
	}
	for packagePath, declarations := range g.declarations {
		clone.declarations[packagePath] = maps.Clone(declarations)
	}
	return clone
}
