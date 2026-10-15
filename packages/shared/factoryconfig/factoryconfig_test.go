package factoryconfig

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseExampleFactoryYAML(t *testing.T) {
	raw, err := os.ReadFile("../../../.alpine/factory.yaml")
	require.NoError(t, err)
	c, err := Parse(raw)
	require.NoError(t, err)
	require.Equal(t, 100, c.Limits.TicketCaps().MaxTurns)
	require.Equal(t, 15, c.Limits.TicketCaps().MaxGateIterations)
	specs, err := c.GateSpecs()
	require.NoError(t, err)
	require.NotEmpty(t, specs)
	require.Equal(t, 4, c.BlastRadius([]string{"src/pages/home.tsx", "db/migrations/001.sql"}))
	require.Equal(t, 3, c.BlastRadius([]string{"src/server/auth.ts"}))
	require.Equal(t, 2, c.BlastRadius([]string{"src/components/ui/button.tsx"}))
	require.Equal(t, 1, c.BlastRadius([]string{"README.md"}))
	require.Equal(t, []string{".github/workflows/ci.yml"}, c.TouchesProtected([]string{".github/workflows/ci.yml", "src/x.ts"}))
}

func TestValidation(t *testing.T) {
	_, err := Parse([]byte("version: 1\nlimits:\n  max_turns: 5\n"))
	require.ErrorContains(t, err, "max_turns")
	_, err = Parse([]byte("version: 1\nmodels:\n  implementer: gpt-5\n  reviewer: gpt-5\n"))
	require.ErrorContains(t, err, "must differ")
	_, err = Parse([]byte("version: 1\nunknown_key: 1\n"))
	require.Error(t, err)
}

func TestGatesDecodedLoosely(t *testing.T) {
	c, err := Parse([]byte(`version: 1
gates:
  - id: dupes
    run: npx jscpd --absolute
    parser: jscpd
    report_file: r.json
  - id: visual
    routes: auto
    warn_only: true
gate_runner:
  image: alpine-gates:1
  anything: [1, 2]
`))
	require.NoError(t, err)
	specs, err := c.GateSpecs()
	require.NoError(t, err)
	require.Len(t, specs, 2)
	require.Equal(t, "dupes", specs[0].ID)
	require.NotZero(t, c.GateRunner.Kind)
	_, err = Parse([]byte("version: 1\ngates: {a: 1}\n"))
	require.Error(t, err)
	_, err = Parse([]byte("version: 1\ntypo_key: 1\n"))
	require.Error(t, err, "top-level keys stay strict")
}
