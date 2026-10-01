package rpc

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/juex-ai/juex/internal/foundation/application"
)

func TestValidationReasonsPreserveBusinessErrorsOnly(t *testing.T) {
	original := &application.ValidationError{Reason: "fact needs an explicit person entity"}
	restored := DecodeError(ErrorCode(original))
	if !errors.Is(restored, application.ErrInvalid) || restored.Error() != original.Error() {
		t.Fatal(restored)
	}
	if code := ErrorCode(errors.New("database connection contains a password")); code != "unavailable" {
		t.Fatal("internal diagnostic escaped", code)
	}
	bounded := ErrorCode(&application.ValidationError{Reason: strings.Repeat("理由", 1000)})
	if !utf8.ValidString(bounded) || len(bounded) > len("invalid:")+1024 {
		t.Fatal("unbounded invalid UTF-8")
	}
}
