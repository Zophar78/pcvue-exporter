import json
import ssl
import threading
import unittest
from contextlib import redirect_stderr
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from io import StringIO
from unittest.mock import patch
from urllib.parse import parse_qs, urlsplit

from explore_pcvue import (
    build_variables_url,
    child_branch_path,
    discover_variables,
    authenticated_session,
    list_root_branches,
    get_access_token,
    parse_branch_path,
    parse_timeout,
    variable_full_path,
)


class BranchPathTests(unittest.TestCase):
    def test_parses_single_and_nested_branch_paths(self):
        self.assertEqual(parse_branch_path("SITE_A"), ("SITE_A",))
        self.assertEqual(
            parse_branch_path("/SITE_A/BUILDING_A/"),
            ("SITE_A", "BUILDING_A"),
        )
        self.assertEqual(parse_branch_path(None), ())

    def test_rejects_empty_branch_segments(self):
        with self.assertRaisesRegex(ValueError, "empty segments"):
            parse_branch_path("SITE_A//BUILDING_A")

    def test_builds_encoded_branch_url_and_pagination_parameters(self):
        url = build_variables_url(
            "https://pcvue.example/",
            ("SITE_A", "TEMPERATURE", "SENSOR_1"),
            page_size=500,
            offset=1000,
        )

        self.assertEqual(
            url,
            "https://pcvue.example/RealTimeData/v2/Variables/"
            "SITE_A/TEMPERATURE/SENSOR_1?Depth=1&Size=500&Offset=1000",
        )

    def test_uses_variable_name_when_label_is_empty(self):
        path = child_branch_path(
            ("SITE_A",),
            {"VariableName": "TEMPERATURE", "Label": "", "IsLeaf": False},
        )

        self.assertEqual(path, ("SITE_A", "TEMPERATURE"))

    def test_uses_branches_array_for_full_variable_path(self):
        full_path = variable_full_path(
            (),
            {
                "VariableName": "TEMPERATURE_VALUE",
                "Label": "Room temperature",
                "branches": ["SITE_A", "TEMPERATURE", "SENSOR_1"],
            },
        )

        self.assertEqual(
            full_path,
            ("SITE_A", "TEMPERATURE", "SENSOR_1", "TEMPERATURE_VALUE"),
        )


class PaginationTests(unittest.TestCase):
    def test_stops_when_api_repeats_a_page(self):
        repeated_response = {
            "variableCollections": [
                {"IsLeaf": True, "VariableName": "one", "branches": []}
            ]
        }
        with patch(
            "explore_pcvue.request_json",
            side_effect=[repeated_response, repeated_response],
        ) as request:
            with self.assertRaisesRegex(RuntimeError, "duplicate page"):
                discover_variables(
                    "https://pcvue.example",
                    "test-token",
                    timeout=1,
                    ssl_context=ssl.create_default_context(),
                    page_size=1,
                    debug=False,
                )

        self.assertEqual(request.call_count, 2)

    def test_starts_discovery_at_requested_branch(self):
        requested_urls = []

        def respond(url, **kwargs):
            requested_urls.append(url)
            return {"variableCollections": []}

        with patch("explore_pcvue.request_json", side_effect=respond):
            variables, stats = discover_variables(
                "https://pcvue.example",
                "test-token",
                timeout=1,
                ssl_context=ssl.create_default_context(),
                initial_branch=("SITE_A", "BUILDING_A"),
                debug=False,
            )

        self.assertEqual(variables, [])
        self.assertEqual(stats.requests, 1)
        self.assertIn(
            "/RealTimeData/v2/Variables/SITE_A/BUILDING_A?",
            requested_urls[0],
        )

    def test_root_branch_listing_does_not_recurse(self):
        requested_urls = []

        def respond(url, **kwargs):
            requested_urls.append(url)
            return {
                "variableCollections": [
                    {
                        "IsLeaf": False,
                        "VariableName": "SITE_A",
                        "branches": [],
                    },
                    {
                        "IsLeaf": True,
                        "VariableName": "Date",
                        "branches": [],
                    },
                ]
            }

        with patch("explore_pcvue.request_json", side_effect=respond):
            branches, stats = list_root_branches(
                "https://pcvue.example",
                "test-token",
                timeout=1,
                ssl_context=ssl.create_default_context(),
                debug=False,
            )

        self.assertEqual([branch["FullPath"] for branch in branches], ["SITE_A"])
        self.assertEqual(stats.requests, 1)
        self.assertEqual(len(requested_urls), 1)


class ParseTimeoutTests(unittest.TestCase):
    def test_parses_go_style_timeout(self):
        self.assertEqual(parse_timeout("1m30s"), 90)
        self.assertEqual(parse_timeout("250ms"), 0.25)


