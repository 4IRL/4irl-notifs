package registry

import (
	"reflect"
	"strings"
	"testing"
)

func TestMaskSecrets(t *testing.T) {
	tests := []struct {
		name  string
		input map[string]string
		want  map[string]string
	}{
		{
			name:  "webhook_url keeps host only",
			input: map[string]string{"webhook_url": "https://discord.com/api/webhooks/123/tokenvalue"},
			want:  map[string]string{"webhook_url": "https://discord.com/****"},
		},
		{
			name:  "url keeps host only",
			input: map[string]string{"url": "https://example.com:8443/hook?key=abc"},
			want:  map[string]string{"url": "https://example.com:8443/****"},
		},
		{
			name:  "userinfo dropped",
			input: map[string]string{"url": "https://user:pw@example.com/hook"},
			want:  map[string]string{"url": "https://example.com/****"},
		},
		{
			name:  "other keys fully masked",
			input: map[string]string{"access_token": "tk_abcdefghijklmnopqrstuvwxyz012", "signing_secret": "supersecretsupersecretsupersecret!"},
			want:  map[string]string{"access_token": "****", "signing_secret": "****"},
		},
		{
			name:  "previous signing secret fully masked",
			input: map[string]string{"signing_secret": "supersecretsupersecretsupersecret!", "signing_secret_previous": "previoussecretprevioussecretprev!"},
			want:  map[string]string{"signing_secret": "****", "signing_secret_previous": "****"},
		},
		{
			name:  "mixed",
			input: map[string]string{"url": "https://example.com/hook", "signing_secret": "x"},
			want:  map[string]string{"url": "https://example.com/****", "signing_secret": "****"},
		},
		{
			name:  "unparseable url fully masked",
			input: map[string]string{"url": "https://exa mple.com/%zz"},
			want:  map[string]string{"url": "****"},
		},
		{
			name:  "url without host fully masked",
			input: map[string]string{"webhook_url": "not a url"},
			want:  map[string]string{"webhook_url": "****"},
		},
		{
			name:  "empty value fully masked",
			input: map[string]string{"access_token": ""},
			want:  map[string]string{"access_token": "****"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := MaskSecrets(test.input)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("MaskSecrets = %v, want %v", got, test.want)
			}
		})
	}
}

func TestMaskSecretsEmptyInputReturnsEmptyNonNilMap(t *testing.T) {
	for name, input := range map[string]map[string]string{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			got := MaskSecrets(input)
			if got == nil {
				t.Fatal("MaskSecrets returned nil, want empty non-nil map")
			}
			if len(got) != 0 {
				t.Fatalf("MaskSecrets = %v, want empty", got)
			}
		})
	}
}

func TestMaskSecretsDoesNotMutateInput(t *testing.T) {
	input := map[string]string{"url": "https://example.com/hook", "signing_secret": "s"}
	MaskSecrets(input)
	if input["url"] != "https://example.com/hook" || input["signing_secret"] != "s" {
		t.Fatalf("input mutated: %v", input)
	}
}

// TestMaskSecretsLeaksNothingAfterHost asserts that no masked value contains
// any run of three or more characters of the input after the host (path,
// query, fragment and userinfo for URLs, the whole value for every other key),
// and that a URL's masked remainder is exactly "/****".
func TestMaskSecretsLeaksNothingAfterHost(t *testing.T) {
	const host = "example.com"
	// maskFrame is the part of a masked URL that legitimately repeats input.
	const maskFrame = "https://" + host + "/****"
	hiddenURLTails := map[string]string{
		"webhook_url": "user:userpw@" + host + "/api/webhooks/987654321/SECRETTOKENabc?token=QUERYSECRET#FRAGSECRET",
		"url":         "user:userpw@" + host + "/hooks/SECRETPATH?sig=QUERYSECRET#FRAGSECRET",
	}
	input := map[string]string{
		"access_token":   "tk_abcdefghijklmnopqrstuvwxyz012",
		"signing_secret": "SIGNINGSECRETvalue0123456789abcdef!",
	}
	hiddenValues := map[string]string{}
	for key, value := range input {
		hiddenValues[key] = value
	}
	for key, tail := range hiddenURLTails {
		input[key] = "https://" + tail
		hiddenValues[key] = strings.TrimPrefix(tail, "user:userpw@"+host)
	}

	output := MaskSecrets(input)

	for key, tail := range hiddenURLTails {
		if want := "https://" + host + "/****"; output[key] != want {
			t.Errorf("masked %s = %q, want %q", key, output[key], want)
		}
		// userinfo is also hidden, so check it separately from the remainder.
		hiddenValues[key] += " user:userpw " + tail
	}
	for key, hidden := range hiddenValues {
		masked := output[key]
		for start := 0; start+3 <= len(hidden); start++ {
			window := hidden[start : start+3]
			// The mask itself legitimately contains "://", the host and "/**".
			if strings.Contains(maskFrame, window) {
				continue
			}
			if strings.Contains(masked, window) {
				t.Errorf("masked %s = %q leaks %q", key, masked, window)
			}
		}
	}
}

func TestMaskSecretsAlwaysEmitsHTTPS(t *testing.T) {
	got := MaskSecrets(map[string]string{"url": "http://example.com/hook"})
	if want := "https://example.com/****"; got["url"] != want {
		t.Fatalf("masked url = %q, want %q", got["url"], want)
	}
}
