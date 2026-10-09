package vimedit

import "slices"

// maxMacroDepth stops a macro that plays itself, since @a inside register a
// would otherwise never end.
const maxMacroDepth = 100

// Recording is the register being recorded into with q, or 0. The footer
// shows it as "recording @a".
func (e *Editor) Recording() rune { return e.macroName }

// stopsMacro is the q that ends a recording. That q is not part of the macro,
// so it never reaches the rest of the key handling.
func (e *Editor) stopsMacro(key, text string) bool {
	if e.macroName == 0 || e.macroDepth > 0 || !e.idle() || (e.mode != Normal && !e.isVisual()) {
		return false
	}
	if tok := keyToken(text, key); tok != "q" {
		return false
	}
	e.macros[e.macroName] = slices.Clone(e.macroKeys)
	e.macroName, e.macroKeys = 0, nil
	return true
}

func keyToken(text, key string) string {
	if text != "" {
		return text
	}
	return key
}

// startMacro is q{a-z}. Recording again into a register replaces it, and an
// empty recording empties it, as in Vim.
func (e *Editor) startMacro(name rune) {
	e.macroName, e.macroKeys = name, nil
	e.clearPending()
}

// playMacro is @{a-z} and @@. A count plays it that many times, stopping at
// the first motion that fails, which is how a recursive macro ends.
func (e *Editor) playMacro(char rune) {
	times := max(e.countN(), 1)
	e.clearPending()
	e.recording, e.rec = false, nil // '.' repeats what the macro changed, not the @
	name := char
	if char == '@' {
		name = e.lastMacro
		if name == 0 {
			e.status = "No macro has been played yet"
			return
		}
	}
	keys, ok := e.macros[name]
	if !ok {
		e.status = "Nothing recorded in @" + string(name)
		return
	}
	if e.macroDepth == 0 {
		e.macroAbort = false
	}
	if e.macroDepth >= maxMacroDepth {
		e.macroAbort = true
		e.status = "Macro @" + string(name) + " calls itself too deeply"
		return
	}
	e.lastMacro = name
	e.macroDepth++
	defer func() { e.macroDepth-- }()
	for range times {
		for _, event := range keys {
			e.press(event.key, event.text)
			if e.macroAbort || e.res.Action != None {
				return
			}
		}
	}
}
