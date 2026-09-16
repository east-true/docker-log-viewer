FROM golang:1.26.8-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -trimpath \
    -ldflags="-s -w -X main.buildVersion=$VERSION -X main.buildCommit=$COMMIT" \
    -o /out/docker-log-viewer ./cmd/docker-log-viewer

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=build /out/docker-log-viewer /usr/local/bin/docker-log-viewer
LABEL org.opencontainers.image.source="https://github.com/east-true/docker-log-viewer" \
      org.opencontainers.image.licenses="Apache-2.0"
EXPOSE 8080 9080
ENTRYPOINT ["docker-log-viewer"]
CMD ["server", "-listen", "0.0.0.0:8080", "-agent-listen", "0.0.0.0:9080"]
