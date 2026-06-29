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

// zhFromLang maps a Handoff.UserLanguage value ("中文" when Chinese) to a bool.
func zhFromLang(userLang string) bool {
	return userLang == "中文"
}