class OAuthFlowTests(unittest.TestCase):
    def test_requests_token_then_fetches_variables(self):
        requested_paths = []

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, format, *args):
                pass

            def respond(self, status, payload):
                body = json.dumps(payload).encode("utf-8")
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def do_POST(self):
                form_body = self.rfile.read(
                    int(self.headers.get("Content-Length", "0"))
                ).decode("utf-8")
                form = parse_qs(form_body)
                valid_form = (
                    self.path == "/OAuth/token"
                    and form.get("grant_type") == ["password"]
                    and form.get("client_id") == ["test-client"]
                    and form.get("client_secret") == ["test-secret"]
                    and form.get("username") == ["test-user"]
                    and form.get("password") == ["test-password"]
                )
                if not valid_form:
                    self.respond(400, {"error": "invalid request"})
                    return
                self.respond(200, {"access_token": "test-access-token"})

            def do_GET(self):
                parsed = urlsplit(self.path)
                requested_paths.append(parsed.path)
                authorized = self.headers.get("Authorization") == (
                    "Bearer test-access-token"
                )
                if parsed.path == "/OAuth/Account/logout":
                    if not authorized:
                        self.send_response(401)
                        self.end_headers()
                        return
                    self.send_response(200)
                    self.end_headers()
                    return

                query = parse_qs(parsed.query)
                if (
                    not authorized
                    or query.get("Depth") != ["1"]
                    or query.get("Size") != ["1"]
                ):
                    self.respond(401, {"error": "unauthorized"})
                    return

                responses = {
                    "/RealTimeData/v2/Variables": [
                        {
                            "IsLeaf": False,
                            "VariableName": "SITE_A",
                            "Label": "",
                            "branches": [],
                        },
                        {
                            "IsLeaf": True,
                            "VariableName": "Date",
                            "Label": "",
                            "branches": [],
                        },
                    ],
                    "/RealTimeData/v2/Variables/SITE_A": [
                        {
                            "IsLeaf": False,
                            "VariableName": "TEMP",
                            "Label": "",
                            "branches": ["SITE_A"],
                        }
                    ],
                    "/RealTimeData/v2/Variables/SITE_A/TEMP": [
                        {
                            "IsLeaf": False,
                            "VariableName": "SENSOR_1",
                            "Label": "",
                            "branches": ["SITE_A", "TEMP"],
                        }
                    ],
                    "/RealTimeData/v2/Variables/SITE_A/TEMP/SENSOR_1": [
                        {
                            "IsLeaf": True,
                            "VariableName": "TEMPERATURE_VALUE",
                            "Label": "Room temperature",
                            "branches": ["SITE_A", "TEMP", "SENSOR_1"],
                        }
                    ],
                }
                entries = responses.get(parsed.path)
                if entries is None:
                    self.respond(404, {"error": "unknown branch"})
                    return

                offset = int(query.get("Offset", ["0"])[0])
                size = int(query["Size"][0])
                self.respond(
                    200,
                    {
                        "variableCollectionIterator": {
                            "CollectionRecordSize": len(entries)
                        },
                        "variableCollections": entries[offset : offset + size],
                    },
                )

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        worker = threading.Thread(target=server.serve_forever, daemon=True)
        worker.start()
        base_url = f"http://127.0.0.1:{server.server_port}"

        try:
            auth = {
                    "type": "oauth2_password",
                    "token_url": f"{base_url}/OAuth/token",
                    "client_id": "test-client",
                    "client_secret": "test-secret",
                    "username": "test-user",
                    "password": "test-password",
                    "scopes": ["RealtimeData"],
                }
            with authenticated_session(
                auth,
                base_url,
                timeout=2,
                ssl_context=ssl.create_default_context(),
                debug=False,
            ) as token:
                variables, stats = discover_variables(
                    base_url,
                    token,
                    timeout=2,
                    ssl_context=ssl.create_default_context(),
                    page_size=1,
                    debug=False,
                )

            self.assertIn(
                "SITE_A/TEMP/SENSOR_1/TEMPERATURE_VALUE",
                [item["FullPath"] for item in variables],
            )
            self.assertIn("/RealTimeData/v2/Variables/SITE_A/TEMP/SENSOR_1", requested_paths)
            self.assertEqual(requested_paths[-1], "/OAuth/Account/logout")
            self.assertEqual(stats.variables, 2)
            self.assertEqual(stats.branches, 3)
        finally:
            server.shutdown()
            server.server_close()
            worker.join(timeout=2)


if __name__ == "__main__":
    unittest.main()