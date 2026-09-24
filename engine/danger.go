package engine

import (
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// dangerKind classifies why a bash command is dangerous.
type dangerKind int

const (
	dangerNone dangerKind = iota
	// dangerProject: destructive but legitimate with explicit user
	// confirmation — data loss is limited to the project/working tree.
	dangerProject
	// dangerSystem: irreversible (OS/disk destruction). Hard-blocked with no
	// confirmation path, because a mistake here cannot be undone.
	dangerSystem
)

// dangerVerdict is the outcome of judging a parsed command.
type dangerVerdict struct {
	kind   dangerKind
	reason string
}

// judgeDanger parses a shell command and returns the most severe danger found
// across every command it would execute — including commands inside pipelines,
// `&&` / `||` / `;` lists, subshells and command substitutions.
//
// Judgment is structural, not textual: it reads the parsed argument vectors, so
// a dangerous string appearing inside a quoted argument (`echo "rm -rf"`) or as
// a search pattern (`grep "drop table"`) is not a false positive, and flag
// order/spelling variants (`rm -r -f`, `rm --recursive --force`) are not
// bypasses.
func judgeDanger(cmd string) dangerVerdict {
	if strings.TrimSpace(cmd) == "" {
		return dangerVerdict{}
	}
	// syntax.NewParser defaults to LangBash.
	file, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err != nil {
		return judgeUnparsable(cmd)
	}

	worst := dangerVerdict{}
	consider := func(v dangerVerdict) {
		if v.kind > worst.kind {
			worst = v
		}
	}

	// Pipeline shape: a downloader piped straight into a shell interpreter.
	syntax.Walk(file, func(node syntax.Node) bool {
		bc, ok := node.(*syntax.BinaryCmd)
		if !ok || (bc.Op != syntax.Pipe && bc.Op != syntax.PipeAll) {
			return true
		}
		if isDownloader(firstCommandName(bc.X)) && isShellInterpreter(firstCommandName(bc.Y)) {
			consider(dangerVerdict{dangerProject, "pipe remote script to shell — arbitrary code execution"})
		}
		return true
	})

	syntax.Walk(file, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.CallExpr:
			consider(judgeCall(n))
		case *syntax.Stmt:
			if ce, ok := n.Cmd.(*syntax.CallExpr); ok {
				consider(judgeTruncateNoop(ce, n.Redirs))
			}
		case *syntax.Redirect:
			consider(judgeRedirect(n))
		case *syntax.FuncDecl:
			consider(judgeFuncDecl(n))
		}
		return true
	})

	return worst
}

// judgeUnparsable is the safety net for commands the parser rejects (bash
// extensions it does not support). It is deliberately conservative: it can only
// ask the user, never hard-block, and it matches on distinctive substrings
// rather than the full legacy pattern table.
func judgeUnparsable(cmd string) dangerVerdict {
	lowered := strings.ToLower(cmd)
	for _, pat := range []string{
		"rm -rf /", "rm -fr /", "rm --recursive /", "mkfs.", "dd if=/dev/", "dd of=/dev/", "> /dev/sd", ":(){ :|:",
	} {
		if strings.Contains(lowered, pat) {
			return dangerVerdict{dangerProject, "unparsable command matched a destructive pattern — confirm before running"}
		}
	}
	return dangerVerdict{}
}

// judgeCall judges one simple command by its resolved name and arguments.
func judgeCall(call *syntax.CallExpr) dangerVerdict {
	args := literalArgs(call)
	name, args := unwrap(args)
	if name == "" {
		return dangerVerdict{}
	}
	base := filepath.Base(name)

	switch base {
	case "rm":
		return judgeRm(args)
	case "dd":
		return judgeDd(args)
	case "chmod":
		return judgeChmod(args)
	case "shred":
		return dangerVerdict{dangerProject, "secure file deletion — irreversible"}
	case "truncate":
		if hasZeroSize(args) {
			return dangerVerdict{dangerProject, "zero-out file content — data loss"}
		}
	case "crontab":
		// -l only lists; anything else rewrites the user's crontab.
		if !hasFlag(args, "-l") {
			return dangerVerdict{dangerProject, "scheduled task modification — persistence risk"}
		}
	case "find":
		if hasFlag(args, "-delete") {
			return dangerVerdict{dangerProject, "find -delete — recursive deletion"}
		}
	case "git":
		return judgeGit(args)
	case "psql", "mysql", "mariadb", "sqlite3", "mongosh", "clickhouse-client":
		if stmt := destructiveSQL(args); stmt != "" {
			return dangerVerdict{dangerProject, "SQL " + stmt + " — database data loss"}
		}
	}
	if strings.HasPrefix(base, "mkfs") {
		return dangerVerdict{dangerSystem, "filesystem creation — data loss"}
	}
	return dangerVerdict{}
}

