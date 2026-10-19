#!/usr/bin/env python3
"""Explicit operator-only HTTPS smoke test. Creates and deletes synthetic workspaces; never contacts real NUT."""
import argparse,datetime,json,os,pathlib,socketserver,subprocess,tempfile,threading,urllib.error,urllib.request,uuid

parser=argparse.ArgumentParser(description=__doc__)
parser.add_argument('--credentials',type=pathlib.Path,required=True)
parser.add_argument('--agent',required=True)
args=parser.parse_args()
credentials=json.loads(args.credentials.read_text());base=credentials['public_url'].rstrip('/')
assert base=='https://ups.omstudioit.com/api/v1','This smoke test targets the configured pilot only'
passed=[];workspaces=[];mode={'status':'OL','charge':'100','available':True}
class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self,*a,**k):return None
def request(method,path,token='',body=None,expected=200):
    headers={'Content-Type':'application/json'}
    if token:headers['Authorization']='Bearer '+token
    req=urllib.request.Request(base+path,method=method,headers=headers,data=None if body is None else json.dumps(body).encode())
    try:response=urllib.request.build_opener(NoRedirect).open(req,timeout=30)
    except urllib.error.HTTPError as error:response=error
    with response:
        assert response.status==expected,f'Unexpected HTTP {response.status}: {method} {path}'
        assert 'no-store' in response.headers.get('Cache-Control','') and 'noindex' in response.headers.get('X-Robots-Tag','')
        assert 'Access-Control-Allow-Origin' not in response.headers
        return json.load(response)
class NUT(socketserver.StreamRequestHandler):
    def handle(self):
        while True:
            line=self.rfile.readline()
            if not line or not mode['available']:return
            command=line.decode().strip()
            if command=='LIST UPS':data='BEGIN LIST UPS\nUPS pilot_ups "Synthetic test UPS"\nEND LIST UPS\n'
            elif command=='LIST VAR "pilot_ups"':
                values={'ups.status':mode['status'],'battery.charge':mode['charge'],'ups.load':'5','ups.realpower.nominal':'810','battery.runtime':'11430','driver.password':'DO-NOT-UPLOAD-THIS'}
                data='BEGIN LIST VAR pilot_ups\n'+''.join(f'VAR pilot_ups {k} "{v}"\n' for k,v in values.items())+'END LIST VAR pilot_ups\n'
            else:raise AssertionError('Unexpected NUT command')
            self.wfile.write(data.encode());self.wfile.flush()
server=socketserver.ThreadingTCPServer(('127.0.0.1',0),NUT);server.daemon_threads=True
threading.Thread(target=server.serve_forever,daemon=True).start()
try:
    assert request('GET','/health')['status']=='ok'
    request('GET','/agents',expected=401)
    for label in ['A','B']:
        workspaces.append(request('POST','/admin/workspaces',credentials['admin_token'],{'name':'Automated synthetic test '+label}))
    owner=workspaces[0]['owner_token'];other=workspaces[1]['owner_token']
    passed.append('Hosted HTTPS API healthy; private noncacheable authentication enforced')
    code=request('POST','/pairings',owner,{'name':'Synthetic Go agent'})['pairing_code']
    with tempfile.TemporaryDirectory(prefix='ups-hosted-test-') as directory:
        temp=pathlib.Path(directory);os.chmod(temp,0o700)
        config=temp/'config.json'
        config.write_text(json.dumps({'state_dir':str(temp/'state'),'sources':[{'id':'synthetic','name':'Synthetic UPS','host':'127.0.0.1','port':server.server_address[1]}],'backend':{'url':''}}));os.chmod(config,0o600)
        def agent(*flags,success=True):
            result=subprocess.run([args.agent,'-config',str(config),*flags],capture_output=True,text=True,timeout=35)
            assert (result.returncode==0)==success,'Agent execution did not match expected result'
            assert 'ups_agent_' not in result.stdout+result.stderr
            return result.stdout
        agent('-pair-url',base,'-pair-code',code);agent('-once')
        status=json.loads(agent('-status'));identity=status['agent_id']
        snapshot=request('GET',f'/agents/{identity}/state',owner)
        assert status['pending_messages']==0 and snapshot['devices'][0]['reading']['power_watts']==40.5
        assert not snapshot['devices'][0]['stale'] and 'DO-NOT-UPLOAD-THIS' not in json.dumps(snapshot)
        request('GET',f'/agents/{identity}/state',other,expected=404)
        assert request('GET','/agents',other)['agents']==[]
        passed.append('Real Go pairing and synthetic NUT upload; other owner cannot read the agent')
        mode.update(status='OB',charge='87');agent('-once')
        mode.update(status='OB LB',charge='9');agent('-once')
        mode.update(status='OL',charge='87');agent('-once')
        frozen=request('GET',f'/agents/{identity}/state',owner)['devices'][0]['reading']
        mode['available']=False;agent('-once');agent('-once')
        snapshot=request('GET',f'/agents/{identity}/state',owner)
        assert snapshot['devices'][0]['stale'] and snapshot['devices'][0]['reading']==frozen
        mode['available']=True;agent('-once')
        snapshot=request('GET',f'/agents/{identity}/state',owner)
        assert not snapshot['devices'][0]['stale']
        events=request('GET',f'/agents/{identity}/events',owner)['items']
        assert [e['data']['kind'] for e in events]==['on_battery','low_battery','power_restored','nut_server_unavailable','nut_server_reconnected']
        passed.append('Energy events and frozen last known state; exactly one loss and reconnect event')
        device=snapshot['devices'][0]['reading']['device_id']
        history=request('GET',f'/agents/{identity}/history?device_id={device}',owner)['items']
        assert len(history)>=4
        settings=json.loads((temp/'state/backend-connection.json').read_text())
        token=pathlib.Path(settings['token_file']).read_text().strip()
        source=snapshot['sources'][0]
        message={'schema_version':1,'agent_id':identity,'id':str(uuid.uuid4()),'sequence':snapshot['last_sequence']+1,'recorded_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'sources':[{'source_id':source['id'],'name':source['name'],'connection':'reachable','discovered_ups':['pilot_ups']}],'readings':[snapshot['devices'][0]['reading']],'events':[],'dropped_messages_total':0}
        body={'schema_version':1,'agent_id':identity,'messages':[message]}
        first=request('POST','/ingest',token,body);second=request('POST','/ingest',token,body)
        assert first==second=={'acknowledged_ids':[message['id']]}
        assert len(request('GET',f'/agents/{identity}/events',owner)['items'])==5
        passed.append('Durable acknowledgement is idempotent; short event history is retained')
        request('POST',f'/agents/{identity}/revoke',owner,{})
        agent('-once',success=False)
        assert json.loads(agent('-status'))['pending_messages']>0
        passed.append('Revocation blocks uploads without deleting the local pending queue')
finally:
    for workspace in workspaces:
        request('DELETE','/workspace',workspace['owner_token'],{})
    server.shutdown();server.server_close()
passed.append('Synthetic spaces and credentials removed; operator space preserved')
print(json.dumps({'passed':len(passed),'failed':0,'scenarios':passed},indent=2))
