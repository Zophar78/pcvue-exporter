# Build
FROM golang:alpine AS builder

# Add ca-certs
RUN apk add --update --no-cache ca-certificates
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG REVISION=unknown
ARG BRANCH=unknown
ARG BUILD_DATE=unknown
ARG BUILD_USER=docker

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go mod download && \
    go build \
      -trimpath \
      -ldflags "\
        -s -w \
        -X github.com/prometheus/common/version.Version=${VERSION} \
        -X github.com/prometheus/common/version.Revision=${REVISION} \
        -X github.com/prometheus/common/version.Branch=${BRANCH} \
        -X github.com/prometheus/common/version.BuildDate=${BUILD_DATE} \
        -X github.com/prometheus/common/version.BuildUser=${BUILD_USER}" \
      -o /out/pcvue-exporter \
      ./cmd/pcvue-exporter

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /out/pcvue-exporter /pcvue-exporter

EXPOSE 9718

ENTRYPOINT ["/pcvue-exporter"]
CMD ["--config.file=/config/config.yml", "--web.listen-address=0.0.0.0:9718"]
