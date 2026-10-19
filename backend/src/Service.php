<?php
declare(strict_types=1);
namespace UPS;

final class Service {
    public function __construct(public readonly Database $db, private readonly array $config) {}

    public static function uuid(): string {
        $b=random_bytes(16);$b[6]=chr((ord($b[6])&15)|64);$b[8]=chr((ord($b[8])&63)|128);$s=bin2hex($b);
        return substr($s,0,8).'-'.substr($s,8,4).'-'.substr($s,12,4).'-'.substr($s,16,4).'-'.substr($s,20);
    }
    public static function token(string $role): string { return 'ups_'.$role.'_'.rtrim(strtr(base64_encode(random_bytes(32)),'+/','-_'),'='); }
    public static function json(mixed $value): string { return json_encode($value,JSON_THROW_ON_ERROR|JSON_UNESCAPED_SLASHES|JSON_UNESCAPED_UNICODE); }

    public function initialized(): bool { return $this->db->row("SELECT value FROM meta WHERE key='initialized'") !== null; }
    public function rate(string $key, int $limit, int $now): void {
        $this->db->transaction(fn()=>$this->db->rate(hash('sha256',$key),$limit,$now));
    }
    public function authenticate(string $token, string $role): array {
        if (!preg_match('/^ups_'.preg_quote($role,'/').'_[A-Za-z0-9_-]{43}$/D',$token)) { throw new ApiError(401,'unauthorized'); }
        $hash=hash('sha256',$token);
        $row=match($role) {
            'owner'=>$this->db->row('SELECT * FROM workspaces WHERE owner_hash=?',[$hash]),
            'agent'=>$this->db->row('SELECT * FROM agents WHERE token_hash=? AND revoked_at IS NULL',[$hash]),
            'admin'=>$this->db->row("SELECT value AS admin_hash FROM meta WHERE key='admin_hash'"),
            default=>null,
        };
        if (!$row || !hash_equals($row[$role.'_hash']??$row['token_hash']??'', $hash)) { throw new ApiError(401,'unauthorized'); }
        return $row;
    }

    public function bootstrap(string $token, string $name, int $now): array {
        $expected=$this->config['bootstrap_hash']??'';
        if (strlen($expected)!==64 || !hash_equals($expected,hash('sha256',$token))) { throw new ApiError(401,'unauthorized'); }
        return $this->db->transaction(function() use($name,$now): array {
            if ($this->initialized()) { throw new ApiError(409,'already_initialized'); }
            $admin=self::token('admin');$workspace=$this->createWorkspace($name,$now);
            $this->db->query("INSERT INTO meta VALUES('admin_hash',?)",[hash('sha256',$admin)]);
            $this->db->query("INSERT INTO meta VALUES('initialized','1')");
            return ['admin_token'=>$admin]+$workspace;
        });
    }
    public function createWorkspace(string $name, int $now): array {
        Validate::text($name,80);$owner=self::token('owner');$id=self::uuid();
        $this->db->query('INSERT INTO workspaces VALUES(?,?,?,?)',[$id,$name,hash('sha256',$owner),$now]);
        return ['workspace_id'=>$id,'name'=>$name,'owner_token'=>$owner];
    }
    public function pairing(string $workspace, string $name, int $now): array {
        Validate::text($name,80);$code=strtoupper(bin2hex(random_bytes(8)));
        $display=implode('-',str_split($code,4));
        return $this->db->transaction(function() use($workspace,$name,$now,$code,$display): array {
            $this->db->query('INSERT INTO pairings VALUES(?,?,?,?,NULL)',[hash('sha256',$code),$workspace,$name,$now+600]);
            return ['pairing_code'=>$display,'expires_at'=>gmdate('Y-m-d\TH:i:s\Z',$now+600)];
        });
    }
    public function claim(string $code, string $agent, int $now): array {
        Validate::id($agent);$code=strtoupper(str_replace('-','',$code));
        if (!preg_match('/^[0-9A-F]{16}$/D',$code)) { throw new ApiError(401,'invalid_or_expired_pairing'); }
        return $this->db->transaction(function() use($code,$agent,$now): array {
            $pair=$this->db->row('SELECT * FROM pairings WHERE code_hash=? AND used_at IS NULL AND expires_at>?',[hash('sha256',$code),$now]);
            if (!$pair) { throw new ApiError(401,'invalid_or_expired_pairing'); }
            $old=$this->db->row('SELECT * FROM agents WHERE id=?',[$agent]);
            if ($old && $old['workspace_id']!==$pair['workspace_id']) { throw new ApiError(409,'agent_cannot_be_paired'); }
            $token=self::token('agent');
            if ($old) { $this->db->query('UPDATE agents SET name=?, token_hash=?, revoked_at=NULL WHERE id=?',[$pair['name'],hash('sha256',$token),$agent]); }
            else { $this->db->query('INSERT INTO agents(id,workspace_id,name,token_hash,created_at) VALUES(?,?,?,?,?)',[$agent,$pair['workspace_id'],$pair['name'],hash('sha256',$token),$now]); }
            $this->db->query('UPDATE pairings SET used_at=? WHERE code_hash=?',[$now,hash('sha256',$code)]);
            return ['agent_id'=>$agent,'agent_token'=>$token,'endpoint'=>rtrim($this->config['public_url'],'/').'/ingest'];
        });
    }
    public function ownedAgent(string $workspace,string $agent): array {
        Validate::id($agent);$row=$this->db->row('SELECT * FROM agents WHERE id=? AND workspace_id=?',[$agent,$workspace]);
        if (!$row) { throw new ApiError(404,'not_found'); } return $row;
    }
    public function revoke(string $workspace,string $agent,int $now): void {
        $this->db->transaction(function() use($workspace,$agent,$now): void { $this->ownedAgent($workspace,$agent);$this->db->query('UPDATE agents SET token_hash=NULL,revoked_at=? WHERE id=?',[$now,$agent]); });
    }

