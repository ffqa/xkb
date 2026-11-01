// Package gate holds the zero-token mechanical checks: card admission and
// path conflict detection.
package gate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xpier/xkb/internal/store"
)

// ValidateTodoGate mechanically checks a card before it may enter todo.
func ValidateTodoGate(card *store.Card) error {
	if card == nil {
		return errors.New("card is nil")
	}
	switch card.Priority {
	case "P0", "P1", "P2", "P3", "P4":
	default:
		return fmt.Errorf("invalid priority %q (want P0..P4)", card.Priority)
	}
	switch card.Size {
	case "small", "large":
	default:
		return fmt.Errorf("invalid size %q (want small|large)", card.Size)
	}
	info, err := os.Stat(card.ProjectPath)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("project path %q is not an existing directory", card.ProjectPath)
	}
	if len(card.Touches) == 0 {
		return errors.New("touches must not be empty")
	}
	for _, t := range card.Touches {
		if err := validateTouch(t); err != nil {
			return err
		}
	}
	if strings.TrimSpace(card.VerifyCmd) == "" {
		return errors.New("verify_cmd must not be empty")
	}
	for name, value := range map[string]string{
		"goal":         card.Goal,
		"out_of_scope": card.OutOfScope,
		"acceptance":   card.Acceptance,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s must not be empty", name)
		}
		upper := strings.ToUpper(value)
		if strings.Contains(upper, "<FILL_IN>") || strings.Contains(upper, "TODO") {
			return fmt.Errorf("%s still contains a placeholder", name)
		}
	}
	return nil
}

func validateTouch(touch string) error {
	t := strings.TrimSpace(touch)
	if t == "" {
		return errors.New("touches contains an empty path")
	}
	slashed := filepath.ToSlash(t)
	if strings.HasPrefix(slashed, "/") || filepath.IsAbs(t) {
		return fmt.Errorf("touch %q must be a relative path", touch)
	}
	for _, seg := range strings.Split(slashed, "/") {
		if seg == ".." {
			return fmt.Errorf("touch %q must not contain ..", touch)
		}
	}
	if filepath.Clean(t) == "." {
		return fmt.Errorf("touch %q resolves to the repository root", touch)
	}
	return nil
}
