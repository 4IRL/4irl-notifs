package keyrotation

import (
	"context"
	"errors"
	"testing"
)

func TestVerifyRejectsNilSealer(testInstance *testing.T) {
	_, verifyErr := Verify(context.Background(), nil, nil, nil)
	if verifyErr == nil {
		testInstance.Fatal("Verify with a nil sealer returned nil, want an error")
	}
	if errors.Is(verifyErr, ErrIncomplete) {
		testInstance.Fatalf("nil sealer refusal must not be ErrIncomplete: %v", verifyErr)
	}
}
