FROM golang:1.14 AS build
WORKDIR /ratelimit

ENV GOPROXY=https://proxy.golang.org
COPY go.mod go.sum /ratelimit/
RUN go mod download

COPY src src
COPY script script
COPY test test

RUN go test -v -race github.com/replicon/ratelimit/...

RUN CGO_ENABLED=0 GOOS=linux go build -o /go/bin/ratelimit -ldflags="-w -s" -v github.com/replicon/ratelimit/src/service_cmd && \
 CGO_ENABLED=0 GOOS=linux go build -o /go/bin/ratelimit_config_check -ldflags="-w -s" -v github.com/replicon/ratelimit/src/config_check_cmd
FROM alpine:3.11 AS final
RUN apk --no-cache add ca-certificates
COPY --from=build /go/bin/ratelimit /bin/ratelimit
COPY --from=build /go/bin/ratelimit_config_check /bin/ratelimit_config_check
ENTRYPOINT [ "/bin/ratelimit" ]
