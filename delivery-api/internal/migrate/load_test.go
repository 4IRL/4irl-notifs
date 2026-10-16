package migrate

import (
	"reflect"
	"testing"
	"testing/fstest"
)

func TestLoad(testInstance *testing.T) {
	testCases := []struct {
		name    string
		files   fstest.MapFS
		want    []Migration
		wantErr bool
	}{
		{
			name: "sorted by version ascending",
			files: fstest.MapFS{
				"0002_add_things.sql": {Data: []byte("SELECT 2;")},
				"0001_baseline.sql":   {Data: []byte("SELECT 1;")},
				"0010_later.sql":      {Data: []byte("SELECT 10;")},
			},
			want: []Migration{
				{Version: 1, Name: "baseline", SQL: "SELECT 1;"},
				{Version: 2, Name: "add_things", SQL: "SELECT 2;"},
				{Version: 10, Name: "later", SQL: "SELECT 10;"},
			},
		},
		{
			name: "non-sql files are ignored",
			files: fstest.MapFS{
				"README":            {Data: []byte("docs")},
				"notes.txt":         {Data: []byte("notes")},
				"0001_baseline.sql": {Data: []byte("SELECT 1;")},
			},
			want: []Migration{{Version: 1, Name: "baseline", SQL: "SELECT 1;"}},
		},
		{
			name:    "version without four digits is malformed",
			files:   fstest.MapFS{"1_x.sql": {Data: []byte("SELECT 1;")}},
			wantErr: true,
		},
		{
			name:    "dash separator is malformed",
			files:   fstest.MapFS{"0001-x.sql": {Data: []byte("SELECT 1;")}},
			wantErr: true,
		},
		{
			name:    "uppercase name is malformed",
			files:   fstest.MapFS{"0001_Baseline.sql": {Data: []byte("SELECT 1;")}},
			wantErr: true,
		},
		{
			name: "duplicate version",
			files: fstest.MapFS{
				"0001_a.sql": {Data: []byte("SELECT 1;")},
				"0001_b.sql": {Data: []byte("SELECT 2;")},
			},
			wantErr: true,
		},
		{
			name:  "empty filesystem",
			files: fstest.MapFS{},
			want:  []Migration{},
		},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			got, err := Load(testCase.files)

			if testCase.wantErr {
				if err == nil {
					subTest.Fatalf("Load error = nil, want an error; got %+v", got)
				}
				return
			}
			if err != nil {
				subTest.Fatalf("Load error = %v, want nil", err)
			}
			if got == nil {
				subTest.Fatal("Load returned a nil slice, want a non-nil empty slice")
			}
			if !reflect.DeepEqual(got, testCase.want) {
				subTest.Fatalf("Load = %+v, want %+v", got, testCase.want)
			}
		})
	}
}

func TestExpectedVersion(testInstance *testing.T) {
	testCases := []struct {
		name       string
		migrations []Migration
		want       int
	}{
		{name: "nil", migrations: nil, want: 0},
		{name: "empty", migrations: []Migration{}, want: 0},
		{name: "highest wins", migrations: []Migration{{Version: 1}, {Version: 3}, {Version: 2}}, want: 3},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			if got := ExpectedVersion(testCase.migrations); got != testCase.want {
				subTest.Fatalf("ExpectedVersion = %d, want %d", got, testCase.want)
			}
		})
	}
}
