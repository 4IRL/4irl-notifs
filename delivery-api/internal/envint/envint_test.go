package envint

import "testing"

func TestBounded(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    int
		wantErr bool
	}{
		{name: "empty gives fallback", raw: "", want: 7},
		{name: "lower bound", raw: "1", want: 1},
		{name: "upper bound", raw: "10", want: 10},
		{name: "zero rejected", raw: "0", wantErr: true},
		{name: "over limit rejected", raw: "11", wantErr: true},
		{name: "non-numeric rejected", raw: "abc", wantErr: true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := Bounded(func(string) string { return testCase.raw }, "X_VAR", 7, 10)
			if testCase.wantErr {
				if err == nil || err.Error() != "invalid X_VAR" {
					t.Fatalf("err = %v, want invalid X_VAR", err)
				}
				return
			}
			if err != nil || got != testCase.want {
				t.Fatalf("got (%d, %v), want %d", got, err, testCase.want)
			}
		})
	}
}