// judgeRm judges an rm invocation from its parsed flags and targets, so any
// spelling of "recursive + force" is recognized and only a root target is
// treated as irreversible.
func judgeRm(args []string) dangerVerdict {
	recursive, force, rootTarget, globAll := false, false, false, false
	for _, a := range args {
		switch {
		case a == "":
			continue
		case a == "--":
			continue
		case strings.HasPrefix(a, "--"):
			switch a {
			case "--recursive":
				recursive = true
			case "--force":
				force = true
			case "--no-preserve-root":
				// The caller explicitly opted into deleting from /.
				rootTarget = true
			}
		case strings.HasPrefix(a, "-") && len(a) > 1:
			for _, c := range a[1:] {
				switch c {
				case 'r', 'R':
					recursive = true
				case 'f':
					force = true
				}
			}
		default:
			if isRootPath(a) {
				rootTarget = true
			}
			if a == "*" {
				globAll = true
			}
		}
	}
	switch {
	case rootTarget && (recursive || force):
		return dangerVerdict{dangerSystem, "irreversible system-wide delete"}
	case recursive && force:
		return dangerVerdict{dangerProject, "recursive force delete — irreversible data loss"}
	case recursive:
		return dangerVerdict{dangerProject, "recursive delete — data loss"}
	case rootTarget:
		return dangerVerdict{dangerProject, "delete at the filesystem root — data loss"}
	case globAll:
		return dangerVerdict{dangerProject, "bulk delete of every entry in the working directory"}
	}
	return dangerVerdict{}
}

// judgeDd only treats writes to a raw device as dangerous. Reading a device
// (`dd if=/dev/sda of=/dev/null`) is harmless and must not be flagged.
func judgeDd(args []string) dangerVerdict {
	for _, a := range args {
		out, ok := strings.CutPrefix(a, "of=")
		if !ok {
			continue
		}
		if isRawDevice(out) {
			return dangerVerdict{dangerSystem, "raw disk write — data destruction"}
		}
	}
	return dangerVerdict{}
}

// judgeChmod flags world-writable permissions on the filesystem root or a
// recursive world-writable change. `chmod 777 /tmp/x` is a normal operation.
func judgeChmod(args []string) dangerVerdict {
	recursive, mode := false, ""
	var targets []string
	for _, a := range args {
		switch {
		case a == "":
			continue
		case strings.HasPrefix(a, "--"):
			if a == "--recursive" {
				recursive = true
			}
		case strings.HasPrefix(a, "-") && len(a) > 1:
			for _, c := range a[1:] {
				if c == 'R' {
					recursive = true
				}
			}
		case mode == "":
			mode = a
		default:
			targets = append(targets, a)
		}
	}
	if mode != "777" && !strings.Contains(mode, "777") {
		return dangerVerdict{}
	}
	for _, tgt := range targets {
		if isRootPath(tgt) {
			return dangerVerdict{dangerProject, "world-writable root directory"}
		}
	}
	if recursive {
		return dangerVerdict{dangerProject, "world-writable recursive permission change"}
	}
	return dangerVerdict{}
}

