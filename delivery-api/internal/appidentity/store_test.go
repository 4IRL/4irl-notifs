package appidentity

import "testing"

func TestBindingMatchesRequiresAllFiveFieldsExactly(testInstance *testing.T) {
	binding := Binding{
		AppID: "urls4irl", Repository: "4IRL/urls4irl", RepositoryID: 12345, RepositoryOwnerID: 678,
		WorkflowPath: ".github/workflows/prod-deploy.yml", Ref: "refs/heads/main", Environment: "production",
	}
	matching := Claims{
		Repository: "4IRL/urls4irl", RepositoryID: 12345, RepositoryOwnerID: 678,
		WorkflowPath: ".github/workflows/prod-deploy.yml", Ref: "refs/heads/main", Environment: "production",
		RunID: "1", JTI: "j",
	}
	if !binding.Matches(matching) {
		testInstance.Fatal("identical claims must match")
	}

	testCases := []struct {
		label  string
		mutate func(*Claims)
	}{
		{"another repository id", func(claims *Claims) { claims.RepositoryID = 99 }},
		{"another owner id", func(claims *Claims) { claims.RepositoryOwnerID = 99 }},
		{"another workflow", func(claims *Claims) { claims.WorkflowPath = ".github/workflows/evil.yml" }},
		{"another ref", func(claims *Claims) { claims.Ref = "refs/heads/feature" }},
		{"another environment", func(claims *Claims) { claims.Environment = "staging" }},
		{"no environment where one was approved", func(claims *Claims) { claims.Environment = "" }},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.label, func(subTest *testing.T) {
			claims := matching
			testCase.mutate(&claims)
			if binding.Matches(claims) {
				subTest.Errorf("claims %+v must not match the binding", claims)
			}
		})
	}

	testInstance.Run("an environment where none was approved", func(subTest *testing.T) {
		unscoped := binding
		unscoped.Environment = ""
		if unscoped.Matches(matching) {
			subTest.Error("a binding with no environment must not match a claim that has one")
		}
		claims := matching
		claims.Environment = ""
		if !unscoped.Matches(claims) {
			subTest.Error("a binding with no environment must match a claim with none")
		}
	})

	testInstance.Run("the display-only repository name is not compared", func(subTest *testing.T) {
		claims := matching
		claims.Repository = "4IRL/renamed"
		if !binding.Matches(claims) {
			subTest.Error("a renamed repository keeps matching by id")
		}
	})
}