    public function ingest(string $agent, array $body, int $now, string $tokenHash): array {
        Validate::keys($body,['schema_version','agent_id','messages']);
        if (($body['schema_version']??null)!==1 || ($body['agent_id']??null)!==$agent) { throw new ApiError(422,'agent_or_schema_mismatch'); }
        $messages=[];$ids=[];$sequences=[];
        foreach (Validate::list($body['messages']??null,100) as $input) {
            $message=Validate::message($input,$agent);
            if (isset($ids[$message['id']]) || isset($sequences[$message['sequence']])) { throw new ApiError(422,'duplicate_message'); }
            $ids[$message['id']]=true;$sequences[$message['sequence']]=true;
            $messages[]=['message'=>$message,'hash'=>hash('sha256',Validate::canonical($input))];
        }
        if (!$messages) { throw new ApiError(422,'missing_messages'); }
        return $this->db->transaction(function() use($messages,$agent,$now,$tokenHash): array {
            // Revocation may have happened after the initial authentication lookup.
            $account=$this->db->row('SELECT * FROM agents WHERE id=? AND revoked_at IS NULL',[$agent]);
            if (!$account || !hash_equals($account['token_hash']??'', $tokenHash)) { throw new ApiError(401,'unauthorized'); }
            $ack=[];
            foreach ($messages as $entry) {
                $m=$entry['message'];$hash=$entry['hash'];$seq=$m['sequence'];$id=$m['id'];
                $previous=$this->db->row('SELECT * FROM messages WHERE agent_id=? AND (id=? OR sequence=?)',[$agent,$id,$seq]);
                if ($previous) {
                    if ($previous['id']!==$id || (int)$previous['sequence']!==$seq || !hash_equals($previous['payload_hash'],$hash)) { throw new ApiError(409,'message_identity_conflict'); }
                    $ack[]=$id;continue;
                }
                $this->db->query('INSERT INTO messages VALUES(?,?,?,?,?)',[$agent,$id,$seq,$hash,$now]);
                [, $recorded]=Validate::timestamp($m['recorded_at']);
                foreach ($m['sources'] as $s) {
                    $source=$s['source_id'];
                    $this->db->query('INSERT INTO sources VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(agent_id,id) DO UPDATE SET name=excluded.name,connection=excluded.connection,error_code=excluded.error_code,inventory_json=excluded.inventory_json,unavailable_json=excluded.unavailable_json,sequence=excluded.sequence,recorded_at=excluded.recorded_at WHERE excluded.sequence>sources.sequence',[$agent,$source,$s['name']??'',$s['connection'],$s['error_code']??null,self::json($s['discovered_ups']??null),self::json($s['unavailable_ups']??[]),$seq,$recorded]);
                    $this->db->query('UPDATE devices SET stale=1,state_sequence=? WHERE agent_id=? AND source_id=? AND state_sequence<?',[$seq,$agent,$source,$seq]);
                }
                $fresh=[];
                foreach (['readings'=>false,'last_known_readings'=>true] as $field=>$stale) {
                    foreach ($m[$field] as $r) {
                        $device=$r['device_id'];[, $observed]=Validate::timestamp($r['observed_at']);$json=self::json($r);
                        $existing=$this->db->row('SELECT id FROM devices WHERE agent_id=? AND source_id=? AND ups_key=?',[$agent,$r['source_id'],$r['ups_key']]);
                        if ($existing && $existing['id']!==$device) { throw new ApiError(409,'device_identity_conflict'); }
                        $existing=$this->db->row('SELECT source_id,ups_key FROM devices WHERE agent_id=? AND id=?',[$agent,$device]);
                        if ($existing && ($existing['source_id']!==$r['source_id'] || $existing['ups_key']!==$r['ups_key'])) { throw new ApiError(409,'device_identity_conflict'); }
                        $this->db->query('INSERT INTO devices VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(agent_id,id) DO UPDATE SET reading_json=CASE WHEN excluded.reading_sequence>=devices.reading_sequence THEN excluded.reading_json ELSE devices.reading_json END,observed_at=CASE WHEN excluded.reading_sequence>=devices.reading_sequence THEN excluded.observed_at ELSE devices.observed_at END,reading_sequence=MAX(devices.reading_sequence,excluded.reading_sequence),stale=CASE WHEN excluded.state_sequence>=devices.state_sequence THEN excluded.stale ELSE devices.stale END,state_sequence=MAX(devices.state_sequence,excluded.state_sequence)',[$agent,$device,$r['source_id'],$r['ups_key'],$json,$observed,$seq,$seq,(int)$stale]);
                        if (!$stale) { $fresh[$device]=$r;$this->sample($agent,$r,'minute:'.intdiv($observed,60),$seq,$now); }
                    }
                }
                foreach ($m['events'] as $e) {
                    [, $observed]=Validate::timestamp($e['observed_at']);
                    if ($observed<$now-7*86400) { continue; }
                    $old=$this->db->row('SELECT event_json FROM events WHERE agent_id=? AND event_id=?',[$agent,$e['id']]);
                    if ($old && Validate::canonical(json_decode($old['event_json'],true,32,JSON_THROW_ON_ERROR))!==Validate::canonical($e)) { throw new ApiError(409,'event_identity_conflict'); }
                    $this->db->query('INSERT OR IGNORE INTO events(agent_id,event_id,event_json,observed_at) VALUES(?,?,?,?)',[$agent,$e['id'],self::json($e),$observed]);
                    if (isset($e['device_id'],$fresh[$e['device_id']])) { $this->sample($agent,$fresh[$e['device_id']],'event:'.$e['id'],$seq,$now); }
                }
                $this->db->query('UPDATE agents SET last_sequence=?,last_recorded_at=? WHERE id=? AND last_sequence<?',[$seq,$recorded,$agent,$seq]);
                $ack[]=$id;
            }
            $this->db->query('UPDATE agents SET last_seen=? WHERE id=?',[$now,$agent]);
            $this->db->cleanup($now);
            return ['acknowledged_ids'=>$ack];
        });
    }

