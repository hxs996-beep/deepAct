package context

import "embed"

//go:embed langpacks/*.md
var langpackFS embed.FS

func GetLangPack(lang Language) string {
	name := "generic"
	switch lang {
	case LangGo:
		name = "go"
	case LangTypeScript:
		name = "typescript"
	case LangPython:
		name = "python"
	case LangRust:
		name = "rust"
	case LangJava:
		name = "java"
	case LangGeneric:
		name = "generic"
	}
	data, err := langpackFS.ReadFile("langpacks/" + name + ".md")
	if err != nil {
		return ""
	}
	return string(data)
}
