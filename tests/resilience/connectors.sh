#!/bin/bash
set -euo pipefail

# Fixture accounts and database exist only in the disposable container.
test -f /.dockerenv
test -x /lab/backup.test
useradd -m bhfixture
printf 'bhfixture:lab-only-test-password\n' | chpasswd
install -d -o bhfixture -g bhfixture -m 0755 /home/bhfixture/capture /tmp/samba-fixture/subfolder
printf 'fixture in subfolder\n' | tee /home/bhfixture/capture/nested.txt /tmp/samba-fixture/subfolder/nested.txt > /dev/null
dd if=/dev/urandom of=/home/bhfixture/capture/payload bs=1M count=3 status=none
cp /home/bhfixture/capture/payload /tmp/samba-fixture/subfolder/payload
mkdir -p /run/samba
for spec in '1445 NT1 NT1' '1446 NT1 SMB3' '1447 SMB2_02 SMB2_10'; do
 read -r port min max <<< "$spec"
 dir=/tmp/samba-$port
 install -d "$dir"/{pid,lock,state,cache,private}
 cat > "$dir/smb.conf" <<CONF
[global]
 workgroup = WORKGROUP
 security = user
 interfaces = lo
 bind interfaces only = yes
 smb ports = $port
 server min protocol = $min
 server max protocol = $max
 server signing = mandatory
 smb encrypt = if_required
 log file = $dir/log.%m
 pid directory = $dir/pid
 lock directory = $dir/lock
 state directory = $dir/state
 cache directory = $dir/cache
 ncalrpc dir = $dir/lock
 private dir = $dir/private
[fixture]
 path = /tmp/samba-fixture
 valid users = bhfixture
 read only = yes
CONF
 printf 'lab-only-test-password\nlab-only-test-password\n' | smbpasswd -s -a -c "$dir/smb.conf" bhfixture
 smbd -D -s "$dir/smb.conf"
done
mkdir -p /run/sshd
/usr/sbin/sshd -p 2222 -o ListenAddress=127.0.0.1 -o PasswordAuthentication=yes -o UsePAM=no -o PidFile=/tmp/sshd.pid
pg_ctlcluster 17 main start
runuser -u postgres -- psql -v ON_ERROR_STOP=1 -q <<'SQL'
CREATE ROLE bhfixture LOGIN SUPERUSER PASSWORD 'lab-only-test-password';
CREATE DATABASE backuphub_faults OWNER bhfixture;
\connect backuphub_faults
CREATE TABLE ordinary(id int, content text);
INSERT INTO ordinary SELECT i,repeat(md5(i::text),64) FROM generate_series(1,5000) i;
CREATE TABLE config(id int, binarydata bytea);
INSERT INTO config SELECT i,decode(repeat(md5(i::text),128),'hex') FROM generate_series(1,5000) i;
SQL
export BACKUPHUB_RESTIC_REQUIRED=1
export BACKUPHUB_TEST_SMB1_HOST=127.0.0.1:1445 BACKUPHUB_TEST_SMB_DUAL_HOST=127.0.0.1:1446 BACKUPHUB_TEST_SMB2_HOST=127.0.0.1:1447
export BACKUPHUB_TEST_SMB1_USER=bhfixture BACKUPHUB_TEST_SMB1_PASSWORD=lab-only-test-password
export BACKUPHUB_TEST_SFTP_HOST=127.0.0.1:2222 BACKUPHUB_TEST_SFTP_USER=bhfixture BACKUPHUB_TEST_SFTP_PASSWORD=lab-only-test-password
export BACKUPHUB_TEST_PG1C_HOST=127.0.0.1:5432 BACKUPHUB_TEST_PG1C_USER=bhfixture BACKUPHUB_TEST_PG1C_PASSWORD=lab-only-test-password
export BACKUPHUB_TEST_SMALL_VOLUME=/bh-small-volume
exec /lab/backup.test -test.v -test.run 'TestRealTransportLossAndNoProgress|TestSIGTERMDuringSFTPTransferThenRepeatedFault|TestDiskFullResticRetry|TestSMB1CaptureAndRestore|TestSMBPrefersModernDialect|TestPostgres1CBackupAndRestore|TestResticProcessFaultsAndPublicationReconciliation|TestAutomaticRetryAfterDestinationReturns|TestOwnedStagingRecoveryLeavesUnknownPaths|TestPublicationReconcilesAfterSQLiteTransactionFailureWithRetriesDisabled'