// judgeGit judges destructive git subcommands. `--force-with-lease` is the safe
// force-push variant and must not be flagged; `git branch -d` (merged-branch
// delete) is routine, only -D loses work.
func judgeGit(args []string) dangerVerdict {
	if len(args) == 0 {
		return dangerVerdict{}
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "push":
		for _, a := range rest {
			switch {
			case a == "--force-with-lease" || strings.HasPrefix(a, "--force-with-lease="):
				continue // safe variant
			case a == "--force" || a == "-f" || strings.HasPrefix(a, "--force="):
				return dangerVerdict{dangerProject, "force push — overwrites remote history"}
			}
		}
	case "reset":
		if hasFlag(rest, "--hard") {
			return dangerVerdict{dangerProject, "destructive git reset — loss of local changes"}
		}
	case "branch":
		if hasFlag(rest, "-D") || hasFlag(rest, "--delete --force") {
			return dangerVerdict{dangerProject, "force delete git branch — unmerged work lost"}
		}
	case "clean":
		force, removesDirs := false, false
		for _, a := range rest {
			switch {
			case a == "--force":
				force = true
			case strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--"):
				for _, c := range a[1:] {
					switch c {
					case 'f':
						force = true
					case 'd', 'x', 'X':
						removesDirs = true
					}
				}
			}
		}
		if force && removesDirs {
			return dangerVerdict{dangerProject, "git clean — removes untracked files irreversibly"}
		}
	}
	return dangerVerdict{}
}

// judgeTruncateNoop detects `: > file` / `:> file` — the no-op command `:`
// combined with a write redirection, whose only purpose is truncating a file to
// zero bytes. A plain `echo x > file` is a normal write and is not flagged.
func judgeTruncateNoop(call *syntax.CallExpr, redirs []*syntax.Redirect) dangerVerdict {
	if len(call.Args) != 1 {
		return dangerVerdict{}
	}
	name, ok := wordLiteral(call.Args[0])
	if !ok || name != ":" {
		return dangerVerdict{}
	}
	for _, r := range redirs {
		switch r.Op {
		case syntax.RdrOut, syntax.ClbOut:
			return dangerVerdict{dangerProject, "truncate file — data loss"}
		}
	}
	return dangerVerdict{}
}

// judgeRedirect judges write-redirections (`>`, `>>`, `>|`, `&>`) by target.
func judgeRedirect(r *syntax.Redirect) dangerVerdict {
	switch r.Op {
	case syntax.RdrOut, syntax.AppOut, syntax.ClbOut, syntax.RdrAll, syntax.AppAll:
	default:
		return dangerVerdict{}
	}
	target, ok := wordLiteral(r.Word)
	if !ok {
		return dangerVerdict{}
	}
	switch {
	case isRawDevice(target):
		return dangerVerdict{dangerSystem, "raw disk write — data destruction"}
	case strings.HasPrefix(target, "/etc/"):
		return dangerVerdict{dangerProject, "overwrite system configuration file"}
	case strings.HasPrefix(target, "/dev/tcp/"), strings.HasPrefix(target, "/dev/udp/"):
		return dangerVerdict{dangerProject, "network redirect — data exfiltration risk"}
	}
	return dangerVerdict{}
}

// judgeFuncDecl detects a fork bomb: a shell function that pipes its own name
// into itself (`:(){ :|:& };:`).
func judgeFuncDecl(fd *syntax.FuncDecl) dangerVerdict {
	if fd.Name == nil || fd.Name.Value == "" {
		return dangerVerdict{}
	}
	name := fd.Name.Value
	bomb := false
	syntax.Walk(fd.Body, func(node syntax.Node) bool {
		bc, ok := node.(*syntax.BinaryCmd)
		if !ok || (bc.Op != syntax.Pipe && bc.Op != syntax.PipeAll) {
			return true
		}
		if firstCommandName(bc.X) == name && firstCommandName(bc.Y) == name {
			bomb = true
		}
		return true
	})
	if bomb {
		return dangerVerdict{dangerSystem, "fork bomb — system crash"}
	}
	return dangerVerdict{}
}

// unwrap strips privilege and launcher prefixes so the wrapped command is
// judged instead of the wrapper: `sudo rm -rf /` is judged as `rm -rf /`.
func unwrap(args []string) (string, []string) {
	for len(args) > 0 {
		base := filepath.Base(args[0])
		switch base {
		case "sudo", "doas":
			args = args[1:]
			for len(args) > 0 && strings.HasPrefix(args[0], "-") {
				flag := args[0]
				args = args[1:]
				if sudoFlagTakesValue(flag) && len(args) > 0 {
					args = args[1:]
				}
			}
			continue
		case "env", "nohup", "command", "builtin", "time":
			args = args[1:]
			// env VAR=value cmd — skip the environment assignments.
			for len(args) > 0 && !strings.HasPrefix(args[0], "-") && strings.Contains(args[0], "=") {
				args = args[1:]
			}
			continue
		}
		return args[0], args[1:]
	}
	return "", nil
}

