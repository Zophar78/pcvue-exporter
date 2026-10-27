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
pcvue_alarm_active_total{severity="critical",assoc_label="power"} 2
pcvue_alarm_unack_total{severity="critical",assoc_label="power"} 1
pcvue_alarm_oldest_active_age_seconds 1840
pcvue_alarm_active{alarm_id="UPS_BATTERY_FAULT",pcvue_name="POWER.UPS_BATTERY_FAULT",severity="critical",area="power"} 1
pcvue_exporter_build_info{version="0.0.1",revision="...",branch="main",build_date="...",build_user="...",goversion="..."} 1
```

Alarm aggregate metrics use `assoc_label`, read from the PCVue alarm properties. Whitelisted alarm metrics use `area`, configured per alarm in `alarms.whitelist`.

## Authentication

The exporter supports two OAuth2 authentication modes:

- `oauth2_password`: direct authentication against the PCVue REST API.
- `oauth2_client_credentials`: machine-to-machine authentication, typically when PCVue is exposed through an API gateway such as WSO2.

The OAuth2 token endpoint is configurable through `pcvue.auth.token_url`.
The example configurations read both the API base URL and token URL from
environment variables so environment-specific hostnames are not committed.

| Configuration | Environment variable | Example value |
| --- | --- | --- |
| Direct WST API base URL | `PCVUE_BASE_URL` | `https://pcvue.example.local` |
| Direct WST token URL | `PCVUE_TOKEN_URL` | `https://pcvue.example.local/OAuth/token` |
| WSO2 API base URL | `PCVUE_WSO2_BASE_URL` | `https://wso2.example.local/pcvue` |
| WSO2 token URL | `PCVUE_WSO2_TOKEN_URL` | `https://wso2.example.local/oauth2/token` |

Copy `.env.example` to `.env` and replace the example URLs and credentials with
your local values. The `.env` file is ignored by Git. To load it into the
current shell before running the exporter or the exploration script:

```bash
set -a
source .env
set +a
```

### PCVue direct authentication

When the exporter connects directly to PCVue, the PCVue WST REST API uses an OAuth2 password grant.

In this mode, the exporter needs:

- a PCVue OAuth client ID;
- a PCVue OAuth client secret;
- a PCVue/Web Services username;
- the corresponding password.

Example:

```yaml
pcvue:
  base_url: "${PCVUE_BASE_URL}"
  insecure_skip_verify: true
  timeout: "10s"

  auth:
    type: "oauth2_password"
    token_url: "${PCVUE_TOKEN_URL}"
    client_id: "${PCVUE_CLIENT_ID}"
    client_secret: "${PCVUE_CLIENT_SECRET}"
    username: "${PCVUE_USERNAME}"
    password: "${PCVUE_PASSWORD}"
    scopes:
      - "RealtimeData"
      - "RealtimeAlarm"
```
The exporter will request an access token from:
```http
POST /OAuth/token
```

### WSO2 / API Gateway authentication

When PCVue is exposed through WSO2 or another API gateway, the preferred mode is usually OAuth2 client credentials.

In this mode, the exporter does not use a PCVue user/password directly. It authenticates as a technical client using:

a client ID;
a client secret;
optional scopes.

Example:
```yaml
pcvue:
  base_url: "${PCVUE_WSO2_BASE_URL}"
  insecure_skip_verify: false
  timeout: "10s"

  auth:
    type: "oauth2_client_credentials"
    token_url: "${PCVUE_WSO2_TOKEN_URL}"
    client_id: "${PCVUE_CLIENT_ID}"
    client_secret: "${PCVUE_CLIENT_SECRET}"
    scopes:
      - "pcvue.read"
```
The exporter will request an access token from:
```http
POST /oauth2/token
```

## Exploring PCVue variables

The `tools/explore_pcvue.py` script uses the PCVue settings in
`config.api.example.yml` by default. It supports three modes:

- List branches directly below the root (the default; one level only).
- List every variable recursively with `--list-variables`; this can take a long time.
- List variables recursively below a selected branch with `--branch`.

Install the script dependency and export the credentials from the project `.env`
file into the current shell:

```bash
python3 -m pip install -r tools/requirements.txt
cp -n .env.example .env
set -a
source .env
set +a
```

Run from the repository root:

```bash
# List root branches only
python3 tools/explore_pcvue.py --list-root-branches

# List all variables recursively; this may generate many API requests
python3 tools/explore_pcvue.py --list-variables --output pcvue-variables.json

# List variables below one branch and all its sub-branches
python3 tools/explore_pcvue.py --branch 'SITE_A/TEMPERATURE/SENSOR_1' \
  --output branch-variables.json

# List only temperature variables for specific branch
python3 tools/explore_pcvue.py --branch 'SITE_A/OFFICE' | jq '[.[] | select(.Label == "Temparature")]'

# Show branch requests and traversal details
python3 tools/explore_pcvue.py --branch 'SITE_A/TEMPERATURE/SENSOR_1' --debug
```

The script supports the OAuth mode configured in the YAML. With
`oauth2_password`, it calls `GET /OAuth/Account/logout` when it finishes to
close the WST session. It does not call that endpoint with
`oauth2_client_credentials`. Use `--config path/to/config.yml` to select
another configuration file.

The exporter also handles `SIGINT` and `SIGTERM` by stopping its HTTP server
gracefully. With `oauth2_password`, it then calls the same PCVue logout endpoint
using the cached access token. No logout request is sent with
`oauth2_client_credentials`.


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

The default Compose service connects directly to the WST API with the OAuth password grant. Set `PCVUE_BASE_URL`, `PCVUE_TOKEN_URL`, and the four credentials in `.env` (Compose loads this file automatically), then run:

```bash
export PCVUE_CLIENT_ID='your-client-id'
export PCVUE_CLIENT_SECRET='your-client-secret'
export PCVUE_USERNAME='your-wst-username'
export PCVUE_PASSWORD='your-wst-password'
docker compose up --build
```

To run the fake WST API and a second exporter instance, use the `fake` profile. The exporter metrics will be available on port `9719`, and the fake API on port `8083`:

```bash
docker compose --profile fake up --build pcvue-exporter-fake
```

For a WSO2/API-gateway deployment, provide `PCVUE_CLIENT_ID` and `PCVUE_CLIENT_SECRET`, then run the separate `wso2` profile. Its metrics endpoint is exposed on port `9720`:

```bash
docker compose --profile wso2 up --build pcvue-exporter-wso2
```

Replace the example URLs and credentials before connecting to a real PCVue server or gateway.

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
