# PCVue Prometheus Exporter

**Open-source Prometheus exporter for PCVue SCADA systems.**

PCVue Prometheus Exporter connects PCVue industrial supervision to the Prometheus monitoring ecosystem. It reads selected real-time variables and aggregate alarm statistics through the PCVue WST REST API and exposes them as Prometheus metrics.

Use the metrics to monitor industrial processes, visualize PCVue data in Grafana, track active and unacknowledged alarms, and create alerts in Prometheus and Alertmanager.

**Technologies:** PCVue, SCADA, Prometheus, Grafana, Go, REST API, OAuth2 and Docker.

> This independent open-source project requires access to a properly licensed PCVue WST REST API. It is not affiliated with PcVue Solutions.

## Documentation

- [Features](#features) and [example metrics](#metrics)
- [Authentication and prerequisites](#authentication)
- [Run locally](#running-locally) or [with Docker](#docker)
- [Explore PCVue variables](#exploring-pcvue-variables)
- [Build and test](#tests)


## Features

- Exposes a Prometheus `/metrics` endpoint
- Exposes a JSON `/healthz` endpoint
- Supports PCVue API configuration through YAML
- Supports environment variable expansion in the YAML config
- Exposes selected PCVue variables as metrics
- Exposes PCVue alarm aggregates
- Exposes build information through Prometheus metrics
- Uses Prometheus-style CLI flags with `kingpin`
- Uses Prometheus `promslog` logging

## Metrics

Example metrics:
```text
pcvue_up 1
pcvue_scrape_duration_seconds 0.123
pcvue_scrape_errors_total{type="variables"} 0
pcvue_scrape_errors_total{type="alarms"} 0
pcvue_variable_value{name="BMS.UPS.BATTERY_LEVEL",area="power"} 87
pcvue_alarm_active{severity="3",assoc_label="on - acknowledged"} 2
pcvue_alarm_unacknowledged{severity="0",assoc_label="on - not acknowledged"} 1
pcvue_alarm_oldest_active_age_seconds 1840
pcvue_exporter_build_info{version="0.0.1",revision="...",branch="main",build_date="...",build_user="...",goversion="..."} 1
```

Alarm aggregate metrics use `assoc_label`, read from the PCVue alarm properties.

`pcvue_scrape_errors_total` counts failed reads separately for `variables` and
`alarms`, incrementing once per failed read during each scrape, not per invalid
variable or alarm. Both series start at zero, remain exposed after successful
scrapes, and reset when the exporter restarts. `pcvue_up` remains zero if either
read fails and returns to one when both succeed.

### Alarm monitoring

The exporter reads PCVue alarms only to calculate aggregate statistics:

- `pcvue_alarm_active`: current number of active alarms by `severity` and `assoc_label`.
- `pcvue_alarm_unacknowledged`: current number of active unacknowledged alarms by the same labels.
- `pcvue_alarm_oldest_active_age_seconds`: age of the oldest active alarm.

`severity` is the value of PCVue's `AlarmLevel` property, copied as a string;
the exporter does not map numeric levels to names such as `critical`. Use the
exact values and association labels returned by your PCVue instance.

The grouped count series are emitted only when their count is positive. Queries
must account for an absent series when no alarms match a group.

Alarm aggregates are enabled by default. Set `aggregate: false` to disable alarm
collection entirely, including the PCVue alarm API request:

```yaml
alarms:
  aggregate: false
```

The current scope is to expose alarm aggregates, not a list of individual
alarms: aggregate statistics provide a metric view of PCVue's alarm activity,
while the alarm list belongs to operational alarm management.
Define monitoring alerts in Prometheus using the scraped variable values,
with Alertmanager handling notification routing. PCVue retains its own
operational alarms and acknowledgement.

`alarms.whitelist` is not accepted.

## Tests

Build the exporter and run tests:

```bash
go build ./...
go test -cover ./...
```

The command tests cover CLI flags, the process health endpoint, and the
healthcheck command. They use local test servers and do not contact PCVue.
CI also runs `go build ./...` to verify compilation.

## Authentication

The exporter requires access to the PCVue WST REST API. Using this API requires
a valid PCVue license; the exporter does not include the API or provide a
license.

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

Copy `.env.example` to `.env` and replace the example URLs, client ID and username
with your local values. The `.env` file is ignored by Git. To load it into the
current shell before running the exporter or the exploration script:

```bash
set -a
source .env
set +a
```

### Secret files

The exporter accepts `PCVUE_CLIENT_SECRET_FILE` and `PCVUE_PASSWORD_FILE` as
paths to files containing the respective credentials. For Docker secrets these
are normally `/run/secrets/pcvue_client_secret` and `/run/secrets/pcvue_password`.
A single trailing newline (LF or CRLF) is removed; other characters are preserved.
The exporter reads the files at startup: restart it after rotating a secret.
It fails on unreadable or empty files and if a file is configured alongside a
nonempty value for the same credential in YAML. Existing `client_secret` and
`password` configuration values, including environment expansion, remain supported.

These `_FILE` variables apply to the exporter, not `tools/explore_pcvue.py`.
For that script, provide the credentials via its YAML configuration or the
existing `PCVUE_CLIENT_SECRET` and `PCVUE_PASSWORD` variables.

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

Install the script dependency, set the credentials as described above, and load
the remaining settings from `.env` into the current shell:

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
PCVUE_CLIENT_SECRET_FILE="$PWD/secrets/pcvue_client_secret" \
PCVUE_PASSWORD_FILE="$PWD/secrets/pcvue_password" ./pcvue-exporter \
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

## Reverse proxy with a URL prefix

When exposing the exporter under a prefix such as `https://example.com/pcvue/`,
configure the proxy to strip `/pcvue` before forwarding requests to the
exporter. The landing page uses relative links, including the configured
`--web.metrics-path`, so they retain the external prefix. The proxy should
redirect `/pcvue` to `/pcvue/`: the trailing slash is necessary for browsers
to resolve relative links under the prefix. For example, route `/pcvue/metrics`
to the exporter's `/metrics` and `/pcvue/healthz` to `/healthz`.

## Docker
Build the image:
```bash
docker build -t pcvue-exporter:local .
```

Release tags publish images for `linux/amd64` and `linux/arm64`.

The image healthcheck runs `pcvue-exporter healthcheck` against
`http://127.0.0.1:9718/healthz`. It checks only the exporter HTTP server:
PCVue outages are reported through metrics such as `pcvue_up`, not Docker
health. It uses the default listen port, `9718`, regardless of the host port
used to publish the container port.

The check connects over IPv4 loopback, so the exporter must listen on
`127.0.0.1` or an address that includes it, such as `0.0.0.0`.

The default Compose service connects directly to the WST API with the OAuth
password grant. Copy `.env.example` to `.env`, and set the direct API URLs,
client ID and username. Then create the two secret files outside Git. For example,
this Bash snippet prompts without echoing or putting their values in shell history:

```bash
mkdir -p secrets
chmod 700 secrets
umask 077
read -r -s -p 'PCVue client secret: ' secret; printf '\n'
printf '%s' "$secret" > secrets/pcvue_client_secret
read -r -s -p 'PCVue password: ' secret; printf '\n'
printf '%s' "$secret" > secrets/pcvue_password
unset secret
docker compose up --build pcvue-exporter
```

Compose mounts these files under `/run/secrets/` and provides only their paths
in the container environment. The `secrets/` directory is excluded from Git and
the Docker build context. Local Compose file-based secrets are still files on
the host: protect their permissions and backups. Docker Swarm-managed secrets
provide a different storage and distribution mechanism.

To run the fake WST API and a second exporter instance, use the `fake` profile. The exporter metrics will be available on port `9719`, and the fake API on port `8083`:

```bash
docker compose --profile fake up --build pcvue-exporter-fake
```

For WSO2/API-gateway, set the WSO2 URLs and `PCVUE_CLIENT_ID` in `.env`,
create `secrets/pcvue_client_secret` as above, then run the `wso2` profile.
It does not require the password file. Its metrics endpoint is on port `9720`:

```bash
docker compose --profile wso2 up --build pcvue-exporter-wso2
```

Replace the example URLs and credentials before connecting to a real PCVue server or gateway.

For a standalone container, mount both secret files and pass their paths. The
example configuration also needs the non-secret API URLs, client ID and username;
set these in an environment file (for example `.env`) before running:

```bash
docker run --rm \
  -p 9718:9718 \
  --env-file .env \
  -e PCVUE_CLIENT_SECRET_FILE=/run/secrets/pcvue_client_secret \
  -e PCVUE_PASSWORD_FILE=/run/secrets/pcvue_password \
  -v "$PWD/secrets/pcvue_client_secret:/run/secrets/pcvue_client_secret:ro" \
  -v "$PWD/secrets/pcvue_password:/run/secrets/pcvue_password:ro" \
  -v "$PWD/config.api.example.yml:/config/config.yml:ro" \
  pcvue-exporter:local
```

If migrating an older `.env`, remove `PCVUE_CLIENT_SECRET` and `PCVUE_PASSWORD`
from it before using `--env-file`; the exporter rejects both a nonempty value
and its corresponding `_FILE` setting.
