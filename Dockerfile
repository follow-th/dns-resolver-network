# syntax=docker/dockerfile:1
# Build one image with: docker build --target server . / docker build --target agent .

FROM --platform=$BUILDPLATFORM golang:1.27 AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/server ./cmd/server \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/agent ./cmd/agent

FROM gcr.io/distroless/static-debian12:nonroot AS server
LABEL org.opencontainers.image.source="https://github.com/follow-th/dns-resolver-network" \
      org.opencontainers.image.description="dns-resolver-network server" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/server /server
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/server"]
CMD ["-config", "/etc/drn/server.yaml"]

FROM gcr.io/distroless/static-debian12:nonroot AS agent
LABEL org.opencontainers.image.source="https://github.com/follow-th/dns-resolver-network" \
      org.opencontainers.image.description="dns-resolver-network agent" \
      org.opencontainers.image.licenses="MIT"
COPY --from=build /out/agent /agent
USER nonroot:nonroot
ENTRYPOINT ["/agent"]
CMD ["-config", "/etc/drn/agent.yaml"]
