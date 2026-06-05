# pcvue-exporter

Prometheus exporter for **PCVue** variables, metrics and alarms.

This exporter exposes selected PCVue real-time variables and alarm states as Prometheus metrics.

## Features

- Exposes a Prometheus `/metrics` endpoint
- Exposes a JSON `/healthz` endpoint
- Supports PCVue API configuration through YAML
- Supports environment variable expansion in the YAML config
- Exposes selected PCVue variables as metrics
- Exposes PCVue alarm aggregates
- Supports whitelisted alarm metrics
- Exposes build information through Prometheus metrics
- Uses Prometheus-style CLI flags with `kingpin`
- Uses Prometheus `promslog` logging

## Metrics

Example metrics:
```text
pcvue_up 1
pcvue_scrape_duration_seconds 0.123
pcvue_variable_value{name="BMS.UPS.BATTERY_LEVEL",alias="ups_battery_level",area="power"} 87
pcvue_variable_quality{name="BMS.UPS.BATTERY_LEVEL",alias="ups_battery_level",area="power"} 192
pcvue_alarm_active_total{severity="critical",area="power"} 2
pcvue_alarm_unack_total{severity="critical",area="power"} 1
pcvue_alarm_oldest_active_age_seconds 1840
pcvue_alarm_active{alarm_id="UPS_BATTERY_FAULT",pcvue_name="POWER.UPS_BATTERY_FAULT",severity="critical",area="power"} 1
pcvue_exporter_build_info{version="0.0.1",revision="...",branch="main",build_date="...",build_user="...",goversion="..."} 1
```


## Running locally
```bash
go mod tidy
go build -o pcvue-exporter ./cmd/pcvue-exporter
```

Run the exporter:
```bash
PCVUE_PASSWORD='your-password' ./pcvue-exporter \
  --config.file=config.yml \
  --web.listen-address=0.0.0.0:9718 \
  --web.metrics-path=/metrics \
  --log.level=info \
  --log.format=json
```

Build with Git metadata:
```bash
VERSION="$(git describe --tags --always --dirty)"
REVISION="$(git rev-parse --short HEAD)"
BRANCH="$(git rev-parse --abbrev-ref HEAD)"
BUILD_DATE="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
BUILD_USER="$(whoami)@$(hostname)"

go build \
  -ldflags "\
    -X github.com/prometheus/common/version.Version=${VERSION} \
    -X github.com/prometheus/common/version.Revision=${REVISION} \
    -X github.com/prometheus/common/version.Branch=${BRANCH} \
    -X github.com/prometheus/common/version.BuildDate=${BUILD_DATE} \
    -X github.com/prometheus/common/version.BuildUser=${BUILD_USER}" \
  -o pcvue-exporter \
  ./cmd/pcvue-exporter
```

## Docker
Build the image:
```bash
docker build -t pcvue-exporter:local .
```
Run it:
```bash
docker run --rm \
  -p 9718:9718 \
  -e PCVUE_PASSWORD='your-password' \
  -v "$PWD/config.yml:/config/config.yml:ro" \
  pcvue-exporter:local \
  --config.file=/config/config.yml \
  --web.listen-address=0.0.0.0:9718 \
  --web.metrics-path=/metrics \
  --log.level=info \
  --log.format=json
```