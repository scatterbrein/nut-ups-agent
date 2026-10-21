#!/usr/bin/env python3
"""Exercise the bootstrap using synthetic local downloads; never install anything."""
import hashlib,io,os,pathlib,subprocess,tarfile,tempfile
script=pathlib.Path(__file__).with_name('install-online.sh').resolve()
with tempfile.TemporaryDirectory(prefix='ups-install-test-') as stage:
    root=pathlib.Path(stage);fake=root/'bin';fake.mkdir();downloads=root/'downloads';downloads.mkdir()
    archive=downloads/'nut-ups-agent-linux-arm64-0.4.0.tar.gz'
    with tarfile.open(archive,'w:gz') as tar:
        data=b'#!/bin/sh\nprintf installed > "$INSTALL_TEST_MARKER"\n'
        info=tarfile.TarInfo('./scripts/install-and-start.sh');info.size=len(data);info.mode=0o755;tar.addfile(info,io.BytesIO(data))
    checksum=hashlib.sha256(archive.read_bytes()).hexdigest()
    (downloads/'SHA256SUMS').write_text(checksum+'  '+archive.name+'\n')
    mocks={
      'uname':'#!/bin/sh\ncase "$1" in -s) echo Linux;; -m) echo "${INSTALL_TEST_ARCH:-aarch64}";; esac\n',
      'id':'#!/bin/sh\necho 0\n',
      'systemctl':'#!/bin/sh\nexit 0\n',
      'curl':'''#!/usr/bin/env python3
import os,pathlib,shutil,sys,urllib.parse
args=sys.argv[1:];url=next(a for a in args if a.startswith('https://'));out=args[args.index('-o')+1]
shutil.copyfile(pathlib.Path(os.environ['INSTALL_TEST_DOWNLOADS'])/pathlib.Path(urllib.parse.urlsplit(url).path).name,out)
'''}
    for name,body in mocks.items():
        p=fake/name;p.write_text(body);p.chmod(0o755)
    marker=root/'installed'
    env=dict(os.environ,PATH=str(fake)+':'+os.environ['PATH'],INSTALL_TEST_DOWNLOADS=str(downloads),INSTALL_TEST_MARKER=str(marker))
    good=subprocess.run(['sh',str(script)],env=env,capture_output=True)
    assert good.returncode==0 and marker.read_text()=='installed',good.stderr.decode()
    marker.unlink();(downloads/'SHA256SUMS').write_text('0'*64+'  '+archive.name+'\n')
    bad=subprocess.run(['sh',str(script)],env=env,capture_output=True)
    assert bad.returncode!=0 and not marker.exists(),'unverified package was installed'
    bad=subprocess.run(['sh',str(script)],env=dict(env,INSTALL_TEST_ARCH='armv6l'),capture_output=True)
    assert bad.returncode!=0 and not marker.exists(),'unsupported architecture was installed'
    print('3 bootstrap checks passed: verified release, corrupt download blocked, unsupported architecture blocked.')
