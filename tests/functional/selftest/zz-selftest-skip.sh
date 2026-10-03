#!/bin/sh
# Must be reported SKIP, and must NOT be counted as passed.
skip_case "the harness skip path, exercised deliberately"
echo "FAIL: skip_case returned instead of ending the case"
exit 1
