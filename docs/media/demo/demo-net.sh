#!/usr/bin/env bash
# The demo LAN of the README's recording and screenshots (docs/media): a
# Docker bridge network standing in for a production line, with PLCs, a drive,
# an HMI and a camera that announce themselves over mDNS, a DHCP and DNS
# server, an engineering laptop on DHCP, and lan-tracker itself in a container
# with only CAP_NET_RAW, its web UI published on 127.0.0.1:9735.
#
#   docs/media/demo/demo-net.sh up [binary]   # binary: default bin/lan-tracker (make build)
#   docs/media/demo/demo-net.sh late          # a drive joins the line
#   docs/media/demo/demo-net.sh conflict      # a laptop claims the packaging PLC's address
#   docs/media/demo/demo-net.sh vanish        # the dock camera goes away
#   docs/media/demo/demo-net.sh cli <args>    # lan-tracker in the edge container
#   docs/media/demo/demo-net.sh down
#
# The MAC addresses carry real vendors' OUIs so the registrant shows; the
# devices are busybox and Python containers, not the vendors' products.
set -euo pipefail
cd "$(dirname "$0")/../../.."

NET=lt-demo SUBNET=172.30.40.0/24 GW=172.30.40.1
BUSYBOX=busybox:stable@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662
DEBIAN=debian:bookworm-slim@sha256:99c0b25c46d0214d5a2091092e7908907dcaecd6a962cde0ac700acee222b6f3
PYTHON=python:3.12-alpine
ALPINE=alpine:3.22
NODES=(lt-edge lt-dhcp lt-plc-press lt-plc-pack lt-drive-mixer lt-hmi lt-cam lt-laptop lt-drive-conveyor lt-rogue)
DEMO=$PWD/docs/media/demo

# Keeps a device talking: one ping to the gateway every 15 s, so its ARP and
# IPv4 frames reach the capture as a real device's traffic would.
chatter="while :; do ping -c1 -W1 $GW >/dev/null 2>&1; sleep 15; done"

node() { # name ip mac image cmd...
	local name=$1 ip=$2 mac=$3 image=$4
	shift 4
	docker run -d --name "$name" --hostname "${name#lt-}" --network "$NET" --ip "$ip" --mac-address "$mac" \
		-v "$DEMO:/demo:ro" "$image" sh -c "$*" >/dev/null
}

up() {
	local bin=${1:-bin/lan-tracker}
	[ -x "$bin" ] || { echo "demo-net: $bin missing (make build)" >&2; exit 2; }
	docker network create --subnet "$SUBNET" --gateway "$GW" "$NET" >/dev/null

	# DHCP (2-minute leases, so renewals show) and DNS for the line's names.
	docker run -d --name lt-dhcp --hostname gw-dhcp --network "$NET" --ip 172.30.40.3 --mac-address 00:0d:b9:40:00:03 \
		--cap-add NET_ADMIN "$ALPINE" sh -c "apk add --no-cache dnsmasq >/dev/null && exec dnsmasq -k --log-facility=- \
		--no-resolv --no-hosts --local-ttl=600 --domain=line1.plant --local=/line1.plant/ \
		--dhcp-range=172.30.40.100,172.30.40.150,2m --dhcp-option=option:router,$GW --dhcp-option=option:dns-server,172.30.40.3 \
		--host-record=plc-press.line1.plant,172.30.40.10 --host-record=plc-pack.line1.plant,172.30.40.11 \
		--host-record=drive-mixer.line1.plant,172.30.40.12 --host-record=hmi-line1.line1.plant,172.30.40.20 \
		--host-record=drive-conveyor.line1.plant,172.30.40.13" >/dev/null

	node lt-plc-press 172.30.40.10 00:1b:1b:4a:10:01 "$BUSYBOX" "$chatter & nc -lk -p 102 -e cat"         # Siemens OUI, S7 port
	node lt-plc-pack 172.30.40.11 00:80:f4:22:11:02 "$BUSYBOX" "$chatter & nc -lk -p 502 -e cat"          # Schneider OUI, Modbus
	node lt-drive-mixer 172.30.40.12 00:00:bc:5e:12:03 "$BUSYBOX" "$chatter & nc -lk -p 44818 -e cat"     # Rockwell OUI, EtherNet/IP
	node lt-hmi 172.30.40.20 00:30:de:40:20:04 "$PYTHON" \
		"$chatter & python3 -m http.server 80 >/dev/null 2>&1 & exec python3 /demo/mdns.py hmi-line1 172.30.40.20 'HMI Line 1'"  # WAGO OUI
	node lt-cam 172.30.40.21 00:40:8c:21:00:05 "$PYTHON" \
		"$chatter & python3 -m http.server 80 >/dev/null 2>&1 & exec python3 /demo/mdns.py cam-dock 172.30.40.21 'Dock camera'"  # Axis OUI
	# The engineering laptop asks for a lease every two minutes, with the broadcast
	# flag, so the replies reach the edge box on this switched bridge too.
	docker run -d --name lt-laptop --hostname eng-laptop --network "$NET" --ip 172.30.40.50 --mac-address 3c:22:fb:50:00:06 \
		--cap-add NET_ADMIN "$BUSYBOX" sh -c "$chatter & while :; do udhcpc -B -i eth0 -n -q -s /bin/true -x hostname:eng-laptop >/dev/null 2>&1; sleep 110; done" >/dev/null

	docker volume create lt-demo-data >/dev/null
	docker run -d --name lt-edge --hostname edge-box --network "$NET" --ip 172.30.40.2 --mac-address 02:00:5e:40:00:02 \
		--cap-drop ALL --cap-add NET_RAW -p 127.0.0.1:9735:9735 \
		-v "$PWD/$bin:/usr/local/bin/lan-tracker:ro" -v "$DEMO/config.yaml:/etc/lan-tracker/config.yaml:ro" \
		-v lt-demo-data:/data/lan-tracker -e TZ=UTC \
		"$DEBIAN" lan-tracker daemon run --config /etc/lan-tracker/config.yaml >/dev/null
	echo "demo-net: up; UI token: $0 cli ui url"
}

case ${1:-} in
up) shift; up "$@" ;;
late) node lt-drive-conveyor 172.30.40.13 00:1d:9c:13:00:07 "$BUSYBOX" "$chatter & nc -lk -p 44818 -e cat" ;; # Rockwell OUI
conflict)
	# A laptop with a static address copied from the packaging PLC.
	docker run -d --name lt-rogue --hostname service-laptop --network "$NET" --ip 172.30.40.66 --mac-address 28:d2:44:66:00:08 \
		--cap-add NET_ADMIN --cap-add NET_RAW "$BUSYBOX" sh -c \
		"ip addr add 172.30.40.11/24 dev eth0 && while :; do arping -U -c 2 -s 172.30.40.11 -I eth0 172.30.40.11 >/dev/null 2>&1; sleep 10; done" >/dev/null ;;
vanish) docker rm -f lt-cam >/dev/null ;;
cli) shift; docker exec lt-edge lan-tracker "$@" ;;
down)
	docker rm -f "${NODES[@]}" >/dev/null 2>&1 || true
	docker network rm "$NET" >/dev/null 2>&1 || true
	docker volume rm lt-demo-data >/dev/null 2>&1 || true
	;;
*) sed -n '2,15p' "$0"; exit 64 ;;
esac
