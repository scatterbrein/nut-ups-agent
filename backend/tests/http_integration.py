#!/usr/bin/env python3
"""Exercise the production PHP bundle with the actual Go agent, using only local simulated UPS data."""
import argparse, contextlib, hashlib, json, os, pathlib, secrets, socket, socketserver, subprocess, tempfile, threading, time, urllib.error, urllib.request

parser=argparse.ArgumentParser()
parser.add_argument('--php',required=True)
parser.add_argument('--agent',required=True)
args=parser.parse_args()
root=pathlib.Path(__file__).resolve().parents[2]
passed=[]
mode={'status':'OL','charge':'100','available':True}

class NUT(socketserver.StreamRequestHandler):
    def handle(self):
        while True:
            line=self.rfile.readline()
            if not line or not mode['available']: return
            command=line.decode().strip()
            if command=='LIST UPS': response='BEGIN LIST UPS\nUPS ups_office "Office UPS"\nEND LIST UPS\n'
            elif command=='LIST VAR "ups_office"':
                variables={'ups.status':mode['status'],'battery.charge':mode['charge'],'ups.load':'5','ups.realpower.nominal':'810','battery.runtime':'11430','input.voltage':'125','output.voltage':'125','battery.voltage':'24','driver.password':'MUST-NOT-LEAVE-NUT'}
                response='BEGIN LIST VAR ups_office\n'+''.join(f'VAR ups_office {key} "{value}"\n' for key,value in variables.items())+'END LIST VAR ups_office\n'
            else: raise RuntimeError('Agent attempted a non-read-only NUT command')
            self.wfile.write(response.encode());self.wfile.flush()

