ARG NODE_IMAGE=node:24-bookworm-slim
ARG GO_IMAGE=golang:1.26-bookworm
ARG RUNTIME_IMAGE=debian:bookworm-slim
FROM ${NODE_IMAGE} AS web
WORKDIR /src/frontend
COPY frontend/package.json frontend/pnpm-lock.yaml frontend/pnpm-workspace.yaml ./
RUN corepack enable && corepack prepare pnpm@11.6.0 --activate && pnpm install --frozen-lockfile
COPY frontend/ ./
RUN pnpm build

FROM ${GO_IMAGE} AS build
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/frontend/dist/ ./internal/entrypoints/webassets/dist/
RUN for name in juex-management juex-runtime juex-execution juex-memory juex-calendar juex-executor juex-guest juex-service-log; do CGO_ENABLED=0 go build -trimpath -o /out/$name ./cmd/$name; done

FROM ${RUNTIME_IMAGE}
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
RUN sed -i 's|http://deb.debian.org|https://deb.debian.org|g' /etc/apt/sources.list.d/debian.sources && apt-get -o Acquire::https::Timeout=30 -o Acquire::Retries=3 update && apt-get -o Acquire::https::Timeout=30 -o Acquire::Retries=3 install -y --no-install-recommends ca-certificates iptables util-linux xfsprogs && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/ /usr/local/bin/
USER 10001:10001
CMD ["juex-management", "serve"]
