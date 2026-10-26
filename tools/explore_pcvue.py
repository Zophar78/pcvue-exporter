#!/usr/bin/env python3
"""List PCVue variables by walking the RealTimeData Variables tree."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import ssl
import sys
from collections import deque
from contextlib import contextmanager
from dataclasses import dataclass
from collections.abc import Iterator, Mapping
from pathlib import Path
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urlencode
from urllib.request import Request, urlopen


DEFAULT_CONFIG = Path(__file__).resolve().parents[1] / "config.api.example.yml"
ENV_REFERENCE = re.compile(r"\$\{[^}]+\}|\$[A-Za-z_][A-Za-z0-9_]*")
DURATION_PART = re.compile(r"(\d+(?:\.\d+)?)(ms|s|m|h)")


def parse_timeout(value: Any) -> float:
    if isinstance(value, (int, float)):
        return float(value)

    text = str(value).strip()
    position = 0
    seconds = 0.0
    units = {"ms": 0.001, "s": 1.0, "m": 60.0, "h": 3600.0}

    for match in DURATION_PART.finditer(text):
        if match.start() != position:
            raise ValueError(f"invalid timeout value: {value!r}")
        seconds += float(match.group(1)) * units[match.group(2)]
        position = match.end()

    if position != len(text) or seconds <= 0:
        raise ValueError(f"invalid timeout value: {value!r}")

    return seconds


def parse_branch_path(value: str | None) -> tuple[str, ...]:
    if value is None:
        return ()
    parts = tuple(part.strip() for part in value.strip("/").split("/"))
    if not parts or any(not part for part in parts):
        raise ValueError("branch path must not contain empty segments")
    return parts


def load_config(path: Path) -> tuple[dict[str, Any], dict[str, Any]]:
    try:
        import yaml
    except ImportError as error:
        raise RuntimeError(
            "PyYAML is required; install it with "
            "'python -m pip install -r tools/requirements.txt'"
        ) from error

    raw_config = os.path.expandvars(path.read_text(encoding="utf-8"))
    unresolved = ENV_REFERENCE.search(raw_config)
    if unresolved:
        raise ValueError(
            f"environment variable {unresolved.group(0)!r} is not set"
        )

    try:
        config = yaml.safe_load(raw_config) or {}
    except yaml.YAMLError as error:
        raise ValueError(f"invalid YAML configuration: {error}") from error

    if not isinstance(config, dict):
        raise ValueError("configuration root must be a YAML mapping")

    pcvue = config.get("pcvue")
    if not isinstance(pcvue, dict):
        raise ValueError("pcvue configuration is missing")

    auth = pcvue.get("auth")
    if not isinstance(auth, dict):
        raise ValueError("pcvue.auth configuration is missing")

    if not pcvue.get("base_url"):
        raise ValueError("pcvue.base_url is required")

    return pcvue, auth


def request_json(
    url: str,
    *,
    timeout: float,
    ssl_context: ssl.SSLContext,
    form: Mapping[str, str] | None = None,
    access_token: str | None = None,
) -> Any:
    headers = {"Accept": "application/json"}
    data = None

    if form is not None:
        data = urlencode(form).encode("utf-8")
        headers["Content-Type"] = "application/x-www-form-urlencoded"

    if access_token:
        headers["Authorization"] = f"Bearer {access_token}"

    request = Request(url, data=data, headers=headers)

    try:
        with urlopen(request, timeout=timeout, context=ssl_context) as response:
            body = response.read().decode("utf-8-sig")
    except HTTPError as error:
        detail = error.read().decode("utf-8-sig", errors="replace").strip()
        suffix = f": {detail}" if detail else ""
        raise RuntimeError(f"HTTP {error.code} from {url}{suffix}") from None
    except URLError as error:
        raise RuntimeError(f"request to {url} failed: {error.reason}") from None

    try:
        return json.loads(body)
    except json.JSONDecodeError as error:
        raise RuntimeError(f"invalid JSON response from {url}: {error}") from error


def get_access_token(
    auth: Mapping[str, Any],
    *,
    timeout: float,
    ssl_context: ssl.SSLContext,
) -> str:
    auth_type = auth.get("type", "oauth2_password")
    form = {
        "client_id": str(auth.get("client_id", "")),
        "client_secret": str(auth.get("client_secret", "")),
    }

    if auth_type == "oauth2_password":
        form.update(
            {
                "grant_type": "password",
                "username": str(auth.get("username", "")),
                "password": str(auth.get("password", "")),
            }
        )
    elif auth_type == "oauth2_client_credentials":
        form["grant_type"] = "client_credentials"
    else:
        raise ValueError(f"unsupported pcvue.auth.type: {auth_type!r}")

    required_fields = ["client_id", "client_secret"]
    if auth_type == "oauth2_password":
        required_fields.extend(["username", "password"])
    missing_fields = [field for field in required_fields if not form[field]]
    if missing_fields:
        raise ValueError(
            "missing OAuth configuration fields: " + ", ".join(missing_fields)
        )

    scopes = auth.get("scopes") or []
    if isinstance(scopes, str):
        scopes = [scopes]
    if scopes:
        form["scope"] = " ".join(str(scope) for scope in scopes)

    token_url = auth.get("token_url")
    if not token_url:
        raise ValueError("pcvue.auth.token_url is required")

    token_response = request_json(
        str(token_url),
        timeout=timeout,
        ssl_context=ssl_context,
        form=form,
    )
    if not isinstance(token_response, dict) or not token_response.get("access_token"):
        raise RuntimeError("OAuth response does not contain an access_token")

    return str(token_response["access_token"])


def logout_session(
    base_url: str,
    access_token: str,
    *,
    timeout: float,
    ssl_context: ssl.SSLContext,
) -> None:
    url = f"{base_url.rstrip('/')}/OAuth/Account/logout"
    request = Request(
        url,
        headers={"Authorization": f"Bearer {access_token}"},
        method="GET",
    )

    try:
        with urlopen(request, timeout=timeout, context=ssl_context) as response:
            response.read()
    except HTTPError as error:
        detail = error.read().decode("utf-8-sig", errors="replace").strip()
        suffix = f": {detail}" if detail else ""
        raise RuntimeError(f"PCVue logout failed: HTTP {error.code}{suffix}") from None
    except URLError as error:
        raise RuntimeError(f"PCVue logout request failed: {error.reason}") from None


@contextmanager
def authenticated_session(
    auth: Mapping[str, Any],
    base_url: str,
    *,
    timeout: float,
    ssl_context: ssl.SSLContext,
    debug: bool,
) -> Iterator[str]:
    access_token = get_access_token(
        auth,
        timeout=timeout,
        ssl_context=ssl_context,
    )
    should_logout = auth.get("type", "oauth2_password") == "oauth2_password"

    try:
        yield access_token
    except BaseException:
        if should_logout:
            try:
                logout_session(
                    base_url,
                    access_token,
                    timeout=timeout,
                    ssl_context=ssl_context,
                )
                if debug:
                    print("[debug] logged out from PCVue session", file=sys.stderr)
            except (OSError, RuntimeError) as error:
                print(f"warning: could not log out from PCVue session: {error}", file=sys.stderr)
        raise
    else:
        if should_logout:
            logout_session(
                base_url,
                access_token,
                timeout=timeout,
                ssl_context=ssl_context,
            )
            if debug:
                print("[debug] logged out from PCVue session", file=sys.stderr)


@dataclass
class TraversalStats:
    requests: int = 0
    branches: int = 0
    variables: int = 0
    empty_branches: int = 0
    unclassified_nodes: int = 0


def node_label(node: Mapping[str, Any]) -> str:
    for key in (
        "variableName",
        "VariableName",
        "name",
        "Name",
        "variableCollectionName",
        "VariableCollectionName",
        "Label",
    ):
        value = node.get(key)
        if isinstance(value, str) and value.strip():
            return value.strip()
        if isinstance(value, (int, float)):
            return str(value)
    return "<unnamed>"


def child_branch_path(
    parent_path: tuple[str, ...], node: Mapping[str, Any]
) -> tuple[str, ...]:
    name = node_label(node)
    if name == "<unnamed>":
        return ()

    parts = tuple(part for part in name.strip("/").split("/") if part)
    ancestors = node.get("branches")
    if isinstance(ancestors, list) and all(isinstance(part, str) for part in ancestors):
        ancestor_parts = tuple(part.strip("/") for part in ancestors if part.strip("/"))
        if ancestor_parts:
            if parts[: len(ancestor_parts)] == ancestor_parts:
                return parts
            if ancestor_parts[-len(parts) :] == parts:
                return ancestor_parts
            return ancestor_parts + parts
    if parts[: len(parent_path)] == parent_path:
        return parts
    return parent_path + parts


def variable_full_path(
    parent_path: tuple[str, ...], variable: Mapping[str, Any]
) -> tuple[str, ...]:
    name = node_label(variable)
    name_parts = tuple(part for part in name.strip("/").split("/") if part)
    ancestors = variable.get("branches")
    if isinstance(ancestors, list) and all(isinstance(part, str) for part in ancestors):
        ancestor_parts = tuple(part.strip("/") for part in ancestors if part.strip("/"))
        if ancestor_parts:
            if name_parts[: len(ancestor_parts)] == ancestor_parts:
                return name_parts
            if ancestor_parts[-len(name_parts) :] == name_parts:
                return ancestor_parts
            return ancestor_parts + name_parts
    if name_parts[: len(parent_path)] == parent_path:
        return name_parts
    return parent_path + name_parts


def build_variables_url(
    base_url: str,
    branch_path: tuple[str, ...],
    *,
    page_size: int,
    offset: int,
) -> str:
    endpoint = f"{base_url.rstrip('/')}/RealTimeData/v2/Variables"
    if branch_path:
        encoded_branch = "/".join(quote(part, safe="") for part in branch_path)
        endpoint = f"{endpoint}/{encoded_branch}"

    return f"{endpoint}?{urlencode({'Depth': 1, 'Size': page_size, 'Offset': offset})}"


def debug_node_details(node: Mapping[str, Any]) -> str:
    fields = ("VariableName", "Label", "IsLeaf", "variableType")
    details = {key: node[key] for key in fields if key in node}
    branch_names = node.get("branches")
    if isinstance(branch_names, list) and all(
        isinstance(name, str) for name in branch_names
    ):
        details["branches"] = branch_names[:10]
        if len(branch_names) > 10:
            details["branches_remaining"] = len(branch_names) - 10
    return json.dumps(details, ensure_ascii=False)


def json_shape(value: Any, depth: int = 0) -> Any:
    if isinstance(value, dict) and depth < 3:
        keys = list(value)
        shape = {key: json_shape(value[key], depth + 1) for key in keys[:20]}
        if len(keys) > 20:
            shape["..."] = f"{len(keys) - 20} more keys"
        return shape
    if isinstance(value, list):
        return {
            "type": "array",
            "length": len(value),
            "first_items": [json_shape(item, depth + 1) for item in value[:2]],
        }
    return type(value).__name__


def fetch_branch_items(
    base_url: str,
    access_token: str,
    branch_path: tuple[str, ...],
    *,
    timeout: float,
    ssl_context: ssl.SSLContext,
    page_size: int = 500,
    debug: bool,
    stats: TraversalStats | None = None,
) -> Iterator[dict[str, Any]]:
    if page_size < 1:
        raise ValueError("page_size must be greater than zero")

    stats = stats or TraversalStats()
    offset = 0
    branch_had_items = False
    seen_pages: set[str] = set()
    branch_label = "/".join(branch_path) or "<root>"

    while True:
        url = build_variables_url(
            base_url,
            branch_path,
            page_size=page_size,
            offset=offset,
        )
        stats.requests += 1
        if debug:
            print(
                f"[debug] GET branch={branch_label!r} depth=1 "
                f"size={page_size} offset={offset}",
                file=sys.stderr,
            )

        response = request_json(
            url,
            timeout=timeout,
            ssl_context=ssl_context,
            access_token=access_token,
        )
        if not isinstance(response, dict):
            raise RuntimeError(
                f"invalid Variables response for {branch_label}: expected a JSON object"
            )

        collections = response.get("variableCollections")
        if collections is None:
            collections = []
        if not isinstance(collections, list):
            raise RuntimeError(
                f"invalid variableCollections for {branch_label}: expected a JSON array"
            )

        page_signature = hashlib.sha256(
            json.dumps(
                collections,
                sort_keys=True,
                ensure_ascii=False,
                separators=(",", ":"),
            ).encode("utf-8")
        ).hexdigest()
        if collections and page_signature in seen_pages:
            raise RuntimeError(
                f"PCVue returned a duplicate page for branch {branch_label} "
                f"at offset {offset}; stopping to avoid an endless pagination loop. "
                "Check whether the API accepts the Offset parameter."
            )
        seen_pages.add(page_signature)

        if debug and stats.requests == 1:
            print(
                f"[debug] response shape: {json.dumps(json_shape(response))}",
                file=sys.stderr,
            )
        if debug:
            iterator = response.get("variableCollectionIterator")
            iterator_details: dict[str, Any] = {}
            if isinstance(iterator, dict):
                for field in ("CollectionRecordSize", "DepthLevel", "branches"):
                    if field in iterator:
                        iterator_details[field] = iterator[field]
            print(
                f"[debug] page branch={branch_label!r} offset={offset} "
                f"returned={len(collections)} "
                f"iterator={json.dumps(iterator_details, ensure_ascii=False)}",
                file=sys.stderr,
            )

        if collections:
            branch_had_items = True
        for item in collections:
            if isinstance(item, dict):
                yield item
            else:
                stats.unclassified_nodes += 1

        if len(collections) < page_size:
            break
        offset += len(collections)

    if not branch_had_items:
        stats.empty_branches += 1


def list_root_branches(
    base_url: str,
    access_token: str,
    *,
    timeout: float,
    ssl_context: ssl.SSLContext,
    page_size: int = 500,
    debug: bool,
) -> tuple[list[dict[str, Any]], TraversalStats]:
    stats = TraversalStats()
    branches = []
    for item in fetch_branch_items(
        base_url,
        access_token,
        (),
        timeout=timeout,
        ssl_context=ssl_context,
        page_size=page_size,
        debug=debug,
        stats=stats,
    ):
        if item.get("IsLeaf") is not False:
            continue
        path = child_branch_path((), item)
        if not path:
            stats.unclassified_nodes += 1
            continue
        record = dict(item)
        record["FullPath"] = "/".join(path)
        branches.append(record)
        stats.branches += 1
        if debug and stats.branches <= 200:
            print(f"[debug] root branch {record['FullPath']}", file=sys.stderr)

    return branches, stats


def discover_variables(
    base_url: str,
    access_token: str,
    *,
    timeout: float,
    ssl_context: ssl.SSLContext,
    page_size: int = 500,
    initial_branch: tuple[str, ...] = (),
    debug: bool,
    stats: TraversalStats | None = None,
) -> tuple[list[dict[str, Any]], TraversalStats]:
    if page_size < 1:
        raise ValueError("page_size must be greater than zero")

    stats = stats or TraversalStats()
    variables: list[dict[str, Any]] = []
    pending_branches = deque([initial_branch])
    visited_branches: set[tuple[str, ...]] = set()

    while pending_branches:
        branch_path = pending_branches.popleft()
        if branch_path in visited_branches:
            continue
        visited_branches.add(branch_path)

        for item in fetch_branch_items(
            base_url,
            access_token,
            branch_path,
            timeout=timeout,
            ssl_context=ssl_context,
            page_size=page_size,
            debug=debug,
            stats=stats,
        ):
            is_leaf = item.get("IsLeaf")
            if is_leaf is True or (
                "variableCollections" in item and item["variableCollections"] is None
            ):
                stats.variables += 1
                name = node_label(item)
                full_path = variable_full_path(branch_path, item)
                record = dict(item)
                if full_path:
                    record["FullPath"] = "/".join(full_path)
                variables.append(record)
                if debug and stats.variables <= 200:
                    print(
                        f"[debug] variable {record.get('FullPath', name)}: "
                        f"{debug_node_details(item)}",
                        file=sys.stderr,
                    )
                continue

            if is_leaf is False:
                stats.branches += 1
                child_path = child_branch_path(branch_path, item)
                if not child_path or child_path == branch_path:
                    stats.unclassified_nodes += 1
                    if debug:
                        print(
                            "[debug] branch has no usable name: "
                            f"{debug_node_details(item)}",
                            file=sys.stderr,
                        )
                    continue
                if child_path not in visited_branches:
                    pending_branches.append(child_path)
                if debug and stats.branches <= 200:
                    print(
                        f"[debug] queued branch {'/'.join(child_path)}: "
                        f"{debug_node_details(item)}",
                        file=sys.stderr,
                    )
                continue

            stats.unclassified_nodes += 1
            if debug:
                print(
                    f"[debug] unclassified collection: {debug_node_details(item)}",
                    file=sys.stderr,
                )

    return variables, stats


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Explore PCVue branches and variables from the RealTimeData API."
    )
    parser.add_argument(
        "--config",
        type=Path,
        default=DEFAULT_CONFIG,
        help=f"YAML configuration file (default: {DEFAULT_CONFIG})",
    )
    parser.add_argument(
        "--output",
        type=Path,
        help="write the JSON list to this file instead of stdout",
    )
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument(
        "--list-root-branches",
        action="store_true",
        help="list only branches directly below the root (default)",
    )
    mode.add_argument(
        "--list-variables",
        action="store_true",
        help="recursively list all variables; may take a long time",
    )
    mode.add_argument(
        "--branch",
        help="recursively list variables in this branch, e.g. SITE_A/BUILDING_A",
    )
    parser.add_argument(
        "--debug",
        action="store_true",
        help="print each branch request and discovered branch or variable",
    )
    args = parser.parse_args()

    try:
        pcvue, auth = load_config(args.config)
        timeout = parse_timeout(pcvue.get("timeout", "10s"))
        ssl_context = ssl.create_default_context()
        if pcvue.get("insecure_skip_verify", False):
            ssl_context.check_hostname = False
            ssl_context.verify_mode = ssl.CERT_NONE

        with authenticated_session(
            auth,
            str(pcvue["base_url"]),
            timeout=timeout,
            ssl_context=ssl_context,
            debug=args.debug,
        ) as access_token:
            if args.list_variables:
                items, stats = discover_variables(
                    str(pcvue["base_url"]),
                    access_token,
                    timeout=timeout,
                    ssl_context=ssl_context,
                    debug=args.debug,
                )
                result_label = "variables"
            elif args.branch is not None:
                items, stats = discover_variables(
                    str(pcvue["base_url"]),
                    access_token,
                    timeout=timeout,
                    ssl_context=ssl_context,
                    initial_branch=parse_branch_path(args.branch),
                    debug=args.debug,
                )
                result_label = "variables"
            else:
                items, stats = list_root_branches(
                    str(pcvue["base_url"]),
                    access_token,
                    timeout=timeout,
                    ssl_context=ssl_context,
                    debug=args.debug,
                )
                result_label = "root branches"
        if args.debug:
            if stats.branches > 200:
                print("[debug] branch log limited to the first 200 branches", file=sys.stderr)
            if stats.variables > 200:
                print("[debug] variable log limited to the first 200 variables", file=sys.stderr)
            print(
                "[debug] traversal summary: "
                f"requests={stats.requests}, branches={stats.branches}, "
                f"variables={stats.variables}, "
                f"empty_branches={stats.empty_branches}, "
                f"unclassified_objects={stats.unclassified_nodes}",
                file=sys.stderr,
            )
            if stats.branches == 0 and stats.variables == 0:
                print(
                    "[debug] no recognized branch or leaf was found; "
                    "the response may use a different schema",
                    file=sys.stderr,
                )
        output = json.dumps(items, ensure_ascii=False, indent=2) + "\n"

        if args.output:
            args.output.write_text(output, encoding="utf-8")
            print(f"Listed {len(items)} {result_label} in {args.output}", file=sys.stderr)
        else:
            sys.stdout.write(output)
            print(f"Listed {len(items)} {result_label}", file=sys.stderr)
    except (OSError, RuntimeError, ValueError) as error:
        print(f"error: {error}", file=sys.stderr)
        return 1

    return 0


if __name__ == "__main__":
    raise SystemExit(main())