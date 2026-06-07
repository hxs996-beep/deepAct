package context

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type RepoMap struct {
	Root     string
	Packages []PackageInfo
}

type PackageInfo struct {
	Name       string
	Dir        string
	Types      []TypeInfo
	Functions  []FuncInfo
	Interfaces []InterfaceInfo
	Vars       []string
	Consts     []string
}

type TypeInfo struct {
	Name   string
	Fields []FieldInfo
}

type FieldInfo struct {
	Name string
	Type string
}

type FuncInfo struct {
	Name     string
	Receiver string
	Params   string
	Returns  string
}

type InterfaceInfo struct {
	Name    string
	Methods []string
}

func GenerateRepoMap(root string) (*RepoMap, error) {
	rm := &RepoMap{Root: root}
	pkgDirs := findGoPackages(root)
	for _, dir := range pkgDirs {
		pkg, err := parsePackageDir(dir, root)
		if err != nil {
			continue
		}
		if pkg != nil && (len(pkg.Types) > 0 || len(pkg.Functions) > 0 || len(pkg.Interfaces) > 0) {
			rm.Packages = append(rm.Packages, *pkg)
		}
	}
	return rm, nil
}

func (rm *RepoMap) Render() string {
	var b strings.Builder
	for _, pkg := range rm.Packages {
		b.WriteString(fmt.Sprintf("pkg %s (%s/)\n", pkg.Name, pkg.Dir))
		for _, iface := range pkg.Interfaces {
			b.WriteString(fmt.Sprintf("  interface %s\n", iface.Name))
			for _, m := range iface.Methods {
				b.WriteString(fmt.Sprintf("    %s\n", m))
			}
		}
		for _, t := range pkg.Types {
			b.WriteString(fmt.Sprintf("  type %s struct\n", t.Name))
			for _, f := range t.Fields {
				b.WriteString(fmt.Sprintf("    %s %s\n", f.Name, f.Type))
			}
		}
		for _, fn := range pkg.Functions {
			if fn.Receiver != "" {
				b.WriteString(fmt.Sprintf("  func (%s) %s(%s) %s\n", fn.Receiver, fn.Name, fn.Params, fn.Returns))
			} else {
				b.WriteString(fmt.Sprintf("  func %s(%s) %s\n", fn.Name, fn.Params, fn.Returns))
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (rm *RepoMap) FindFile(symbol string) string {
	lower := strings.ToLower(symbol)
	for _, pkg := range rm.Packages {
		for _, t := range pkg.Types {
			if strings.ToLower(t.Name) == lower {
				return pkg.Dir
			}
		}
		for _, fn := range pkg.Functions {
			if strings.ToLower(fn.Name) == lower {
				return pkg.Dir
			}
		}
		for _, iface := range pkg.Interfaces {
			if strings.ToLower(iface.Name) == lower {
				return pkg.Dir
			}
		}
	}
	return ""
}

func findGoPackages(root string) []string {
	var dirs []string
	seen := make(map[string]bool)
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			base := filepath.Base(path)
			if base == ".git" || base == "vendor" || base == "node_modules" || base == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			dir := filepath.Dir(path)
			if !seen[dir] {
				seen[dir] = true
				dirs = append(dirs, dir)
			}
		}
		return nil
	})
	sort.Strings(dirs)
	return dirs
}

func parsePackageDir(dir, root string) (*PackageInfo, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(info os.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	relDir, _ := filepath.Rel(root, dir)
	if relDir == "" {
		relDir = "."
	}

	// Sort file keys for deterministic iteration — pkg.Files is map[string]*ast.File,
	// and Go map iteration order is randomized. Without sorting, RepoMap rendering
	// produces different output between turns, breaking DeepSeek's prefix cache.
	for _, pk := range pkgs {
		if strings.HasSuffix(pk.Name, "_test") {
			continue
		}
		name := pk.Name
		info := &PackageInfo{Name: name, Dir: relDir}
		sortedFiles := make([]string, 0, len(pk.Files))
		for fname := range pk.Files {
			sortedFiles = append(sortedFiles, fname)
		}
		sort.Strings(sortedFiles)
		for _, fname := range sortedFiles {
			extractDecls(pk.Files[fname], info)
		}
		return info, nil
	}
	return nil, nil
}

func extractDecls(file *ast.File, info *PackageInfo) {
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			extractGenDecl(d, info)
		case *ast.FuncDecl:
			extractFuncDecl(d, info)
		}
	}
}

