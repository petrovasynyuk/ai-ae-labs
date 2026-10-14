# Build context: ai-ae-labs repository root. Go/ADK pins: September 2026.
FROM --platform=$BUILDPLATFORM golang:1.27.1 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
COPY week2/internal/refund/ ./week2/internal/refund/
COPY week2/Day4_Agent_as_Service_Deploy/labs4/ ./week2/Day4_Agent_as_Service_Deploy/labs4/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -mod=readonly -trimpath -ldflags="-s -w" \
    -o /out/adk-service ./week2/Day4_Agent_as_Service_Deploy/labs4

# Target 1: scratch (minimal, zero overhead, requires explicit CA bundle)
FROM scratch AS runtime-scratch
COPY --from=build /out/adk-service /adk-service
# Kept for HTTPS tool extensions; the default graph makes no outbound calls.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
USER 65532:65532
ENV PORT=8080
EXPOSE 8080
STOPSIGNAL SIGTERM
ENTRYPOINT ["/adk-service"]

# Target 2: distroless (Google Container Tools static-debian12, built-in CAs and nonroot)
FROM gcr.io/distroless/static-debian12:nonroot AS runtime-distroless
COPY --from=build /out/adk-service /adk-service
USER 65532:65532
ENV PORT=8080
EXPOSE 8080
STOPSIGNAL SIGTERM
ENTRYPOINT ["/adk-service"]

# Target 3: alpine (minimal Linux distribution with busybox, package manager, and built-in CAs)
FROM alpine:3.21 AS runtime-alpine
COPY --from=build /out/adk-service /adk-service
USER 65532:65532
ENV PORT=8080
EXPOSE 8080
STOPSIGNAL SIGTERM
ENTRYPOINT ["/adk-service"]

# Default target for standard builds without --target flag:
FROM runtime-scratch
