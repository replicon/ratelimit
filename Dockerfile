FROM 434423891815.dkr.ecr.us-east-1.amazonaws.com/machine-images/fips-base:m-34423-amazon-linux-2023 AS build
WORKDIR /ratelimit

ENV GOPROXY=https://proxy.golang.org
ENV PATH=/usr/local/go/bin:$PATH
COPY go.mod go.sum /ratelimit/
RUN dnf upgrade -y && \
    dnf groupinstall -y "Development Tools" && \
    curl -fsSL https://go.dev/dl/go1.24.13.linux-amd64.tar.gz | tar -C /usr/local -xz && \
    go mod download

COPY src src
COPY script script
COPY test test

ARG BUILDPLATFORM
ARG TARGETPLATFORM
RUN if [ "$BUILDPLATFORM" = "$TARGETPLATFORM" ]; then go test -v -race github.com/replicon/ratelimit/... ; fi

RUN GOEXPERIMENT=boringcrypto CGO_ENABLED=1 GOOS=linux go build -o /go/bin/ratelimit -ldflags="-w -s" -v github.com/replicon/ratelimit/src/service_cmd && \
GOEXPERIMENT=boringcrypto CGO_ENABLED=1 GOOS=linux go build -o /go/bin/ratelimit_config_check -ldflags="-w -s" -v github.com/replicon/ratelimit/src/config_check_cmd


FROM 434423891815.dkr.ecr.us-east-1.amazonaws.com/machine-images/fips-base:m-34423-amazon-linux-2023 AS final

RUN dnf upgrade -y && dnf install -y python3 python3-pip && \
  pip3 install ipaddress pyyaml && \
  mkdir -p /srv/runtime_data/current/config && \
  mkdir -p /srv/runtime_data/current/validate_config

COPY --from=build /go/bin/ratelimit /bin/ratelimit
COPY --from=build /go/bin/ratelimit_config_check /bin/ratelimit_config_check
COPY entrypoint.sh /entrypoint.sh
COPY sync_config.sh /sync_config.sh
COPY metrics /metrics
ENTRYPOINT [ "/entrypoint.sh" ]
