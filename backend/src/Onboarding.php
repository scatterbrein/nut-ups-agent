<?php
declare(strict_types=1);
namespace UPS;

/** Installation proof is private on the Pi; discovery exposes only a random ID.
 * A workspace is created only after explicit phone consent and the install code.
 * No admin/owner credential is distributed and no NUT credential crosses here.
 */
final class Onboarding {
    public function __construct(private Service $service) {}
    private function hex(mixed $value): string {
        if (!is_string($value)||!preg_match('/^[0-9a-f]{64}$/D',$value)) { throw new ApiError(422,'invalid_installation_proof'); }
        return $value;
    }
    private function credential(mixed $value,string $role): string {
        if (!is_string($value)||!preg_match('/^ups_'.$role.'_[A-Za-z0-9_-]{43}$/D',$value)) { throw new ApiError(422,'invalid_installation_credential'); }
        return $value;
    }
    public function register(array $body,int $now): array {
        Validate::keys($body,['id','agent_id','name','proof','code','agent_token']);
        $id=Validate::id($body['id']??null);$agent=Validate::id($body['agent_id']??null);
        $name=Validate::text($body['name']??null,80);$proof=$this->hex($body['proof']??null);
        $code=$body['code']??'';
        if (!is_string($code)||!preg_match('/^[0-9]{8}$/D',$code)) { throw new ApiError(422,'invalid_installation_code'); }
        $agentToken=$this->credential($body['agent_token']??null,'agent');
        return $this->service->db->transaction(function() use($id,$agent,$name,$proof,$code,$agentToken,$now): array {
            $db=$this->service->db;
            $old=$db->row('SELECT * FROM enrollments WHERE id=?',[$id]);
            if ($old) {
                if ($old['agent_id']!==$agent||!hash_equals($old['proof_hash'],hash('sha256',$proof))||!hash_equals($old['agent_token_hash'],hash('sha256',$agentToken))||!hash_equals($old['code_hash'],hash('sha256',$code))) { throw new ApiError(409,'installation_conflict'); }
                if ((int)$old['expires_at']<=$now) { throw new ApiError(410,'installation_expired'); }
                return ['id'=>$id,'expires_at'=>(int)$old['expires_at']];
            }
            $known=$db->row('SELECT * FROM agents WHERE id=?',[$agent]);
            if ($known && ($known['revoked_at']!==null || !hash_equals($known['token_hash']??'',hash('sha256',$agentToken)))) { throw new ApiError(409,'agent_already_linked'); }
            if (!$known && $db->row('SELECT id FROM enrollments WHERE agent_id=? AND expires_at>?',[$agent,$now])) { throw new ApiError(409,'agent_already_linked'); }
            // A server operator possessing the existing private agent credential
            // may prepare a new phone code without rotating or replacing it.
            if ($known) { $db->query('DELETE FROM enrollments WHERE agent_id=?',[$agent]); }
            $db->query('DELETE FROM enrollments WHERE claimed_at IS NULL AND expires_at<=?',[$now]);
            if ((int)$db->query('SELECT COUNT(*) FROM enrollments WHERE claimed_at IS NULL')->fetchColumn()>=500) { throw new ApiError(429,'installation_capacity'); }
            $db->query('INSERT INTO enrollments(id,agent_id,name,proof_hash,code_hash,agent_token_hash,expires_at) VALUES(?,?,?,?,?,?,?)',[$id,$agent,$name,hash('sha256',$proof),hash('sha256',$code),hash('sha256',$agentToken),$now+1800]);
            if ($known) { $db->query('UPDATE enrollments SET workspace_id=? WHERE id=?',[$known['workspace_id'],$id]); }
            return ['id'=>$id,'expires_at'=>$now+1800];
        });
    }
    public function status(array $body,int $now): array {
        Validate::keys($body,['id','proof']);$id=Validate::id($body['id']??null);$proof=$this->hex($body['proof']??null);
        $row=$this->service->db->row('SELECT * FROM enrollments WHERE id=?',[$id]);
        if (!$row||!hash_equals($row['proof_hash'],hash('sha256',$proof))) { throw new ApiError(401,'invalid_installation'); }
        if ($row['claimed_at']!==null) {
            $agent=$this->service->db->row('SELECT id FROM agents WHERE id=? AND revoked_at IS NULL AND token_hash=?',[$row['agent_id'],$row['agent_token_hash']]);
            if (!$agent) { throw new ApiError(401,'invalid_installation'); }
            return ['linked'=>true,'agent_id'=>$row['agent_id']];
        }
        if ((int)$row['expires_at']<=$now) { throw new ApiError(410,'installation_expired'); }
        return ['linked'=>false,'agent_id'=>$row['agent_id']];
    }
    public function claim(array $body,int $now): array {
        Validate::keys($body,['id','code','mobile_token','consent']);
        if (($body['consent']??null)!==true) { throw new ApiError(422,'remote_consent_required'); }
        $id=Validate::id($body['id']??null);$code=$body['code']??'';
        if (!is_string($code)||!preg_match('/^[0-9]{8}$/D',$code)) { throw new ApiError(401,'invalid_installation_code'); }
        $token=$this->credential($body['mobile_token']??null,'mobile');
        // Commit failed-attempt counters even when returning an authentication error.
        $result=$this->service->db->transaction(function() use($id,$code,$token,$now): array {
            $db=$this->service->db;$row=$db->row('SELECT * FROM enrollments WHERE id=?',[$id]);
            if (!$row||(int)$row['expires_at']<=$now||(int)$row['attempts']>=5) { return ['error'=>true]; }
            if (!hash_equals($row['code_hash'],hash('sha256',$code))) {
                $db->query('UPDATE enrollments SET attempts=attempts+1 WHERE id=?',[$id]);return ['error'=>true];
            }
            if ($row['claimed_at']!==null) {
                if (!hash_equals($row['mobile_token_hash']??'',hash('sha256',$token))) { return ['error'=>true]; }
                $session=$db->row('SELECT * FROM mobile_sessions WHERE id=? AND expires_at>?',[$row['session_id'],$now]);
                if (!$session) { return ['error'=>true]; }
                return $this->receipt($row,$token,(int)$session['expires_at']);
            }
            // Limit completed public registrations, rather than only pending installs.
            $db->rate(hash('sha256','onboarding-workspaces'),100,$now,86400);
            $workspace=$row['workspace_id']!==null?['workspace_id'=>$row['workspace_id']]:$this->service->createWorkspace($row['name'],$now);
            $session=Service::uuid();$expires=$now+90*86400;
            if ($row['workspace_id']===null) { $db->query('INSERT INTO agents(id,workspace_id,name,token_hash,created_at) VALUES(?,?,?,?,?)',[$row['agent_id'],$workspace['workspace_id'],$row['name'],$row['agent_token_hash'],$now]); }
            else {
                $existing=$db->row('SELECT * FROM agents WHERE id=? AND revoked_at IS NULL',[$row['agent_id']]);
                if (!$existing||!hash_equals($existing['token_hash']??'',$row['agent_token_hash'])) { throw new ApiError(401,'invalid_installation'); }
                if ((int)$db->query('SELECT COUNT(*) FROM mobile_sessions WHERE workspace_id=? AND expires_at>?',[$workspace['workspace_id'],$now])->fetchColumn()>=20) { throw new ApiError(409,'device_limit'); }
            }
            $db->query('INSERT INTO mobile_sessions VALUES(?,?,?,?,?)',[$session,$workspace['workspace_id'],hash('sha256',$token),$now,$expires]);
            $db->query('INSERT INTO workspace_managers VALUES(?,?)',[$session,$workspace['workspace_id']]);
            $db->query('UPDATE enrollments SET workspace_id=?,session_id=?,mobile_token_hash=?,claimed_at=? WHERE id=?',[$workspace['workspace_id'],$session,hash('sha256',$token),$now,$id]);
            $row['workspace_id']=$workspace['workspace_id'];$row['session_id']=$session;
            return $this->receipt($row,$token,$expires);
        });
        if (isset($result['error'])) { throw new ApiError(401,'invalid_or_expired_installation'); }
        return $result;
    }
    private function receipt(array $row,string $token,int $expires): array {
        return ['agent_id'=>$row['agent_id'],'workspace_id'=>$row['workspace_id'],'session_id'=>$row['session_id'],'access_token'=>$token,'expires_at'=>gmdate('Y-m-d\TH:i:s\Z',$expires)];
    }
}
