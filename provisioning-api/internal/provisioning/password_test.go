package provisioning

import (
	"regexp"
	"testing"
)

func TestGenerateRandomPasswordProducesUniqueHexSecrets(t *testing.T) {
	hexPattern := regexp.MustCompile(`^[0-9a-f]{64}$`)

	firstPassword, firstErr := GenerateRandomPassword()
	if firstErr != nil {
		t.Fatalf("GenerateRandomPassword returned unexpected error: %v", firstErr)
	}
	secondPassword, secondErr := GenerateRandomPassword()
	if secondErr != nil {
		t.Fatalf("GenerateRandomPassword returned unexpected error: %v", secondErr)
	}

	if !hexPattern.MatchString(firstPassword) {
		t.Fatalf("password %q is not 64 hex chars (32 bytes of entropy)", firstPassword)
	}
	if firstPassword == secondPassword {
		t.Fatal("two generated passwords were identical — not random")
	}
}
