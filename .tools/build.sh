#!/usr/bin/env bash
# Sandbox-local Go wrapper: HOME is read-only here, so GOCACHE/GOMODCACHE are
# redirected into the repo (both are gitignored). Usage: bash .tools/build.sh test ./internal/cli/
set -euo pipefail
export PATH=/usr/local/go/bin:$PATH
export GOFLAGS=-mod=mod
export GOMODCACHE=/home/li63050a/work/boxli/.gomodcache
export GOCACHE=/home/li63050a/work/boxli/.gocache
export GOPATH=/home/li63050a/work/boxli/.gopath
cd /home/li63050a/work/boxli
exec go "$@"
