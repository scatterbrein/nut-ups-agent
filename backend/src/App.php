<?php
declare(strict_types=1);
namespace UPS;

require_once __DIR__.'/Database.php';
require_once __DIR__.'/Validate.php';
require_once __DIR__.'/Service.php';

final class App {
    public readonly Service $service;
    public function __construct(array $config) {
        $this->service=new Service(new Database($config['storage_dir']),$config);
    }

    /** Transport-independent entry point used by the HTTP adapter and security/contract tests. */
    public function handle(string $method,string $path,string $token,array $body,array $query,string $peer,int $now): array {
        if ($method==='GET' && $path==='/health') { return ['service'=>'nut-ups-monitor-api','api_version'=>1,'status'=>$this->service->initialized()?'ok':'setup_required']; }
        if ($method==='POST' && $path==='/bootstrap') {
            $this->service->rate('bootstrap:'.$peer,5,$now);Validate::keys($body,['name']);
            return $this->service->bootstrap($token,Validate::text($body['name']??null,80),$now);
        }
        if (!$this->service->initialized()) { throw new ApiError(503,'setup_required'); }
        if ($method==='POST' && $path==='/pairings/claim') {
            $this->service->rate('claim-global',100,$now);
            $this->service->rate('claim:'.$peer,8,$now);
            Validate::keys($body,['code','agent_id']);
            return $this->service->claim(Validate::text($body['code']??null,32),Validate::id($body['agent_id']??null),$now);
        }
        $role=$path==='/ingest'?'agent':(str_starts_with($path,'/admin/')?'admin':'owner');
        try { $identity=$this->service->authenticate($token,$role); }
        catch (ApiError $e) { $this->service->rate('auth-failed:'.$peer,60,$now);throw $e; }
        $this->service->rate($role.':'.hash('sha256',$token),120,$now);
        if ($method==='POST' && $path==='/ingest') { return $this->service->ingest($identity['id'],$body,$now,hash('sha256',$token)); }
        if ($method==='POST' && $path==='/admin/workspaces') {
            Validate::keys($body,['name']);$name=Validate::text($body['name']??null,80);
            return $this->service->db->transaction(fn()=>$this->service->createWorkspace($name,$now));
        }
        if ($method==='POST' && $path==='/pairings') {
            Validate::keys($body,['name']);return $this->service->pairing($identity['id'],Validate::text($body['name']??null,80),$now);
        }
        if ($method==='GET' && $path==='/agents') {
            return ['agents'=>$this->service->db->query('SELECT id,name,created_at,revoked_at,last_seen,last_sequence FROM agents WHERE workspace_id=? ORDER BY created_at,id',[$identity['id']])->fetchAll()];
        }
        if (preg_match('~^/agents/([0-9a-f-]{36})/(state|history|events|revoke)$~D',$path,$match)) {
            [$unused,$agent,$action]=$match;
            if ($method==='GET' && $action==='state') { return $this->service->agentState($identity['id'],$agent,$now); }
            if ($method==='GET' && in_array($action,['history','events'],true)) { return $this->service->history($identity['id'],$agent,$action,$query,$now); }
            if ($method==='POST' && $action==='revoke') { Validate::keys($body,[]);$this->service->revoke($identity['id'],$agent,$now);return ['revoked'=>true]; }
        }
        if ($method==='DELETE' && $path==='/workspace') {
            Validate::keys($body,[]);
            $this->service->db->transaction(fn()=>$this->service->db->query('DELETE FROM workspaces WHERE id=?',[$identity['id']]));
            return ['deleted'=>true];
        }
        throw new ApiError(404,'not_found');
    }
}
