module github.com/alpine-dev/alpine/services/integrations

go 1.26.0

require (
	github.com/alpine-dev/alpine/packages/shared v0.0.0
	github.com/golang-jwt/jwt/v5 v5.3.1
)

replace github.com/alpine-dev/alpine/packages/shared => ../../packages/shared
