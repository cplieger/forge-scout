# check=error=true
FROM golang:1.27-alpine@sha256:7d5cbf6833f7331dafd25a2e8b9673477f559759ff8ed4ca8efabe6795ad08db AS builder
ENV GOTOOLCHAIN=auto

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY internal/ internal/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /forge-scout .
RUN mkdir -p /out/data /out/config
COPY LICENSE NOTICE THIRD_PARTY_NOTICES.md ./
COPY scripts/collect-licenses.sh scripts/
RUN sh scripts/collect-licenses.sh --name forge-scout .

FROM gcr.io/distroless/static-debian13:nonroot@sha256:d62067879de890a1def9da0e7766b7d20810a5e5555cef8506b752437d7a3e88

COPY --chmod=755 --from=builder /forge-scout /forge-scout
COPY --chown=65532:65532 --from=builder /out/data /data
COPY --chown=65532:65532 --from=builder /out/config /config
COPY --from=builder /out/usr/share/licenses /usr/share/licenses
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=5s --retries=3 --start-period=15s \
    CMD ["/forge-scout", "health"]
ENTRYPOINT ["/forge-scout"]
