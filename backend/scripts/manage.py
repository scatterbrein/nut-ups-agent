#!/usr/bin/env python3
"""Operator CLI. Credentials stay in a private file, never in arguments or output."""
import argparse,json,os,pathlib,stat,sys,urllib.error,urllib.parse,urllib.request,uuid

class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self,*args,**kwargs): return None

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--credentials',type=pathlib.Path,required=True)
    sub=parser.add_subparsers(dest='action',required=True)
    pair=sub.add_parser('pair');pair.add_argument('--name',required=True)
    sub.add_parser('agents')
    for action in ['state','events','history','revoke']:
        command=sub.add_parser(action);command.add_argument('--agent',required=True,type=uuid.UUID)
        if action=='history':command.add_argument('--device',required=True)
    create=sub.add_parser('create-workspace');create.add_argument('--name',required=True);create.add_argument('--save',required=True,type=pathlib.Path)
    args=parser.parse_args()
    fd=os.open(args.credentials,os.O_RDONLY|os.O_NONBLOCK)
    with os.fdopen(fd) as source:
        info=os.fstat(source.fileno())
        if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode)!=0o600 or info.st_size>8192:
            raise ValueError('Credentials must be a regular file with permission 0600')
        credentials=json.load(source)
    base=credentials.get('public_url','https://ups.omstudioit.com/api/v1').rstrip('/')
    parsed=urllib.parse.urlsplit(base)
    if parsed.scheme!='https' or not parsed.hostname or parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise ValueError('A clean HTTPS API URL is required')
    admin=args.action=='create-workspace'
    token=credentials['admin_token' if admin else 'owner_token']
    body=None;method='GET'
    if args.action=='pair':path='/pairings';method='POST';body={'name':args.name}
    elif admin:path='/admin/workspaces';method='POST';body={'name':args.name}
    elif args.action=='agents':path='/agents'
    else:
        path='/agents/'+str(args.agent)+'/'+args.action
        if args.action=='revoke':method='POST';body={}
        if args.action=='history':path+='?'+urllib.parse.urlencode({'device_id':args.device})
    # Reserve private output before provisioning so a path error cannot lose a newly issued token.
    target=None
    if admin:
        target=os.open(args.save,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
    try:
        request=urllib.request.Request(base+path,method=method,headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'},data=None if body is None else json.dumps(body).encode())
        with urllib.request.build_opener(NoRedirect).open(request,timeout=20) as response:
            raw=response.read(2*1024*1024+1)
            if len(raw)>2*1024*1024:raise ValueError('Response exceeded limit')
            result=json.loads(raw)
        if admin:
            result['public_url']=base
            with os.fdopen(target,'w') as output:
                target=None;json.dump(result,output,indent=2);output.flush();os.fsync(output.fileno())
            print('Workspace created; private credentials saved to '+str(args.save))
        else:print(json.dumps(result,indent=2,ensure_ascii=False))
    except BaseException:
        if target is not None:
            os.close(target);args.save.unlink(missing_ok=True)
        raise

if __name__=='__main__':
    try:main()
    except urllib.error.HTTPError as error:
        print('API rejected the operation (HTTP '+str(error.code)+').',file=sys.stderr);sys.exit(1)
    except (ValueError,KeyError,OSError):
        print('Could not complete the operation. Check the private credential file, connection and arguments.',file=sys.stderr);sys.exit(1)
