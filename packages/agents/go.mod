module github.com/alpine-dev/alpine/packages/agents

go 1.26.0

require github.com/alpine-dev/alpine/packages/shared v0.0.0

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dlclark/regexp2 v1.11.4 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/redis/go-redis/v9 v9.23.0 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel v1.47.0 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.opentelemetry.io/otel/metric v1.47.0 // indirect
	go.opentelemetry.io/otel/trace v1.47.0 // indirect
	go.uber.org/atomic v1.12.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)

replace github.com/alpine-dev/alpine/packages/shared => ../shared
