#!/bin/sh
# Builds (build) or checks (check) the web UI (frontend/, ADR 0012). Runs in
# the pinned Node container from the Makefile, on a copy of the sources: the
# checkout's frontend/node_modules belongs to the host (npm run dev) and holds
# the host's native modules, so it is never used or touched here. npm's cache
# lives on the dev cache volume, so a repeat npm ci is quick.
set -eu
mode=${1:-build}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cd /src/frontend
cp -R package.json package-lock.json tsconfig.json vite.config.ts index.html public src "$work"/
cd "$work"
npm ci --no-audit --no-fund --loglevel=error
case $mode in
build)
	npm run build
	find /src/frontend/dist -mindepth 1 ! -name .gitkeep -exec rm -rf {} +
	cp -R dist/. /src/frontend/dist/
	echo "Built frontend/dist"
	;;
check)
	npm run typecheck
	npm test
	;;
*)
	echo "usage: frontend.sh build|check" >&2
	exit 64
	;;
esac
