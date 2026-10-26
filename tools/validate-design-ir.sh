#!/usr/bin/env sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
ir_file="$repo_root/docs/design.ir.yaml"
PYTHONDONTWRITEBYTECODE=1
export PYTHONDONTWRITEBYTECODE

if [ -n "${DESIGN_IR_REPO:-}" ]; then
    PYTHONPATH="$DESIGN_IR_REPO/src${PYTHONPATH:+:$PYTHONPATH}"
    export PYTHONPATH
    expected_schema_sha=$(python3 - "$ir_file" <<'PY'
import sys
from design_ir.validator import load_document

print(load_document(sys.argv[1])["schema_ref"]["sha256"])
PY
    )
    actual_schema_sha=$(sha256sum "$DESIGN_IR_REPO/schema/design-ir.schema.json" | cut -d ' ' -f 1)
    if [ "$expected_schema_sha" != "$actual_schema_sha" ]; then
        printf '%s\n' "Design IR schema digest mismatch: expected $expected_schema_sha, got $actual_schema_sha" >&2
        exit 2
    fi
fi

if [ -n "${DESIGN_IR_REPO:-}" ]; then
    python3 - "$ir_file" <<'PY'
import sys

from design_ir.validator import load_document, validate_document

document = load_document(sys.argv[1])
report = validate_document(document)
for error in report.structural_errors:
    print(f"SCHEMA ERROR: {error}")
for error in report.semantic_errors:
    print(f"SEMANTIC ERROR: {error}")
for warning in report.warnings:
    print(f"WARNING: {warning}")
print("VALID" if report.valid else "INVALID")
raise SystemExit(0 if report.valid else 1)
PY
    exit $?
fi

if command -v design-ir >/dev/null 2>&1; then
    exec design-ir validate "$ir_file"
fi

printf '%s\n' 'design-ir CLI not found. Install the official v1alpha1 validator or set DESIGN_IR_REPO to a design-ir checkout.' >&2
exit 2
