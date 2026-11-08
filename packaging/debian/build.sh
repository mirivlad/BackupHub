#!/bin/sh
# Build an installable Debian/Ubuntu amd64 package from already-tested binaries.
set -eu
VERSION="${1:-0.1.0~preview.1}"
ROOT="${2:-dist}"
ARCH="${3:-amd64}"
BASE="$ROOT/backuphub_${VERSION}_${ARCH}"
mkdir -p "$ROOT" "$BASE/DEBIAN" "$BASE/usr/bin" "$BASE/usr/share/backuphub/web" "$BASE/lib/systemd/system"
install -m 0755 "$ROOT/backuphub" "$BASE/usr/bin/backuphub"
cp -R web/dist/. "$BASE/usr/share/backuphub/web/"
install -m 0644 packaging/debian/backuphub.service "$BASE/lib/systemd/system/backuphub.service"
for name in postinst prerm postrm; do install -m 0755 "packaging/debian/$name" "$BASE/DEBIAN/$name"; done
cat > "$BASE/DEBIAN/control" <<CONTROL
Package: backuphub
Version: $VERSION
Section: admin
Priority: optional
Architecture: $ARCH
Maintainer: BackupHub contributors <noreply@github.com>
Depends: adduser, ca-certificates, openssl, restic, postgresql-client, systemd
Description: Self-hosted backup, version history and restoration system
 BackupHub provides a Go daemon, Svelte web interface, native SMB2/3,
 SFTP and PostgreSQL dump sources, scheduled Restic encrypted snapshots,
 browser downloads and safe alternate-directory restoration.
 This is a preview release; see supplied documentation for limitations.
CONTROL
dpkg-deb --build --root-owner-group "$BASE" "$ROOT/backuphub_${VERSION}_${ARCH}.deb"
dpkg-deb --info "$ROOT/backuphub_${VERSION}_${ARCH}.deb"
(cd "$ROOT" && sha256sum "backuphub_${VERSION}_${ARCH}.deb" > "backuphub_${VERSION}_${ARCH}.deb.sha256")
