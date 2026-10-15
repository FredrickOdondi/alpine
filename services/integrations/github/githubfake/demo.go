package githubfake

// DemoRepos are three repos covering the repo picker states: a valid factory config, none (the
// starter config path) and an invalid one.
func DemoRepos(user string) []Repo {
	return []Repo{
		{ID: 501, FullName: user + "/checkout-service", Private: true, DefaultBranch: "main", Files: map[string]string{
			"go.mod": "module example.com/checkout\n\ngo 1.22\n",
			".alpine/factory.yaml": `version: 1
limits:
  max_turns: 20
  max_gate_iterations: 3
  max_cost_usd: 1.5
gates:
  - id: unit
    parser: go-test
    run: go test -json ./...
merge_policy:
  BR1: human
  BR2: human
  BR3: human
  BR4: human
`}},
		{ID: 502, FullName: user + "/web-storefront", Private: false, DefaultBranch: "main", Files: map[string]string{
			"package.json": `{"name":"storefront","scripts":{"test":"vitest run"}}`}},
		{ID: 503, FullName: user + "/legacy-billing", Private: true, DefaultBranch: "master", Files: map[string]string{
			".alpine/factory.yaml": "version: 7\ngates: not-a-list\n"}},
	}
}
