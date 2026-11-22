import http.cookiejar
import json
import os
import pathlib
import ssl
import subprocess as sp
import time
import urllib.request

def cmd(*args):
    return sp.check_output(args, text=True).strip()

base = pathlib.Path('/var/lib/bh-lab')
base.mkdir(exist_ok=True)
phase = base / 'phase'
state = pathlib.Path('/var/lib/backuphub')
url = 'https://127.0.0.1:8443'
cookies = http.cookiejar.CookieJar()
client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(cookies),
    urllib.request.HTTPSHandler(context=ssl._create_unverified_context()))
csrf = ''
credentials = dict(username='labadmin', password='isolated-guest-only-password-12345')

def http(route, payload=None, method=None):
    data = None if payload is None else json.dumps(payload).encode()
    req = urllib.request.Request(url + route, data, {'Content-Type':'application/json','X-BackupHub-CSRF':csrf}, method=method)
    try:
        with client.open(req, timeout=4) as response:
            return response.read()
    except urllib.error.HTTPError as error:
        print(route, error.read().decode(errors='replace'), flush=True)
        raise

def api(route, payload=None):
    return json.loads(http('/api/v1' + route, payload))

def await_value(fn, seconds=100):
    until = time.monotonic() + seconds
    while time.monotonic() < until:
        try:
            value = fn()
            if value:
                return value
        except OSError:
            pass
        time.sleep(.05)
    raise RuntimeError('time out awaiting '+str(fn))

def login():
    global csrf
    api('/auth/login', credentials)
    csrf = next(c.value for c in cookies if c.name == 'backuphub_csrf')

def restic(*args):
    return cmd('runuser', '-u', 'backuphub', '--', 'restic',
        '-r', str(state / 'repository'), '--password-file', str(state / 'restic.key'), *args)

def verify_mount_outage():
    mount = pathlib.Path('/mnt/backuphub-lab')
    mount.mkdir(exist_ok=True)
    # Give the sandbox its own propagation boundary before starting it.
    # Subsequent mount changes must be visible inside the running daemon.
    cmd('mount', '--bind', '/mnt', '/mnt')
    cmd('mount', '--make-rshared', '/mnt')
    cmd('mount', '/dev/vdb', str(mount))
    cmd('systemctl', 'restart', 'backuphub')
    await_value(lambda: api('/health'))
    login()
    mounted_source = mount / 'source'
    mounted_source.mkdir()
    (mounted_source / 'proof').write_text('source on isolated disk\n')
    root = pathlib.Path('/opt/bh-lab-source')
    target_source = root / 'tiny'
    target_source.mkdir()
    (target_source / 'proof').write_text('separate archive\n')
    store = api('/backup/storages', dict(name='Disposable volume',parent_path=str(mount),
        folder='archives',password_mode='password'))
    jobs = []
    for folder, repository, code in (
        (mounted_source, 'primary', 'source_unavailable'),
        (target_source, store['id'], 'storage_unavailable'),
    ):
        source = api('/backup/sources', dict(name=code,kind='local',path=str(folder)))
        job = api('/backup/jobs', dict(name=code,source_id=source['id'],repository_id=repository,
            retry_count=3,retry_interval_minutes=1,schedule=dict(mode='interval',interval_unit='days',
                interval_value=7,timezone='UTC',first_run='2099-01-01T02:00')))
        jobs.append((job, code))
    cmd('umount', str(mount))
    pid = cmd('systemctl', 'show', '-p', 'MainPID', '--value', 'backuphub')
    def daemon_has_mount():
        return any(line.split()[4] == str(mount) for line in pathlib.Path('/proc/'+pid+'/mountinfo').read_text().splitlines())
    await_value(lambda: not daemon_has_mount(), 5)
    # Filesystem directories beneath the mount are currently visible.
    # The locally configured source must never capture the underlying stub.
    (mount / 'source').mkdir()
    (mount / 'source' / 'proof').write_text('do not back up the stub\n')
    for job, _ in jobs:
        api('/backup/jobs/'+job['id']+'/run', {})
    def waiting():
        active = {j['id']: j for j in api('/backup/jobs')['jobs']}
        if all(active[job['id']].get('wait_reason') == code and active[job['id']].get('next_retry')
               for job, code in jobs):
            return {job['id']: active[job['id']]['operation_id'] for job, _ in jobs}
        return None
    waiting_ops = await_value(waiting,25)
    assert not (mount / 'archives' / 'repository' / 'config').exists()
    cmd('mount', '/dev/vdb', str(mount))
    await_value(daemon_has_mount, 5)
    def complete():
        success = {r['operation_id']: r for r in api('/backup/runs')['runs'] if r['state']=='completed' and r.get('operation_id')}
        if all(operation in success for operation in waiting_ops.values()):
            return success
        return None
    success = await_value(complete,110)
    for job, _ in jobs:
        recovered = success[waiting_ops[job['id']]]
        assert recovered['attempt'] == 2
        contents = http('/api/v1/backup/snapshots/'+recovered['snapshot_id']+'/download?mode=single&path=proof')
        assert contents in (b'source on isolated disk\n',b'separate archive\n')
    print('BACKUPHUB_MOUNT_OUTAGE_AND_AUTOMATIC_RECOVERY_OK', flush=True)

