import glob,pathlib,signal,subprocess,sys,time
d=pathlib.Path(sys.argv[1])
options=['qemu-system-x86_64','-enable-kvm','-cpu','host','-smp','2','-m','2048','-nodefaults','-nographic',
    '-no-reboot','-nic','none','-monitor','none','-serial','stdio',
    '-kernel',glob.glob(str(d/'boot/vmlinuz-*'))[0],'-initrd',glob.glob(str(d/'boot/initrd.img-*'))[0],
    '-append','root=/dev/vda rw console=ttyS0 systemd.show_status=error fsck.repair=yes panic=1',
    '-drive',f'file={d}/guest.img,format=raw,if=virtio,cache=writethrough',
    '-drive',f'file={d}/volume.img,format=raw,if=virtio,cache=writethrough']
for phase, marker, timeout in [(1,'BACKUPHUB_CUT_POWER',150),(2,'BACKUPHUB_POWER_RECOVERY_RESTIC_CHECK_AND_RESTORE_OK',270)]:
    log=d/f'vm-{phase}.log'
    with log.open('wb') as f:
        vm=subprocess.Popen(options,stdout=f,stderr=subprocess.STDOUT)
        until=time.monotonic()+timeout
        try:
            while time.monotonic()<until and vm.poll() is None:
                output = log.read_text(errors='replace')
                if marker in output or 'Traceback (most recent call last):' in output:
                    break
                time.sleep(.04)
            if marker not in log.read_text(errors='replace'):
                raise RuntimeError(f'missing {marker}, see {log}')
            if phase==1:
                vm.send_signal(signal.SIGKILL)
                assert vm.wait(timeout=10)==-signal.SIGKILL
            else:
                assert vm.wait(timeout=40)==0
        finally:
            if vm.poll() is None:
                vm.kill()
                vm.wait()
print('Guest acceptance completed')
