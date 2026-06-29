package context

import "embed"

//go:embed langpacks/*.md langpacks/zh/*.md
var langpackFS embed.FS

// GetLangPack returns the language pack for the given project language.
// If userLang is "中文", returns the Chinese version; otherwise returns English.
func GetLangPack(lang Language, userLang string) string {
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
	// Try Chinese version first
	if userLang == "中文" {
		data, err := langpackFS.ReadFile("langpacks/zh/" + name + ".md")
		if err == nil {
			return string(data)
		}
		// Fall back to English if Chinese version not available
	}
	data, err := langpackFS.ReadFile("langpacks/" + name + ".md")
	if err != nil {
		return ""
	}
	return string(data)
}
