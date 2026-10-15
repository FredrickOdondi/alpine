// Package selectrunner picks the sandbox backend from env: RUNNER=e2b|docker, defaulting to E2B when
// E2B_API_KEY is set and Docker otherwise.
package selectrunner

import (
	"fmt"
	"os"

	"github.com/alpine-dev/alpine/services/runner"
	"github.com/alpine-dev/alpine/services/runner/docker"
	"github.com/alpine-dev/alpine/services/runner/e2b"
)

// FromEnv returns the configured Runner.
func FromEnv() (runner.Runner, error) {
	kind := os.Getenv("RUNNER")
	if kind == "" {
		kind = "docker"
		if os.Getenv("E2B_API_KEY") != "" {
			kind = "e2b"
		}
	}
	switch kind {
	case "e2b":
		r := e2b.New(os.Getenv("E2B_API_KEY"))
		r.APIURL = os.Getenv("E2B_API_URL")
		return r, nil
	case "docker":
		return &docker.Runner{Bin: os.Getenv("DOCKER_BIN"), DefaultImage: os.Getenv("RUNNER_DOCKER_IMAGE")}, nil
	default:
		return nil, fmt.Errorf("runner: unknown RUNNER %q (want e2b or docker)", kind)
	}
}
