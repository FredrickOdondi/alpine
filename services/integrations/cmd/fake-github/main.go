// Command fake-github serves githubfake (an in-memory GitHub: github.com pages + REST API) for the
// onboarding Playwright run. Point the API at it with GITHUB_API_URL and GITHUB_WEB_URL.
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/alpine-dev/alpine/services/integrations/github/githubfake"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9900", "listen address")
	user := flag.String("user", "fredrick", "the signed-in GitHub user")
	flag.Parse()
	f := githubfake.New(*user, githubfake.DemoRepos(*user)...)
	log.Printf("fake GitHub (test double) for @%s on http://%s", *user, *addr)
	log.Fatal(http.ListenAndServe(*addr, f))
}
