from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import parse_qs, urlparse
import json
import time

ACCESS_TOKEN = "fake-access-token"

VALID_CLIENT_ID = "fake-client-id"
VALID_CLIENT_SECRET = "fake-client-secret"
VALID_USERNAME = "monitoring-user"
VALID_PASSWORD = "test"


class Handler(BaseHTTPRequestHandler):
    def log_message(self, format, *args):
        # Keep default HTTP logs readable.
        print("%s - - [%s] %s" % (self.client_address[0], self.log_date_time_string(), format % args))

    def _json(self, payload, status=200):
        body = json.dumps(payload).encode()

        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _read_form(self):
        content_length = int(self.headers.get("Content-Length", "0"))
        raw_body = self.rfile.read(content_length).decode()
        return parse_qs(raw_body)

    def _authorized(self):
        return self.headers.get("Authorization") == f"Bearer {ACCESS_TOKEN}"

    def _token_response(self):
        self._json({
            "access_token": ACCESS_TOKEN,
            "token_type": "Bearer",
            "expires_in": 3600,
            "refresh_token": "fake-refresh-token",
            "scope": "RealtimeData RealtimeAlarm pcvue.read",
        })

    def _handle_oauth_token(self):
        form = self._read_form()

        grant_type = form.get("grant_type", [""])[0]
        client_id = form.get("client_id", [""])[0]
        client_secret = form.get("client_secret", [""])[0]
        username = form.get("username", [""])[0]
        password = form.get("password", [""])[0]

        if client_id != VALID_CLIENT_ID or client_secret != VALID_CLIENT_SECRET:
            self._json({
                "error": "invalid_client",
                "error_description": "Invalid client credentials",
            }, 401)
            return

        if grant_type == "password":
            if username != VALID_USERNAME or password != VALID_PASSWORD:
                self._json({
                    "error": "invalid_grant",
                    "error_description": "Invalid username or password",
                }, 400)
                return

            self._token_response()
            return

        if grant_type == "refresh_token":
            refresh_token = form.get("refresh_token", [""])[0]
            if refresh_token != "fake-refresh-token":
                self._json({
                    "error": "invalid_grant",
                    "error_description": "Invalid refresh token",
                }, 400)
                return

            self._token_response()
            return

        self._json({
            "error": "unsupported_grant_type",
            "error_description": f"Unsupported grant_type: {grant_type}",
        }, 400)

    def do_POST(self):
        parsed = urlparse(self.path)

        # PCVue direct style.
        if parsed.path == "/OAuth/token":
            self._handle_oauth_token()
            return

        if parsed.path == "/RealTimeData/v2/BulkRead":
            if not self._authorized():
                self._json({"error": "unauthorized"}, 401)
                return

            request = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))))
            requested_variables = request.get("Variables", [])
            now = time.strftime("%Y-%m-%dT%H:%M:%S.000", time.gmtime())

            # BulkRead returns an object keyed by the requested variable names.
            values = {
                "BMS.UPS.BATTERY_LEVEL": {
                    "value": 87,
                    "Timestamp": now,
                    "quality": "Good",
                    "QualityValue": 192,
                    "alarm": None,
                    "properties": {
                        "VariableName": "BMS.UPS.BATTERY_LEVEL",
                        "Description": "UPS battery level",
                    },
                },
                "BMS.HVAC.TEMP_SUPPLY": {
                    "value": 21.5,
                    "Timestamp": now,
                    "quality": "Good",
                    "QualityValue": 192,
                    "alarm": None,
                    "properties": {
                        "VariableName": "BMS.HVAC.TEMP_SUPPLY",
                        "Description": "HVAC supply temperature",
                    },
                },
            }
            self._json({name: values[name] for name in requested_variables if name in values})
            return

        self._json({"error": "not found"}, 404)

    def do_GET(self):
        parsed = urlparse(self.path)

        if parsed.path == "/RealTimeData/v2/Status":
            if not self._authorized():
                self._json({"error": "unauthorized"}, 401)
                return

            self._json({
                "status": "UP",
                "service": "RealTimeData",
            })
            return

        if parsed.path == "/RealTimeAlarm/v2/Status":
            if not self._authorized():
                self._json({"error": "unauthorized"}, 401)
                return

            self._json({
                "status": "UP",
                "service": "RealTimeAlarm",
            })
            return

        if parsed.path == "/RealTimeAlarm/v2/Values":
            if not self._authorized():
                self._json({"error": "unauthorized"}, 401)
                return

            now = time.strftime("%Y-%m-%dT%H:%M:%S.000", time.gmtime())

            # Shape aligned with PCVue alarm Values style:
            # values[] with timestamp, eventCode, properties[], eventValue.
            #
            # Properties follow the order requested by the exporter:
            # VariableName, AssocLabel, AlarmLevel, Description.
            self._json({
                "values": [
                    {
                        "removedFromList": False,
                        "timestamp": now,
                        "eventCode": "AlarmOnNotAck",
                        "properties": [
                            "POWER.UPS_BATTERY_FAULT",
                            "power",
                            "critical",
                            "UPS battery fault",
                        ],
                        "eventValue": 16,
                    },
                    {
                        "removedFromList": False,
                        "timestamp": now,
                        "eventCode": "AlarmOnAck",
                        "properties": [
                            "SYSTEM.SCADA_LINK_DOWN",
                            "system",
                            "critical",
                            "SCADA link down",
                        ],
                        "eventValue": 16,
                    },
                    {
                        "removedFromList": False,
                        "timestamp": now,
                        "eventCode": "AlarmUnavailable",
                        "properties": [
                            "BMS.HVAC_SENSOR_UNAVAILABLE",
                            "hvac",
                            "warning",
                            "HVAC sensor unavailable",
                        ],
                        "eventValue": 8,
                    },
                ],
                "maxNumberExceeded": False,
            })
            return

        self._json({"error": "not found"}, 404)


if __name__ == "__main__":
    HTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
