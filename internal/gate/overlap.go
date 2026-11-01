package gate

import (
	"path"
	"path/filepath"
	"strings"
)

// PathsOverlap reports whether two touch sets could modify the same file.
// Directory prefixes and globs are both considered; the check is deliberately
// conservative so the scheduler never runs two conflicting cards at once.
func PathsOverlap(touchesA, touchesB []string) bool {
	for _, a := range touchesA {
		for _, b := range touchesB {
			if pathsOverlap(a, b) {
				return true
			}
		}
	}
	return false
}

func pathsOverlap(a, b string) bool {
	x, y := norm(a), norm(b)
	if x == "" || y == "" {
		return false
	}
	if x == y {
		return true
	}
	if hasGlob(x) || hasGlob(y) {
		if globMatch(x, y) || globMatch(y, x) {
			return true
		}
		// A glob restriction such as internal/*.go conflicts with anything in
		// the same static directory, including deeper paths.
		return segmentPrefix(staticDir(x), staticDir(y)) || segmentPrefix(staticDir(y), staticDir(x))
	}
	return segmentPrefix(x, y) || segmentPrefix(y, x)
}

// CheckWorktreeViolations returns changed files outside allowedTouches.
// AGENTS.md is always allowed.
func CheckWorktreeViolations(changedFiles, allowedTouches []string) []string {
	var violations []string
	for _, f := range changedFiles {
		file := norm(f)
		if file == "" || file == "AGENTS.md" {
			continue
		}
		allowed := false
		for _, t := range allowedTouches {
			if pathAllowed(file, t) {
				allowed = true
				break
			}
		}
		if !allowed {
			violations = append(violations, f)
		}
	}
	return violations
}

func pathAllowed(file, touch string) bool {
	t := norm(touch)
	if t == "" {
		return false
	}
	if hasGlob(t) {
		return strictGlob(t, file)
	}
	return file == t || strings.HasPrefix(file, t+"/")
}

func norm(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = filepath.ToSlash(p)
	p = strings.TrimPrefix(p, "./")
	p = path.Clean(p)
	p = strings.TrimSuffix(p, "/")
	if p == "." {
		return ""
	}
	return p
}

func hasGlob(p string) bool { return strings.ContainsAny(p, "*?[") }

func staticDir(p string) string {
	parts := strings.Split(p, "/")
	var fixed []string
	for _, part := range parts {
		if strings.ContainsAny(part, "*?[") {
			break
		}
		fixed = append(fixed, part)
	}
	return strings.Join(fixed, "/")
}

// segmentPrefix reports a path-segment prefix relationship.
func segmentPrefix(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// strictGlob matches a touch glob against a concrete file path.
func strictGlob(pattern, name string) bool {
	if strings.HasSuffix(pattern, "/**") {
		base := strings.TrimSuffix(pattern, "/**")
		return name == base || strings.HasPrefix(name, base+"/")
	}
	if strings.Contains(pattern, "**/") {
		pattern = strings.ReplaceAll(pattern, "**/", "")
	}
	ok, err := path.Match(pattern, name)
	return err == nil && ok
}

// globMatch matches a slash path against a glob. ** is treated as a single
// greedy segment so patterns like internal/** work as directory prefixes.
func globMatch(pattern, name string) bool {
	if pattern == "" || name == "" {
		return false
	}
	if strings.HasSuffix(pattern, "/**") {
		base := strings.TrimSuffix(pattern, "/**")
		return name == base || strings.HasPrefix(name, base+"/")
	}
	pattern = strings.ReplaceAll(pattern, "**/", "")
	if ok, err := path.Match(pattern, name); err == nil && ok {
		return true
	}
	// path.Match's * does not cross separators; also try the leading directory
	// prefix so internal/*.go covers nested files under internal/.
	if base := staticDir(pattern); base != "" && base != pattern {
		return name == base || strings.HasPrefix(name, base+"/")
	}
	return false
}