func extractGenDecl(decl *ast.GenDecl, info *PackageInfo) {
	for _, spec := range decl.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			switch t := s.Type.(type) {
			case *ast.StructType:
				if s.Name.IsExported() {
					ti := TypeInfo{Name: s.Name.Name}
					if t.Fields != nil {
						for _, field := range t.Fields.List {
							typeName := formatExpr(field.Type)
							if len(field.Names) > 0 {
								for _, name := range field.Names {
									if name.IsExported() {
										ti.Fields = append(ti.Fields, FieldInfo{Name: name.Name, Type: typeName})
									}
								}
							}
						}
					}
					info.Types = append(info.Types, ti)
				}
			case *ast.InterfaceType:
				if s.Name.IsExported() {
					iface := InterfaceInfo{Name: s.Name.Name}
					if t.Methods != nil {
						for _, m := range t.Methods.List {
							if len(m.Names) > 0 {
								sig := m.Names[0].Name + formatFuncType(m.Type)
								iface.Methods = append(iface.Methods, sig)
							}
						}
					}
					info.Interfaces = append(info.Interfaces, iface)
				}
			}
		case *ast.ValueSpec:
			if decl.Tok == token.CONST {
				for _, name := range s.Names {
					if name.IsExported() {
						info.Consts = append(info.Consts, name.Name)
					}
				}
			} else if decl.Tok == token.VAR {
				for _, name := range s.Names {
					if name.IsExported() {
						info.Vars = append(info.Vars, name.Name)
					}
				}
			}
		}
	}
}

func extractFuncDecl(decl *ast.FuncDecl, info *PackageInfo) {
	if !decl.Name.IsExported() {
		return
	}
	fn := FuncInfo{Name: decl.Name.Name}
	if decl.Recv != nil && len(decl.Recv.List) > 0 {
		fn.Receiver = formatExpr(decl.Recv.List[0].Type)
	}
	if decl.Type.Params != nil {
		fn.Params = formatFieldList(decl.Type.Params)
	}
	if decl.Type.Results != nil {
		fn.Returns = formatFieldList(decl.Type.Results)
	}
	info.Functions = append(info.Functions, fn)
}

func formatFieldList(fl *ast.FieldList) string {
	if fl == nil || len(fl.List) == 0 {
		return ""
	}
	var parts []string
	for _, field := range fl.List {
		typeName := formatExpr(field.Type)
		if len(field.Names) > 0 {
			for _, name := range field.Names {
				parts = append(parts, name.Name+" "+typeName)
			}
		} else {
			parts = append(parts, typeName)
		}
	}
	return strings.Join(parts, ", ")
}

func formatFuncType(expr ast.Expr) string {
	ft, ok := expr.(*ast.FuncType)
	if !ok {
		return ""
	}
	params := formatFieldList(ft.Params)
	results := formatFieldList(ft.Results)
	if results == "" {
		return "(" + params + ")"
	}
	return "(" + params + ") " + results
}

func formatExpr(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return formatExpr(e.X) + "." + e.Sel.Name
	case *ast.StarExpr:
		return "*" + formatExpr(e.X)
	case *ast.ArrayType:
		return "[]" + formatExpr(e.Elt)
	case *ast.MapType:
		return "map[" + formatExpr(e.Key) + "]" + formatExpr(e.Value)
	case *ast.InterfaceType:
		return "interface{}"
	case *ast.FuncType:
		return "func" + formatFuncType(expr)
	case *ast.ChanType:
		return "chan " + formatExpr(e.Value)
	case *ast.Ellipsis:
		return "..." + formatExpr(e.Elt)
	default:
		return "any"
	}
}
