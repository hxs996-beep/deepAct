package engine

// pickPrompt returns the Chinese prompt when zh is true, otherwise the English
// prompt. Used at every prompt-construction site to select between the two
// language variants based on the session-locked language flag.
func pickPrompt(zh bool, en, zhPrompt string) string {
	if zh {
		return zhPrompt
	}
	return en
}

// UserLanguage is the language the user writes in, locked from the first user
// message of the session.
//
// It is a typed value rather than a bare string so that "not determined yet"
// can never be confused with a real language, and so that English is an
// explicit value. The previous convention used "" for English, which was
// indistinguishable from "unknown" and made the system prompt skippable for
// every non-Chinese session.
type UserLanguage string

const (
	// LangUnset is the zero value: no language has been determined yet.
	// ContextAssembler only reaches it before the first user message arrives.
	LangUnset    UserLanguage = ""
	LangChinese  UserLanguage = "中文"
	LangEnglish  UserLanguage = "English"
	LangJapanese UserLanguage = "日本語"
)

// IsChinese reports whether the session language is Chinese. Every localized
// branch must go through this (or UserLanguageFor) instead of comparing
// against a literal.
func (l UserLanguage) IsChinese() bool {
	return l == LangChinese
}

// UserLanguageFor returns the session language for a Chinese/non-Chinese flag.
func UserLanguageFor(zh bool) UserLanguage {
	if zh {
		return LangChinese
	}
	return LangEnglish
}

// zhFromLang maps a session language to the Chinese/English prompt choice.
func zhFromLang(userLang UserLanguage) bool {
	return userLang.IsChinese()
}
