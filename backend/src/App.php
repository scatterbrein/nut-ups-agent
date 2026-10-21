<?php
declare(strict_types=1);
namespace UPS;

require_once __DIR__.'/Database.php';
require_once __DIR__.'/Validate.php';
require_once __DIR__.'/Push.php';
require_once __DIR__.'/Service.php';
require_once __DIR__.'/Onboarding.php';

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
        if ($method==='POST' && in_array($path,['/installations','/installations/status','/installations/claim'],true)) {
            $this->service->rate('installation:'.$peer,20,$now);
            $this->service->rate('installation-global',300,$now);
            $onboarding=new Onboarding($this->service);
            return match($path) {
                '/installations'=>$onboarding->register($body,$now),
                '/installations/status'=>$onboarding->status($body,$now),
                '/installations/claim'=>$onboarding->claim($body,$now)
            };
        }
        if ($method==='POST' && $path==='/mobile-pairings/claim') {
            $this->service->rate('mobile-claim-global',100,$now);$this->service->rate('mobile-claim:'.$peer,8,$now);
            Validate::keys($body,['code']);return $this->service->claimMobile(Validate::text($body['code']??null,32),$now);
        }
        if ($method==='POST' && $path==='/pairings/claim') {
            $this->service->rate('claim-global',100,$now);
            $this->service->rate('claim:'.$peer,8,$now);
            Validate::keys($body,['code','agent_id']);
            return $this->service->claim(Validate::text($body['code']??null,32),Validate::id($body['agent_id']??null),$now);
        }
        $role=$path==='/ingest'?'agent':(str_starts_with($path,'/admin/')?'admin':'owner');
        try { $identity=$this->service->authenticate($token,$role,$now); }
        catch (ApiError $e) { $this->service->rate('auth-failed:'.$peer,60,$now);throw $e; }
        $this->service->rate($role.':'.hash('sha256',$token),120,$now);
        if (isset($identity['mobile_session_id'])) {
            $installationOwner=$this->service->db->row('SELECT session_id FROM workspace_managers WHERE workspace_id=? AND session_id=?',[$identity['id'],$identity['mobile_session_id']])!==null;
            if ($method==='GET' && $path==='/workspace-management') { return ['can_manage'=>$installationOwner]; }
            if ($installationOwner && $method==='POST' && $path==='/mobile-pairings') { Validate::keys($body,[]);return $this->service->mobilePairing($identity['id'],$now); }
            if ($installationOwner && $method==='DELETE' && $path==='/workspace') {
                Validate::keys($body,[]);$this->service->db->transaction(fn()=>$this->service->db->query('DELETE FROM workspaces WHERE id=?',[$identity['id']]));return ['deleted'=>true];
            }
            if ($method==='GET' && $path==='/push-subscriptions') {
                return ['agents'=>array_column($this->service->db->query('SELECT agent_id FROM push_subscriptions WHERE session_id=?',[$identity['mobile_session_id']])->fetchAll(),'agent_id')];
            }
            if ($method==='DELETE' && $path==='/mobile-session') {
                Validate::keys($body,[]);$this->service->db->transaction(fn()=>$this->service->db->query('DELETE FROM mobile_sessions WHERE id=?',[$identity['mobile_session_id']]));return ['deleted'=>true];
            }
            if (preg_match('~^/push-subscriptions/([0-9a-f-]{36})$~D',$path,$push)) {
                return $this->service->push->subscription($identity['id'],$identity['mobile_session_id'],$push[1],$method,$body,$now);
            }
            if ($method!=='GET' && !($method==='POST' && $path==='/pairings')) { throw new ApiError(403,'read_only_mobile_session'); }
        }
        if ($method==='POST' && $path==='/mobile-pairings') { Validate::keys($body,[]);return $this->service->mobilePairing($identity['id'],$now); }
        if ($method==='GET' && $path==='/mobile-sessions') { return ['sessions'=>$this->service->db->query('SELECT id,created_at,expires_at FROM mobile_sessions WHERE workspace_id=?',[$identity['id']])->fetchAll()]; }
        if ($method==='DELETE' && preg_match('~^/mobile-sessions/([0-9a-f-]{36})$~D',$path,$m)) {
            Validate::keys($body,[]);$this->service->db->transaction(fn()=>$this->service->db->query('DELETE FROM mobile_sessions WHERE id=? AND workspace_id=?',[$m[1],$identity['id']]));return ['deleted'=>true];
        }
        if ($method==='POST' && $path==='/ingest') { return $this->service->ingest($identity['id'],$body,$now,hash('sha256',$token)); }
        if ($path==='/admin/apns' && $method==='GET') { return $this->service->push->operatorStatus(); }
        if ($path==='/admin/apns' && $method==='POST') { $this->service->rate('apns-config',6,$now);return $this->service->push->configure($body); }
        if ($path==='/admin/apns/probe' && $method==='POST') {
            $this->service->rate('apns-probe',6,$now);Validate::keys($body,['environment']);
            return $this->service->push->probe(Validate::text($body['environment']??null,20),$now);
        }
        if ($method==='POST' && $path==='/admin/push-drain') { Validate::keys($body,[]);return $this->service->push->drain($now); }
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
