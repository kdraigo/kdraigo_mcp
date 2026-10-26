# The hosted MCP server: kdraigo-mcp serve --http, behind https://mcp.kdraigo.com.
# Configuration is KDRAIGO_MCP_* (cmd/kdraigo-mcp/serve.go); it holds no secret
# but users_service's internal token, from the environment.
FROM golang:1.25.9-alpine AS build
WORKDIR /src
ENV GOTOOLCHAIN=local CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /kdraigo-mcp ./cmd/kdraigo-mcp

# Static, no shell, CA certificates for the platform's https API, nobody.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /kdraigo-mcp /kdraigo-mcp
USER nonroot:nonroot
EXPOSE 8090
ENTRYPOINT ["/kdraigo-mcp", "serve", "--http"]