    private function sample(string $agent,array $r,string $key,int $sequence,int $now): void {
        [, $observed]=Validate::timestamp($r['observed_at']);if ($observed<$now-7*86400) { return; }
        $this->db->query('INSERT INTO samples(agent_id,device_id,sample_key,reading_json,observed_at,sequence) VALUES(?,?,?,?,?,?) ON CONFLICT(agent_id,device_id,sample_key) DO UPDATE SET reading_json=excluded.reading_json,observed_at=excluded.observed_at,sequence=excluded.sequence WHERE excluded.sequence>samples.sequence',[$agent,$r['device_id'],$key,self::json($r),$observed,$sequence]);
    }

    public function agentState(string $workspace,string $agent,int $now): array {
        $a=$this->ownedAgent($workspace,$agent);$available=$a['revoked_at']===null && $a['last_seen']!==null && $now-(int)$a['last_seen']<=90;
        $sources=$this->db->query('SELECT id,name,connection,error_code,sequence,recorded_at,inventory_json,unavailable_json FROM sources WHERE agent_id=? ORDER BY id',[$agent])->fetchAll();
        $active=[];
        foreach($sources as &$s){$s['active']=(int)$s['sequence']===(int)$a['last_sequence'];$active[$s['id']]=$s['active'];$s['stale']=!$available||!$s['active']||$now-(int)$s['recorded_at']>90||(int)$s['recorded_at']>$now+300;$s['discovered_ups']=json_decode($s['inventory_json'],true);$s['unavailable_ups']=json_decode($s['unavailable_json'],true);unset($s['inventory_json'],$s['unavailable_json']);}unset($s);
        $devices=[];
        foreach ($this->db->query('SELECT * FROM devices WHERE agent_id=? ORDER BY source_id,ups_key',[$agent])->fetchAll() as $d) {
            $clockSkew=(int)$d['observed_at']>$now+300;$age=max(0,$now-(int)$d['observed_at']);
            $devices[]=['reading'=>json_decode($d['reading_json'],true),'stale'=>(bool)$d['stale']||!$available||!($active[$d['source_id']]??false)||$age>90||$clockSkew,'age_seconds'=>$age,'clock_skew'=>$clockSkew];
        }
        return ['agent_id'=>$agent,'name'=>$a['name'],'agent_available'=>$available,'revoked'=>$a['revoked_at']!==null,'last_seen'=>$a['last_seen'],'last_sequence'=>(int)$a['last_sequence'],'sources'=>$sources,'devices'=>$devices];
    }
    public function history(string $workspace,string $agent,string $kind,array $query,int $now): array {
        $this->ownedAgent($workspace,$agent);Validate::keys($query,['device_id','after','cursor','limit']);
        $number=static function(mixed $v,int $default,int $max): int { if($v===null){return $default;}if(!is_string($v)||!preg_match('/^\d{1,12}$/D',$v)||(int)$v>$max){throw new ApiError(422,'invalid_query');}return (int)$v;};
        $after=max($now-7*86400,$number($query['after']??null,$now-86400,4102444800));$cursor=$number($query['cursor']??null,0,PHP_INT_MAX);$limit=max(1,$number($query['limit']??null,200,1000));
        $table=$kind==='history'?'samples':'events';$column=$kind==='history'?'reading_json':'event_json';$params=[$agent,$after,$cursor];$deviceClause='';
        if ($kind==='history') { $device=Validate::hash($query['device_id']??null);$deviceClause=' AND device_id=?';$params[]=$device; }
        $rows=$this->db->query("SELECT id,$column FROM $table WHERE agent_id=? AND observed_at>=? AND id>?$deviceClause ORDER BY id LIMIT ".($limit+1),$params)->fetchAll();
        $more=count($rows)>$limit;if($more){array_pop($rows);}
        return ['items'=>array_map(static fn($r)=>['cursor'=>(int)$r['id'],'data'=>json_decode($r[$column],true)],$rows),'next_cursor'=>$more?(int)$rows[array_key_last($rows)]['id']:null];
    }
}
