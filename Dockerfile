FROM golang:1.25.13-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/docker-log-viewer ./cmd/docker-log-viewer

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=build /out/docker-log-viewer /usr/local/bin/docker-log-viewer
EXPOSE 8080 9080
ENTRYPOINT ["docker-log-viewer"]
CMD ["server", "-listen", "0.0.0.0:8080", "-agent-listen", "0.0.0.0:9080"]
