#!/bin/sh
# Enables the repo-local git hooks (does not touch user-level git settings).
# Without this, .githooks/pre-commit never runs and secretscan is CI-only.
git config core.hooksPath .githooks
