ARG GO_BUILD_IMAGE=docker.io/library/golang:1.27.4-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea
FROM ${GO_BUILD_IMAGE} AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY VERSION ./
COPY cmd ./cmd
COPY internal ./internal
ARG APP_VERSION
RUN test -n "$APP_VERSION" && test "$(cat VERSION)" = "$APP_VERSION" \
    && go build -buildvcs=false -trimpath -ldflags="-s -w -X main.version=$APP_VERSION" -o /out/runnerctl ./cmd/runnerctl

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chown=65532:65532 /out/runnerctl /runnerctl
USER 65532:65532
HEALTHCHECK NONE
ENTRYPOINT ["/runnerctl"]
CMD ["help"]
