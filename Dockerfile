# syntax=docker/dockerfile:1
# artex-mcp: thin MCP server exposing the ARTEX HTTP API as MCP tools.
# Build: docker build -t artex-mcp:0.2.0 .

# ---------- build stage: the MCP server (Go stdlib only) ----------
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod ./
COPY server/ .
RUN CGO_ENABLED=0 go build -trimpath -o /out/artex-mcp .

# ---------- runtime stage: minimal ----------
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/artex-mcp /usr/local/bin/artex-mcp

EXPOSE 8790
ENTRYPOINT ["/usr/local/bin/artex-mcp"]