func sudoFlagTakesValue(flag string) bool {
	switch flag {
	case "-u", "--user", "-g", "--group", "-p", "--prompt", "-C", "--close-from", "-h", "--host", "-r", "--role", "-t", "--type", "-U", "--other-user":
		return true
	}
	return false
}

// literalArgs returns the command's argument vector. Words that cannot be
// resolved statically become "" so the rest of the command is still judged —
// `rm -rf "$DIR"` is still recognized as a recursive force delete.
func literalArgs(call *syntax.CallExpr) []string {
	args := make([]string, 0, len(call.Args))
	for _, w := range call.Args {
		s, _ := wordLiteral(w)
		args = append(args, s)
	}
	return args
}

// wordLiteral resolves a word to its literal string. ok is false when the word
// contains an expansion (parameter, command/arithmetic substitution, process
// substitution) that cannot be resolved without running a shell. Globs are
// literal here — they are compared as patterns, which is what the callers want
// (`rm -rf /*`).
func wordLiteral(w *syntax.Word) (string, bool) {
	if w == nil {
		return "", false
	}
	var b strings.Builder
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			b.WriteString(p.Value)
		case *syntax.SglQuoted:
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, dp := range p.Parts {
				lit, ok := dp.(*syntax.Lit)
				if !ok {
					return "", false
				}
				b.WriteString(lit.Value)
			}
		default:
			return "", false
		}
	}
	return b.String(), true
}

// firstCommandName returns the name of the leftmost simple command inside node,
// or "" when it cannot be resolved statically.
func firstCommandName(node syntax.Node) string {
	name := ""
	syntax.Walk(node, func(n syntax.Node) bool {
		if name != "" {
			return false
		}
		ce, ok := n.(*syntax.CallExpr)
		if !ok || len(ce.Args) == 0 {
			return true
		}
		if s, ok := wordLiteral(ce.Args[0]); ok {
			name = filepath.Base(s)
		}
		return false
	})
	return name
}

// isRootPath reports whether a path targets the filesystem root (including the
// glob forms `/` and `/*`).
func isRootPath(p string) bool {
	if p == "" {
		return false
	}
	if p == "/" || p == "/*" || p == "//" {
		return true
	}
	if strings.HasPrefix(p, "/*") {
		return true
	}
	return filepath.Clean(p) == "/"
}

// isRawDevice reports whether a path is a raw block device.
func isRawDevice(p string) bool {
	rest, ok := strings.CutPrefix(p, "/dev/")
	if !ok {
		return false
	}
	for _, pfx := range []string{"sd", "hd", "vd", "nvme", "disk", "mmcblk", "loop", "sr", "nbd", "dm-", "rdisk"} {
		if strings.HasPrefix(rest, pfx) {
			return true
		}
	}
	return false
}

func isDownloader(name string) bool {
	switch name {
	case "curl", "wget":
		return true
	}
	return false
}

func isShellInterpreter(name string) bool {
	switch name {
	case "sh", "bash", "zsh", "dash", "ksh", "ash", "fish":
		return true
	}
	return false
}

// hasFlag reports whether the exact flag appears in args.
func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// hasZeroSize reports whether a truncate invocation zeroes the file, in any of
// its spellings (`-s 0`, `-s0`, `--size=0`, `--size 0`).
func hasZeroSize(args []string) bool {
	for i, a := range args {
		switch {
		case a == "--size=0", a == "-s0", a == "-s=0":
			return true
		case a == "-s", a == "--size":
			if i+1 < len(args) && args[i+1] == "0" {
				return true
			}
		}
	}
	return false
}

// destructiveSQL reports the destructive statement found in a SQL client's
// arguments, or "" if none. Restricting the scan to SQL clients keeps
// `grep "drop table" migrations/` from being flagged.
func destructiveSQL(args []string) string {
	for _, a := range args {
		lowered := strings.ToLower(a)
		for _, stmt := range []string{"drop table", "drop database", "drop schema", "truncate table"} {
			if strings.Contains(lowered, stmt) {
				return strings.ToUpper(stmt)
			}
		}
	}
	return ""
}
