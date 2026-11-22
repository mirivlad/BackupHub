#!/bin/sh
set -eu
if [ "$#" -ne 1 ] || [ ! -f "$1" ]; then
  printf 'Usage: %s /path/to/verified-v0.6.1.deb\n' "$0" >&2
  exit 2
fi
baseline=$(realpath "$1")
cd "$(dirname "$0")/../.."
lab=$(mktemp -d)
printf 'Guest acceptance logs: %s\n' "$lab"
mkdir "$lab/build"
npm --prefix web run build
CGO_ENABLED=0 go build -o "$lab/backuphub" ./cmd/backuphub
sh packaging/debian/build.sh 0.7.0~rc1 "$lab"
cp "$lab/backuphub_0.7.0~rc1_amd64.deb" "$lab/build/backuphub.deb"
cp "$baseline" "$lab/build/baseline.deb"
cp tests/resilience/guest.py tests/resilience/backuphub-guest-acceptance.service "$lab/build/"
docker build -t backuphub-resilience-fixtures tests/resilience
docker build -t backuphub-resilience-guest -f tests/resilience/Dockerfile.vm "$lab/build"
guest=$(docker create --network none backuphub-resilience-guest)
trap 'docker rm "$guest" >/dev/null' EXIT
docker cp "$guest:/boot/." "$lab/boot"
docker export "$guest" > "$lab/root.tar"
docker run --rm --network none -e "LAB_UID=$(id -u)" -e "LAB_GID=$(id -g)" \
  -v "$lab:/lab" backuphub-resilience-guest sh -eu -c '
  mkdir /tmp/guest
  tar xf /lab/root.tar -C /tmp/guest
  truncate -s 3G /lab/guest.img
  truncate -s 256M /lab/volume.img
  mkfs.ext4 -q -d /tmp/guest /lab/guest.img
  mkfs.ext4 -q /lab/volume.img
  chown "$LAB_UID:$LAB_GID" /lab/guest.img /lab/volume.img
  chmod 600 /lab/guest.img /lab/volume.img
'
rm -- "$lab/root.tar"
python3 tests/resilience/vm.py "$lab"
