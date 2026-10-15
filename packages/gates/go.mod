module github.com/alpine-dev/alpine/packages/gates

go 1.26.0

require (
	github.com/alpine-dev/alpine/packages/shared v0.0.0
	go.yaml.in/yaml/v3 v3.0.5
)

replace github.com/alpine-dev/alpine/packages/shared => ../shared
