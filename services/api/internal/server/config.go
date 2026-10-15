package server

import (
	"encoding/json"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"
	"gopkg.in/yaml.v3"

	"github.com/alpine-dev/alpine/packages/shared/factoryconfig"
	"github.com/alpine-dev/alpine/packages/shared/ticketrepo"
	"github.com/alpine-dev/alpine/services/api/internal/apigen"
)

func dateOf(t time.Time) openapi_types.Date { return openapi_types.Date{Time: t} }

// factoryConfig renders a stored factory.yaml: the raw text, a snake_case parse mirroring the YAML
// (what the repo config page shows), and validation errors from factoryconfig.Parse.
func factoryConfig(row ticketrepo.FactoryConfigRow) apigen.FactoryConfig {
	out := apigen.FactoryConfig{RepoId: row.RepoID, Path: ".alpine/factory.yaml", UpdatedAt: row.CreatedAt}
	if row.CommitSHA != nil {
		out.Sha = *row.CommitSHA
	}
	if row.RawYAML != nil {
		out.RawYaml = *row.RawYAML
	}
	out.Validation.Errors = []string{}
	if _, err := factoryconfig.Parse([]byte(out.RawYaml)); err != nil {
		out.Validation.Errors = append(out.Validation.Errors, err.Error())
	}
	out.Validation.Valid = len(out.Validation.Errors) == 0

	var generic map[string]any
	if err := yaml.Unmarshal([]byte(out.RawYaml), &generic); err == nil && generic != nil {
		// models: a name or a chain -> "primary, fallback"
		if models, ok := generic["models"].(map[string]any); ok {
			flat := map[string]any{}
			for k, v := range models {
				switch m := v.(type) {
				case []any:
					parts := make([]string, 0, len(m))
					for _, x := range m {
						if s, ok := x.(string); ok {
							parts = append(parts, s)
						}
					}
					flat[k] = strings.Join(parts, ", ")
				default:
					flat[k] = v
				}
			}
			generic["models"] = flat
		}
		if b, err := json.Marshal(generic); err == nil {
			_ = json.Unmarshal(b, &out.Parsed)
		}
	}
	p := &out.Parsed
	p.Rules.Files = nonNil(p.Rules.Files)
	p.Rules.Skills = nonNil(p.Rules.Skills)
	if p.Rules.Mcp == nil {
		p.Rules.Mcp = make([]struct {
			Mode apigen.FactoryConfigSpecRulesMcpMode `json:"mode"`
			Name string                               `json:"name"`
			Url  string                               `json:"url"`
		}, 0)
	}
	p.Environment.Services = nonNil(p.Environment.Services)
	p.Layers.BR1, p.Layers.BR2, p.Layers.BR3, p.Layers.BR4 = nonNil(p.Layers.BR1), nonNil(p.Layers.BR2), nonNil(p.Layers.BR3), nonNil(p.Layers.BR4)
	p.ProtectedPaths = nonNil(p.ProtectedPaths)
	if p.Models == nil {
		p.Models = map[string]string{}
	}
	if p.Gates == nil {
		_ = json.Unmarshal([]byte("[]"), &p.Gates)
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
