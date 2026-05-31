package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateRepoMap_CurrentProject(t *testing.T) {
	root := findProjectRoot(t)
	rm, err := GenerateRepoMap(root)
	if err != nil {
		t.Fatalf("GenerateRepoMap: %v", err)
	}
	if len(rm.Packages) == 0 {
		t.Fatal("expected at least one package")
	}

	foundEngine := false
	foundContext := false
	for _, pkg := range rm.Packages {
		if pkg.Name == "engine" {
			foundEngine = true
		}
		if pkg.Name == "context" {
			foundContext = true
		}
	}
	if !foundEngine {
		t.Error("expected engine package in repo map")
	}
	if !foundContext {
		t.Error("expected context package in repo map")
	}
}

func TestGenerateRepoMap_FindsExportedTypes(t *testing.T) {
	root := findProjectRoot(t)
	rm, err := GenerateRepoMap(root)
	if err != nil {
		t.Fatalf("GenerateRepoMap: %v", err)
	}

	found := false
	for _, pkg := range rm.Packages {
		if pkg.Name == "engine" {
			for _, typ := range pkg.Types {
				if typ.Name == "EngineConfig" {
					found = true
					break
				}
			}
		}
	}
	if !found {
		t.Error("expected EngineConfig type in engine package")
	}
}

func TestGenerateRepoMap_FindsExportedFunctions(t *testing.T) {
	root := findProjectRoot(t)
	rm, err := GenerateRepoMap(root)
	if err != nil {
		t.Fatalf("GenerateRepoMap: %v", err)
	}

	found := false
	for _, pkg := range rm.Packages {
		if pkg.Name == "engine" {
			for _, fn := range pkg.Functions {
				if fn.Name == "NewEngine" {
					found = true
					if fn.Receiver != "" {
						t.Errorf("NewEngine should have no receiver, got %q", fn.Receiver)
					}
					break
				}
			}
		}
	}
	if !found {
		t.Error("expected NewEngine function in engine package")
	}
}

func TestGenerateRepoMap_FindsInterfaces(t *testing.T) {
	root := findProjectRoot(t)
	rm, err := GenerateRepoMap(root)
	if err != nil {
		t.Fatalf("GenerateRepoMap: %v", err)
	}

	found := false
	for _, pkg := range rm.Packages {
		if pkg.Name == "engine" {
			for _, iface := range pkg.Interfaces {
				if iface.Name == "ModelClient" {
					found = true
					if len(iface.Methods) == 0 {
						t.Error("ModelClient interface should have methods")
					}
					break
				}
			}
		}
	}
	if !found {
		t.Error("expected ModelClient interface in engine package")
	}
}

func TestRepoMap_Render(t *testing.T) {
	root := findProjectRoot(t)
	rm, err := GenerateRepoMap(root)
	if err != nil {
		t.Fatalf("GenerateRepoMap: %v", err)
	}

	output := rm.Render()
	if output == "" {
		t.Fatal("Render returned empty string")
	}
	if !strings.Contains(output, "pkg engine") {
		t.Error("rendered output should contain 'pkg engine'")
	}
	if !strings.Contains(output, "func NewEngine") {
		t.Error("rendered output should contain 'func NewEngine'")
	}
}

func TestRepoMap_FindFile(t *testing.T) {
	root := findProjectRoot(t)
	rm, err := GenerateRepoMap(root)
	if err != nil {
		t.Fatalf("GenerateRepoMap: %v", err)
	}

	dir := rm.FindFile("EngineConfig")
	if dir == "" {
		t.Fatal("FindFile should locate EngineConfig")
	}
	if !strings.Contains(dir, "engine") {
		t.Errorf("EngineConfig should be in engine dir, got %q", dir)
	}
}

func TestGenerateRepoMap_SkipsTestFiles(t *testing.T) {
	root := findProjectRoot(t)
	rm, err := GenerateRepoMap(root)
	if err != nil {
		t.Fatalf("GenerateRepoMap: %v", err)
	}

	for _, pkg := range rm.Packages {
		if strings.HasSuffix(pkg.Name, "_test") {
			t.Errorf("should not include test package: %s", pkg.Name)
		}
	}
}

func findProjectRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find project root (no go.mod found)")
		}
		dir = parent
	}
}