server=socketserver.ThreadingTCPServer(('127.0.0.1',0),NUT)
server.daemon_threads=True
thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
with tempfile.TemporaryDirectory(prefix='ups-remote-http-') as directory:
    temp=pathlib.Path(directory);os.chmod(temp,0o700)
    with contextlib.closing(socket.socket()) as probe:
        probe.bind(('127.0.0.1',0));port=probe.getsockname()[1]
    base=f'http://127.0.0.1:{port}/api/v1'
    bootstrap=secrets.token_hex(32)
    env=os.environ.copy();env.update(UPS_STORAGE_DIR=str(temp/'backend'),UPS_BOOTSTRAP_HASH=hashlib.sha256(bootstrap.encode()).hexdigest(),UPS_PUBLIC_URL=base,UPS_ALLOW_LOOPBACK_HTTP='1')
    log=open(temp/'php.log','wb')
    process=subprocess.Popen([args.php,'-S',f'127.0.0.1:{port}','-t',str(root/'backend/dist'),str(root/'backend/dist/ups-api.php')],cwd=root,env=env,stdout=log,stderr=log)
    def request(method,path,token='',body=None,expected=200,content_type='application/json'):
        headers={}
        if token:headers['Authorization']='Bearer '+token
        data=None
        if body is not None:
            headers['Content-Type']=content_type;data=json.dumps(body).encode()
        req=urllib.request.Request(base+path,method=method,headers=headers,data=data)
        try:
            response=urllib.request.urlopen(req,timeout=15)
        except urllib.error.HTTPError as error:response=error
        with response:
            payload=response.read()
            assert response.status==expected, f'Unexpected status {response.status} for {method} {path}'
            assert 'no-store' in response.headers.get('Cache-Control',''), 'API response can be cached'
            assert 'noindex' in response.headers.get('X-Robots-Tag',''), 'Private API is indexable'
            assert 'Access-Control-Allow-Origin' not in response.headers, 'Cross-origin access enabled'
            return json.loads(payload)
    try:
        for _ in range(100):
            if process.poll() is not None:raise RuntimeError('PHP test server failed to start')
            try:request('GET','/health');break
            except (urllib.error.URLError,ConnectionError):time.sleep(.05)
        else:raise RuntimeError('PHP test server did not become ready')
        credentials=request('POST','/bootstrap',bootstrap,{'name':'Integration test'})
        request('POST','/bootstrap',bootstrap,{'name':'Again'},409)
        request('GET','/agents',expected=401)
        request('POST','/pairings',credentials['owner_token'],{'name':'Pi'},415,content_type='text/plain')
        passed.append('HTTP adapter authentication, content type and private cache headers')
        code=request('POST','/pairings',credentials['owner_token'],{'name':'Pi test'})['pairing_code']
        config=temp/'config.json'
        config.write_text(json.dumps({'state_dir':str(temp/'agent'),'sources':[{'id':'office','name':'Office','host':'127.0.0.1','port':server.server_address[1]}],'backend':{'url':'','allow_http_loopback':True}}));os.chmod(config,0o600)
        def agent(*flags):
            result=subprocess.run([args.agent,'-config',str(config),*flags],cwd=root,capture_output=True,text=True,timeout=20)
            assert result.returncode==0,'Go agent command failed: '+result.stderr
            assert 'ups_agent_' not in result.stdout+result.stderr,'Raw token appeared in agent output'
            return result.stdout
        agent('-pair-url',base,'-pair-code',code)
        saved=json.loads((temp/'agent/backend-connection.json').read_text())
        assert saved['url']==base+'/ingest' and 'agent_token' not in saved
        passed.append('Real Go binary pairs with PHP and saves separate private credentials')
        agent('-once')
        current=json.loads(agent('-status'));agent_id=current['agent_id']
        snapshot=request('GET',f'/agents/{agent_id}/state',credentials['owner_token'])
        reading=snapshot['devices'][0]['reading'];original_time=reading['observed_at']
        assert reading['power_watts']==40.5 and reading['power_estimated']
        assert 'MUST-NOT-LEAVE-NUT' not in json.dumps(snapshot)
        assert current['pending_messages']==0
        passed.append('NUT to Go queue to PHP durable receipt and private state')
        mode.update(status='OB',charge='87');agent('-once')
        mode.update(status='OB LB',charge='9');agent('-once')
        mode.update(status='OL',charge='87');agent('-once')
        events=request('GET',f'/agents/{agent_id}/events',credentials['owner_token'])['items']
        assert [e['data']['kind'] for e in events]==['on_battery','low_battery','power_restored']
        passed.append('Power loss, low battery and restoration events from real agent')
        latest=request('GET',f'/agents/{agent_id}/state',credentials['owner_token'])['devices'][0]['reading']
        mode['available']=False;agent('-once');agent('-once')
        snapshot=request('GET',f'/agents/{agent_id}/state',credentials['owner_token'])
        assert snapshot['devices'][0]['stale'] and snapshot['devices'][0]['reading']==latest
        mode['available']=True;agent('-once')
        snapshot=request('GET',f'/agents/{agent_id}/state',credentials['owner_token'])
        assert not snapshot['devices'][0]['stale']
        events=request('GET',f'/agents/{agent_id}/events',credentials['owner_token'])['items']
        assert sum(e['data']['kind']=='nut_server_unavailable' for e in events)==1
        assert sum(e['data']['kind']=='nut_server_reconnected' for e in events)==1
        passed.append('Frozen offline state, thresholded outage and one reconnection')
        for file in (temp/'backend').iterdir():
            if file.is_file():assert 'MUST-NOT-LEAVE-NUT' not in file.read_bytes().decode(errors='ignore')
        request('POST',f'/agents/{agent_id}/revoke',credentials['owner_token'],{})
        result=subprocess.run([args.agent,'-config',str(config),'-once'],capture_output=True,text=True,timeout=20)
        assert result.returncode!=0 and json.loads(agent('-status'))['pending_messages']>0
        passed.append('Remote revocation rejects upload without losing pending local readings')
        print(json.dumps({'passed':len(passed),'failed':0,'scenarios':passed},indent=2))
    finally:
        process.terminate();process.wait(timeout=10);log.close()
server.shutdown();server.server_close()
