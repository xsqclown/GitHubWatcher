#!/usr/bin/env bash
# Puts the bot on the server it runs on: builds the binary for Linux, copies it
# and the configuration over, and restarts the service.
#
#   HOST=root@77.90.183.87 ./deploy.sh
#
# The service itself is /etc/systemd/system/fadwix-adminbot.service, which runs
# the bot as the "adminbot" user out of /opt/fadwix-adminbot and starts it again
# whenever it stops.
set -euo pipefail

HOST="${HOST:-root@77.90.183.87}"
DIR="${DIR:-/opt/fadwix-adminbot}"
VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"

echo "building ${VERSION} for linux/amd64"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
	-ldflags "-s -w -X main.version=${VERSION}" -o bin/fadwix-adminbot-linux ./cmd/bot

echo "copying to ${HOST}:${DIR}"
scp bin/fadwix-adminbot-linux "${HOST}:${DIR}/fadwix-adminbot.new"
scp .env repos.json messages.json "${HOST}:${DIR}/"

echo "restarting the service"
ssh "${HOST}" "
	set -e
	mv ${DIR}/fadwix-adminbot.new ${DIR}/fadwix-adminbot
	chmod +x ${DIR}/fadwix-adminbot
	chown -R adminbot:adminbot ${DIR}
	chmod 600 ${DIR}/.env
	systemctl restart fadwix-adminbot
	sleep 3
	systemctl is-active fadwix-adminbot
"