if not phase.exists():
    print(cmd('dpkg','-i','/opt/bh-lab/baseline.deb'),flush=True)
    await_value(lambda: api('/health'), 50)
    assert b'BackupHub' in http('/')
    assert cmd('systemctl','is-active','backuphub') == 'active'
    for unit in ('backuphub.service','backuphub-storage.socket','backuphub-storage.service'):
        cmd('systemd-analyze','verify',unit)
    token = cmd('runuser','-u','backuphub','--','backuphub','bootstrap','-data-dir',str(state)).splitlines()[1]
    api('/setup/admin', dict(bootstrap_token=token, **credentials))
    login()
    root = pathlib.Path('/opt/bh-lab-source')
    root.mkdir(exist_ok=True)
    root.chmod(0o755)
    (root / 'proof').write_text('before power loss\n')
    source = api('/backup/sources',dict(name='Power cut fixture',kind='local',path=str(root)))
    configuration = dict(name='Power cut fixture',source_id=source['id'],repository_id='primary',
        schedule=dict(mode='interval',interval_unit='days',interval_value=7,
            timezone='UTC',first_run='2099-01-01T02:00'))
    job = api('/backup/jobs', configuration)
    api('/backup/jobs/'+job['id']+'/run',{})
    previous = await_value(lambda: next((r for r in api('/backup/runs')['runs'] if r['state']=='completed'),None))
    old = api('/backup/snapshots?source_id='+source['id'])['snapshots'][0]
    assert old['id'] == previous['snapshot_id']
    print('BACKUPHUB_DEBIAN_INSTALL_HTTPS_AND_BACKUP_OK',flush=True)
    keys = {name: (state / name).read_bytes() for name in ('master.key','restic.key')}
    print(cmd('dpkg','-i','/opt/bh-lab/backuphub.deb'),flush=True)
    await_value(lambda: api('/health').get('version') == '0.7.0')
    login()
    upgraded = next(j for j in api('/backup/jobs')['jobs'] if j['id']==job['id'])
    assert upgraded['retry_count']==3 and upgraded['retry_interval_minutes']==30
    assert upgraded['next_run']==job['next_run']
    assert keys == {name: (state / name).read_bytes() for name in keys}
    assert http('/api/v1/backup/snapshots/'+old['id']+'/download?mode=single&path=proof')==b'before power loss\n'
    upgraded = json.loads(http('/api/v1/backup/jobs/'+job['id'], dict(configuration,retry_interval_minutes=1), 'PUT'))
    assert upgraded['retry_interval_minutes']==1
    print('BACKUPHUB_UPGRADE_KEYS_SCHEDULE_AND_OLD_SNAPSHOT_OK',flush=True)
    cmd('systemctl','restart','backuphub')
    await_value(lambda: api('/health'))
    login()
    assert http('/api/v1/backup/snapshots/'+old['id']+'/download?mode=single&path=proof')==b'before power loss\n'
    print('BACKUPHUB_SYSTEMD_RESTART_AND_RESTORE_OK',flush=True)
    (root / 'proof').write_text('after power loss\n')
    with (root / 'payload').open('wb') as blob:
        for _ in range(160):
            blob.write(os.urandom(2**20))
    payload = dict(job=job['id'], source=source['id'], old=old['id'], next_run=job['next_run'])
    phase.write_text(json.dumps(payload))
    cmd('sync')
    api('/backup/jobs/'+job['id']+'/run',{})
    await_value(lambda: next((r for r in api('/backup/runs')['runs'] if r['state']=='committing'),None))
    def restic_backup_active():
        for p in pathlib.Path('/proc').glob('[0-9]*/cmdline'):
            try:
                terms = p.read_bytes().split(b'\0')
            except (OSError, ProcessLookupError):
                continue
            if terms and terms[0].endswith(b'/restic') and b'backup' in terms and b'bh-pending' in terms:
                return True
        return False
    await_value(restic_backup_active)
    print('BACKUPHUB_CUT_POWER',flush=True)
    time.sleep(500)
else:
    payload = json.loads(phase.read_text())
    await_value(lambda: api('/health'), 50)
    login()
    def interruption():
        return next((r for r in api('/backup/runs')['runs'] if r['state']=='interrupted'), None)
    interrupted = await_value(interruption,30)
    assert interrupted['attempt']==1 and interrupted['trigger']=='manual'
    assert interrupted['failure_stage']=='committing'
    print('BACKUPHUB_POWER_CUT_RECORDED',flush=True)
    def recovered():
        return next((r for r in api('/backup/runs')['runs'] if r['state']=='completed' and r.get('operation_id')==interrupted['operation_id']),None)
    done = await_value(recovered,135)
    assert done['attempt']==2, done
    current = next(j for j in api('/backup/jobs')['jobs'] if j['id']==payload['job'])
    assert current['next_run']==payload['next_run']
    snaps = api('/backup/snapshots?source_id='+payload['source'])['snapshots']
    assert len(snaps)==2, snaps
    assert http('/api/v1/backup/snapshots/'+payload['old']+'/download?mode=single&path=proof')==b'before power loss\n'
    assert http('/api/v1/backup/snapshots/'+done['snapshot_id']+'/download?mode=single&path=proof')==b'after power loss\n'
    verify_mount_outage()
    cmd('systemctl','stop','backuphub')
    print(restic('check','--read-data','--no-lock'),flush=True)
    print('BACKUPHUB_POWER_RECOVERY_RESTIC_CHECK_AND_RESTORE_OK',flush=True)
    cmd('systemctl','poweroff')
