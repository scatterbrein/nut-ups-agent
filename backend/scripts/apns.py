#!/usr/bin/env python3
"""Operator-only APNs setup. Private keys travel only over HTTPS to the configured pilot backend."""
import argparse,json,os,pathlib,stat,sys,urllib.error,urllib.parse,urllib.request
class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self,*args,**kwargs):return None

def private_file(path,limit):
    with open(path,'rb') as source:
        info=os.fstat(source.fileno())
        if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode)!=0o600 or info.st_size>limit:
            raise ValueError('A regular private file with mode 0600 is required')
        return source.read(limit+1)

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--credentials',type=pathlib.Path,required=True)
    commands=parser.add_subparsers(dest='action',required=True)
    commands.add_parser('status');commands.add_parser('drain')
    install=commands.add_parser('configure');install.add_argument('--config',type=pathlib.Path,required=True)
    probe=commands.add_parser('probe');probe.add_argument('--environment',choices=['sandbox','production'],required=True)
    args=parser.parse_args();credentials=json.loads(private_file(args.credentials,8192))
    base=credentials['public_url'].rstrip('/')
    # This operation must not transmit APNs secrets to an arbitrary redirected or supplied host.
    if base!='https://ups.omstudioit.com/api/v1':raise ValueError('Unexpected backend destination')
    token=credentials['admin_token']
    def request(path,body=None):
        req=urllib.request.Request(base+path,method='GET' if body is None else 'POST',
            headers={'Content-Type':'application/json','Authorization':'Bearer '+token},
            data=None if body is None else json.dumps(body).encode())
        with urllib.request.build_opener(NoRedirect).open(req,timeout=20) as response:
            raw=response.read(65537)
            if len(raw)>65536:raise ValueError('Response exceeded limit')
            return json.loads(raw)
    if args.action=='configure':
        config=json.loads(private_file(args.config,8192))
        if config.get('team_id')!='86D2TRN5L5' or config.get('topic')!='local.UPSMonitor':raise ValueError('Wrong APNs app identity')
        for environment in ['sandbox','production']:
            key=config[environment];pem=private_file(key['key_path'],4096).decode('ascii')
            result=request('/admin/apns',{'team_id':config['team_id'],'topic':config['topic'],'environment':environment,'key_id':key['key_id'],'private_key':pem})
            print(json.dumps(result))
    elif args.action=='status':print(json.dumps(request('/admin/apns'),indent=2))
    elif args.action=='drain':print(json.dumps(request('/admin/push-drain',{})))
    else:print(json.dumps(request('/admin/apns/probe',{'environment':args.environment}),indent=2))
if __name__=='__main__':
    try:main()
    except urllib.error.HTTPError as error:
        print('APNs operation rejected (HTTP '+str(error.code)+'). No credentials were printed.',file=sys.stderr);sys.exit(1)
    except (ValueError,KeyError,OSError,UnicodeError):
        print('APNs operation incomplete. Check private files and connectivity; no credentials were printed.',file=sys.stderr);sys.exit(1)
