#!/usr/bin/env python3
"""Smoke-test the bundled HTTP adapter with private, disposable local state; no APNs or NUT traffic."""
import contextlib, hashlib, json, os, pathlib, secrets, socket, subprocess, tempfile, time, urllib.error, urllib.request, uuid
import argparse
parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--php',required=True);args=parser.parse_args()
root=pathlib.Path(__file__).resolve().parents[1]
with tempfile.TemporaryDirectory(prefix='ups-push-http-') as directory:
    private=pathlib.Path(directory);os.chmod(private,0o700)
    with contextlib.closing(socket.socket()) as probe:
        probe.bind(('127.0.0.1',0));port=probe.getsockname()[1]
    base=f'http://127.0.0.1:{port}/api/v1';secret=secrets.token_hex(32)
    env=dict(os.environ,UPS_STORAGE_DIR=str(private/'state'),UPS_BOOTSTRAP_HASH=hashlib.sha256(secret.encode()).hexdigest(),UPS_PUBLIC_URL=base,UPS_ALLOW_LOOPBACK_HTTP='1')
    with open(private/'server.log','wb') as log:
        process=subprocess.Popen([args.php,'-S',f'127.0.0.1:{port}',str(root/'dist/ups-api.php')],env=env,stdout=log,stderr=log)
        def request(method,path,token='',body=None,status=200):
            headers={'Content-Type':'application/json'}
            if token:headers['Authorization']='Bearer '+token
            req=urllib.request.Request(base+path,method=method,headers=headers,data=json.dumps(body).encode() if body is not None else None)
            try:response=urllib.request.urlopen(req,timeout=10)
            except urllib.error.HTTPError as error:response=error
            with response:
                assert response.status==status,f'{method} {path}: expected {status}, received {response.status}'
                assert 'no-store' in response.headers.get('Cache-Control','')
                return json.loads(response.read())
        try:
            for attempt in range(100):
                try:request('GET','/health');break
                except (urllib.error.URLError,ConnectionError):time.sleep(.05)
            owner=request('POST','/bootstrap',secret,{'name':'Disposable push HTTP test'})
            pairing=request('POST','/pairings',owner['owner_token'],{'name':'Synthetic agent'})
            agent=request('POST','/pairings/claim',body={'code':pairing['pairing_code'],'agent_id':str(uuid.uuid4())})
            pairing=request('POST','/mobile-pairings',owner['owner_token'],{})
            phone=request('POST','/mobile-pairings/claim',body={'code':pairing['pairing_code']})
            path='/push-subscriptions/'+agent['agent_id']
            result=request('GET',path,phone['access_token'])
            assert result['enabled'] is False and result['available']=={'sandbox':False,'production':False}
            request('POST',path,phone['access_token'],{'environment':'sandbox','device_token':'a1'*32,'kinds':'on_battery'},503)
            request('POST',path,agent['agent_token'],{},401)
            request('POST','/admin/push-drain',phone['access_token'],{},401)
            assert request('POST','/admin/push-drain',owner['admin_token'],{})['sent']==0
            assert request('DELETE',path,phone['access_token'],{})=={'enabled':False}
            assert request('GET','/push-subscriptions',phone['access_token'])=={'agents':[]}
            request('GET','/admin/apns',phone['access_token'],status=401)
            request('POST','/admin/apns',owner['owner_token'],{},401)
            pem=subprocess.run(['openssl','genpkey','-algorithm','EC','-pkeyopt','ec_paramgen_curve:P-256'],check=True,capture_output=True,text=True).stdout
            for environment in ['sandbox','production']:
                configured=request('POST','/admin/apns',owner['admin_token'],{'team_id':'86D2TRN5L5','topic':'local.UPSMonitor','environment':environment,'key_id':'TESTKEY123','private_key':pem})
                assert configured['configured'] and configured['ready']
            status=request('GET','/admin/apns',owner['admin_token'])
            assert all(status['environments'][env]['ready'] for env in ['sandbox','production'])
            assert 'PRIVATE KEY' not in json.dumps(status) and 'key_path' not in json.dumps(status)
            request('DELETE','/mobile-session',phone['access_token'],{})
            request('GET',path,phone['access_token'],status=401)
            installation={'id':str(uuid.uuid4()),'agent_id':str(uuid.uuid4()),'name':'Synthetic installation','proof':secrets.token_hex(32),'code':'00123456','agent_token':'ups_agent_'+secrets.token_urlsafe(32)}
            request('POST','/installations',body=installation)
            assert request('POST','/installations/status',body={'id':installation['id'],'proof':installation['proof']})['linked'] is False
            claim={'id':installation['id'],'code':installation['code'],'mobile_token':'ups_mobile_'+secrets.token_urlsafe(32),'consent':False}
            request('POST','/installations/claim',body=claim,status=422)
            claim['consent']=True
            linked=request('POST','/installations/claim',body=claim)
            assert request('POST','/installations/claim',body=claim)['session_id']==linked['session_id']
            assert request('POST','/installations/status',body={'id':installation['id'],'proof':installation['proof']})['linked'] is True
            assert request('GET','/workspace-management',linked['access_token'])['can_manage'] is True
            invited_code=request('POST','/mobile-pairings',linked['access_token'],{})
            invited=request('POST','/mobile-pairings/claim',body={'code':invited_code['pairing_code']})
            assert request('GET','/workspace-management',invited['access_token'])['can_manage'] is False
            request('DELETE','/workspace',invited['access_token'],{},403)
            request('DELETE','/workspace',linked['access_token'],{})
            request('GET','/agents',invited['access_token'],status=401)
            print(json.dumps({'passed':19,'failed':0,'checks':['scoped opt-in status','unconfigured APNs does not enable alerts','agent cannot register a phone','mobile session cannot run worker','operator worker returns safe summary','idempotent disable','no device token in list','revocation invalidates access','private operator setup requires admin','Sandbox installed privately','Production installed privately','both configurations coexist','status excludes key material','installation remains inactive before consent','public setup enforces consent','claim retry reuses one session','agent receives no phone token','only installation creator can manage invitations','workspace deletion revokes invited phones']}))
        finally:
            process.terminate();process.wait(timeout=10)
