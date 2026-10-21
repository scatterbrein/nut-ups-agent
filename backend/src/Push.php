<?php
declare(strict_types=1);
namespace UPS;

/** Optional APNs delivery. Only a mobile session can register its own subscriptions. */
final class Push {
    public const KINDS=['on_battery','power_restored','low_battery','voltage_drop'];
    private array $config;
    public function __construct(private readonly Database $db,array $config) {
        $this->config=$config;
        // The APNs configuration and keys live outside public_html, beside the private database.
        $path=$config['storage_dir'].'/apns/config.json';
        if (!isset($config['apns']) && is_file($path) && !is_link($path)) {
            try {
                $value=json_decode(file_get_contents($path),true,8);
                if (is_array($value)) { $this->config['apns']=$value; }
            } catch (\Throwable) { /* Optional push setup must not interrupt monitoring. */ }
        }
    }
    public function ready(string $environment): bool {
        $c=$this->config['apns']??[];$key=$c[$environment]??[];
        return preg_match('/^[A-Z0-9]{10}$/D',$c['team_id']??'')===1 && ($c['topic']??'')==='local.UPSMonitor'
            && preg_match('/^[A-Z0-9]{10}$/D',$key['key_id']??'')===1 && is_file($key['key_path']??'')
            && !is_link($key['key_path']) && function_exists('openssl_sign') && function_exists('curl_init')
            && (curl_version()['features'] & CURL_VERSION_HTTP2)!==0;
    }
    /** Operator-only HTTPS provisioning. The HTTP adapter never logs request bodies. */
    public function configure(array $body): array {
        Validate::keys($body,['team_id','topic','environment','key_id','private_key']);
        $env=$body['environment']??null;$id=$body['key_id']??null;$pem=$body['private_key']??null;
        if (($body['team_id']??null)!=='86D2TRN5L5'||($body['topic']??null)!=='local.UPSMonitor'
            || !in_array($env,['sandbox','production'],true)||!is_string($id)||!preg_match('/^[A-Z0-9]{10}$/D',$id)
            || !is_string($pem)||strlen($pem)>4096||!str_starts_with($pem,'-----BEGIN PRIVATE KEY-----')) { throw new ApiError(422,'invalid_apns_configuration'); }
        try { $key=openssl_pkey_get_private($pem);$details=$key?openssl_pkey_get_details($key):false; }
        catch (\Throwable) { $details=false; }
        if (!$details||($details['ec']['curve_name']??'')!=='prime256v1') { throw new ApiError(422,'invalid_apns_private_key'); }
        $directory=$this->config['storage_dir'].'/apns';
        if (is_link($directory)||(!is_dir($directory)&&!mkdir($directory,0700,true))) { throw new \RuntimeException('push_storage_unavailable'); }
        chmod($directory,0700);
        $lock=fopen($directory.'/configure.lock','c');
        if (!$lock||!flock($lock,LOCK_EX)) { throw new \RuntimeException('push_storage_unavailable'); }
        try {
            $path=$directory.'/config.json';
            if (is_link($path)) { throw new \RuntimeException('push_storage_unavailable'); }
            $previous=is_file($path)?json_decode(file_get_contents($path),true,8):[];
            $config=is_array($previous)?$previous:[];
            // Versioned private files keep an existing working key available for operator rollback.
            $keyPath=$directory.'/'.$env.'-'.$id.'-'.substr(hash('sha256',$pem),0,16).'.p8';
            $this->atomicPrivateFile($keyPath,$pem);
            $config['team_id']=$body['team_id'];$config['topic']=$body['topic'];
            $config[$env]=['key_id'=>$id,'key_path'=>$keyPath];
            $this->atomicPrivateFile($path,Service::json($config));
            $this->config['apns']=$config;
        } finally { flock($lock,LOCK_UN);fclose($lock); }
        return ['configured'=>true,'environment'=>$env,'key_id'=>$id,'ready'=>$this->ready($env)];
    }
    private function atomicPrivateFile(string $path,string $data): void {
        $temp=$path.'.'.bin2hex(random_bytes(8));$handle=fopen($temp,'x');
        if (!$handle) { throw new \RuntimeException('push_storage_unavailable'); }
        try {
            chmod($temp,0600);
            if (fwrite($handle,$data)!==strlen($data)||!fflush($handle)||!fsync($handle)) { throw new \RuntimeException('push_storage_unavailable'); }
            fclose($handle);$handle=null;
            if (!rename($temp,$path)) { throw new \RuntimeException('push_storage_unavailable'); }
        } finally { if (is_resource($handle)) { fclose($handle); } if (file_exists($temp)) { unlink($temp); } }
    }
    public function operatorStatus(): array {
        $result=[];
        foreach(['sandbox','production'] as $env) {
            $result[$env]=['configured'=>isset($this->config['apns'][$env]),'key_id'=>$this->config['apns'][$env]['key_id']??null,'ready'=>$this->ready($env)];
        }
        return ['environments'=>$result,'pending'=>(int)$this->db->query("SELECT COUNT(*) FROM push_jobs WHERE state='pending'")->fetchColumn(),
            'subscriptions'=>(int)$this->db->query('SELECT COUNT(*) FROM push_subscriptions')->fetchColumn()];
    }
    public function probe(string $environment,int $now): array {
        if (!in_array($environment,['sandbox','production'],true)) { throw new ApiError(422,'invalid_push_environment'); }
        if (!$this->ready($environment)) { throw new ApiError(503,'push_not_configured'); }
        // An unregistered random token exercises Apple authentication without notifying a person.
        $result=$this->send(['environment'=>$environment,'device_token'=>bin2hex(random_bytes(32)),'id'=>Service::uuid(),
            'payload'=>Service::json(['aps'=>['alert'=>['title'=>'NUT connection check','body'=>'Synthetic unregistered token.']]]),'expires_at'=>0],$now);
        return ['environment'=>$environment,'status'=>$result['status'],'reason'=>$result['reason'],'iphone_delivery_tested'=>false];
    }

    public function subscription(string $workspace,string $session,string $agent,string $method,array $body,int $now): array {
        Validate::id($agent);
        if (!in_array($method,['GET','POST','DELETE'],true)) { throw new ApiError(405,'method_not_allowed'); }
        return $this->db->transaction(function() use($workspace,$session,$agent,$method,$body,$now): array {
            // Recheck session and agent inside the writer transaction, including concurrent revocation.
            if (!$this->db->row('SELECT id FROM mobile_sessions WHERE id=? AND workspace_id=? AND expires_at>?',[$session,$workspace,$now])) { throw new ApiError(401,'unauthorized'); }
            if (!$this->db->row('SELECT id FROM agents WHERE id=? AND workspace_id=? AND revoked_at IS NULL',[$agent,$workspace])) { throw new ApiError(404,'not_found'); }
            if ($method==='DELETE') {
                Validate::keys($body,[]);
                $this->db->query('DELETE FROM push_subscriptions WHERE session_id=? AND agent_id=?',[$session,$agent]);
                return ['enabled'=>false];
            }
            if ($method==='GET') {
                $row=$this->db->row('SELECT environment,kinds,updated_at FROM push_subscriptions WHERE session_id=? AND agent_id=?',[$session,$agent]);
                return ['enabled'=>$row!==null,'environment'=>$row['environment']??null,'kinds'=>isset($row['kinds'])?explode(',',$row['kinds']):[],
                    'available'=>['sandbox'=>$this->ready('sandbox'),'production'=>$this->ready('production')]];
            }
            Validate::keys($body,['device_token','environment','kinds']);
            $token=$body['device_token']??null;$env=$body['environment']??null;$kindString=$body['kinds']??null;
            // Device token length is variable; do not assume a fixed 32-byte token.
            if (!is_string($token)||!preg_match('/^[a-f0-9]{32,512}$/D',$token)||strlen($token)%2!==0) { throw new ApiError(422,'invalid_push_token'); }
            if (!in_array($env,['sandbox','production'],true)||!is_string($kindString)) { throw new ApiError(422,'invalid_push_preferences'); }
            $kinds=explode(',',$kindString);sort($kinds);
            if (!$kinds||count($kinds)>4||count(array_unique($kinds))!==count($kinds)||array_diff($kinds,self::KINDS)) { throw new ApiError(422,'invalid_push_preferences'); }
            if (!$this->ready($env)) { throw new ApiError(503,'push_not_configured'); }
            $kindString=implode(',',$kinds);
            $old=$this->db->row('SELECT * FROM push_subscriptions WHERE session_id=? AND agent_id=?',[$session,$agent]);
            // Avoid duplicate delivery when this same phone is linked again with a new session.
            $this->db->query('DELETE FROM push_subscriptions WHERE environment=? AND device_token=? AND agent_id=? AND session_id<>?',[$env,$token,$agent,$session]);
            $id=$old['id']??Service::uuid();
            $changed=!$old || $old['device_token']!==$token || $old['environment']!==$env || $old['kinds']!==$kindString;
            if ($changed) {
                $this->db->query('DELETE FROM push_jobs WHERE subscription_id=? AND state=?',[$id,'pending']);
                $this->db->query('INSERT INTO push_subscriptions VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(session_id,agent_id) DO UPDATE SET device_token=excluded.device_token,environment=excluded.environment,kinds=excluded.kinds,enabled_since=excluded.enabled_since,updated_at=excluded.updated_at,revision=excluded.revision',
                    [$id,$session,$agent,$token,$env,$kindString,$now,$now,Service::uuid()]);
            } else { $this->db->query('UPDATE push_subscriptions SET updated_at=? WHERE id=?',[$now,$id]); }
            return ['enabled'=>true];
        });
    }
    /** Called within the ingestion transaction. No network calls or hardware commands here. */
    public function enqueue(string $agent,array $event,?array $reading,int $now): void {
        if (!in_array($event['kind'],self::KINDS,true)||!$reading) { return; }
        $chargeEvidence=$event['kind']==='low_battery'?Validate::lowBatteryEvidence($reading,$event):null;
        if ($event['kind']==='low_battery'&&!$chargeEvidence) { return; }
        [, $observed]=Validate::timestamp($event['observed_at']);
        // Imported history is preserved, but it must not trigger old or future alerts.
        if ($observed<$now-600||$observed>$now+30) { return; }
        $rows=$this->db->query('SELECT p.* FROM push_subscriptions p JOIN mobile_sessions s ON s.id=p.session_id WHERE p.agent_id=? AND s.expires_at>? AND p.enabled_since<=?',[$agent,$now,$observed])->fetchAll();
        $titles=['on_battery'=>'Switched to Battery','power_restored'=>'Power Restored','low_battery'=>'Low Battery','voltage_drop'=>'Input Voltage Drop'];
        $bodies=['on_battery'=>'Utility power was lost. Your UPS is running on battery.','power_restored'=>'Utility power is available again.','low_battery'=>'Your UPS reported a low battery condition.','voltage_drop'=>'A significant input voltage drop was detected.'];
        foreach ($rows as $p) {
            if (!in_array($event['kind'],explode(',',$p['kinds']),true)) { continue; }
            $body=$bodies[$event['kind']];
            if ($chargeEvidence) { $body='Battery charge is '.$chargeEvidence['charge'].'% (low threshold '.$chargeEvidence['threshold'].'%).'; }
            if ($event['timing_uncertain']??false) { $body.=' Observed after a monitoring gap; exact timing is unknown.'; }
            $name=$reading['ups_key'];
            $payload=['aps'=>['alert'=>['title'=>$name.' · '.$titles[$event['kind']],'body'=>$body],'sound'=>'default'],
                'agent_id'=>$agent,'device_id'=>$event['device_id'],'event_id'=>$event['id'],'kind'=>$event['kind'],'observed_at'=>$event['observed_at']];
            if ($chargeEvidence) { $payload['battery_charge']=$chargeEvidence['charge'];$payload['charge_threshold']=$chargeEvidence['threshold']; }
            $this->db->query('INSERT OR IGNORE INTO push_jobs(id,subscription_id,event_id,payload,created_at,expires_at,next_attempt) VALUES(?,?,?,?,?,?,?)',
                [Service::uuid(),$p['id'],$event['id'],Service::json($payload),$now,$observed+600,$now]);
        }
    }
    /** Bounded worker; leases prevent overlapping agent requests from sending the same job. */
    public function drain(int $now,?callable $transport=null): array {
        $sent=0;$failed=0;$start=microtime(true);
        for ($i=0;$i<4 && microtime(true)-$start<3;$i++) {
            $lease=Service::uuid();
            $job=$this->db->transaction(function() use($now,$lease): ?array {
                $this->db->query("UPDATE push_jobs SET state='expired' WHERE state='pending' AND expires_at<=?",[$now]);
                $r=$this->db->row("SELECT j.*,p.device_token,p.environment,p.revision,p.updated_at FROM push_jobs j JOIN push_subscriptions p ON p.id=j.subscription_id JOIN mobile_sessions s ON s.id=p.session_id JOIN agents a ON a.id=p.agent_id WHERE j.state='pending' AND j.next_attempt<=? AND j.lease_until<=? AND s.expires_at>? AND a.revoked_at IS NULL ORDER BY j.next_attempt,j.created_at LIMIT 1",[$now,$now,$now]);
                if (!$r) { return null; }
                $this->db->query('UPDATE push_jobs SET lease=?,lease_until=?,attempts=attempts+1 WHERE id=?',[$lease,$now+60,$r['id']]);return $r;
            });
            if (!$job) { break; }
            try { $response=$transport ? $transport($job) : $this->send($job,$now); }
            catch (\Throwable) { $response=['status'=>0,'reason'=>'transport_unavailable']; }
            $status=$response['status'];$reason=$response['reason']??'';
            $this->db->transaction(function() use($job,$lease,$status,$reason,$response,$now,&$sent,&$failed): void {
                // Registration may have rotated or been deleted while APNs was contacted.
                if (!$this->db->row('SELECT id FROM push_subscriptions WHERE id=? AND revision=?',[$job['subscription_id'],$job['revision']])) { return; }
                if ($status===200) { $state='sent';$sent++; }
                elseif (($status===410 && ($response['timestamp']??PHP_INT_MAX)>=$job['updated_at']*1000) || ($status===400 && in_array($reason,['BadDeviceToken','DeviceTokenNotForTopic'],true))) {
                    $this->db->query('DELETE FROM push_subscriptions WHERE id=? AND revision=?',[$job['subscription_id'],$job['revision']]);$failed++;return;
                } elseif ($status===0||$status===429||$status>=500||$status===403) { $state='pending';$failed++; }
                else { $state='failed';$failed++; }
                $delay=min(300,30*(2**min(4,(int)$job['attempts'])));
                // Keep only known reason codes; never persist arbitrary remote response text.
                $safe=preg_match('/^[A-Za-z_]{1,80}$/D',$reason)?$reason:'delivery_error';
                $this->db->query('UPDATE push_jobs SET state=?,last_reason=?,next_attempt=?,lease=NULL,lease_until=0 WHERE id=? AND lease=?',[$state,$safe,$now+$delay,$job['id'],$lease]);
            });
        }
        return ['sent'=>$sent,'deferred_or_failed'=>$failed];
    }
    private function send(array $job,int $now): array {
        if (!$this->ready($job['environment'])) { return ['status'=>0,'reason'=>'push_not_configured']; }
        $jwt=$this->providerToken($job['environment'],$now);
        $host=$job['environment']==='sandbox'?'https://api.sandbox.push.apple.com':'https://api.push.apple.com';
        $curl=curl_init($host.'/3/device/'.$job['device_token']);
        curl_setopt_array($curl,[CURLOPT_POST=>true,CURLOPT_POSTFIELDS=>$job['payload'],CURLOPT_RETURNTRANSFER=>true,
            CURLOPT_HTTP_VERSION=>CURL_HTTP_VERSION_2_0,CURLOPT_CONNECTTIMEOUT_MS=>800,CURLOPT_TIMEOUT_MS=>1200,
            CURLOPT_FOLLOWLOCATION=>false,CURLOPT_SSL_VERIFYPEER=>true,CURLOPT_SSL_VERIFYHOST=>2,
            CURLOPT_PROTOCOLS=>CURLPROTO_HTTPS,CURLOPT_HTTPHEADER=>['authorization: bearer '.$jwt,'content-type: application/json',
            'apns-topic: local.UPSMonitor','apns-push-type: alert','apns-priority: 10','apns-id: '.$job['id'],'apns-collapse-id: '.$job['id'],'apns-expiration: '.$job['expires_at']]]);
        $raw=curl_exec($curl);$status=(int)curl_getinfo($curl,CURLINFO_RESPONSE_CODE);curl_close($curl);
        $response=is_string($raw)?json_decode($raw,true):null;
        return ['status'=>$status,'reason'=>$response['reason']??'','timestamp'=>$response['timestamp']??null];
    }
    private function providerToken(string $environment,int $now): string {
        $c=$this->config['apns'];$key=$c[$environment];
        $directory=$this->config['storage_dir'].'/apns';
        if (!is_dir($directory) && !mkdir($directory,0700,true)) { throw new \RuntimeException('push_key_unavailable'); }
        $lock=fopen($directory.'/jwt-'.$environment.'.lock','c');
        if (!$lock||!flock($lock,LOCK_EX)) { throw new \RuntimeException('push_key_unavailable'); }
        try {
            $path=$directory.'/jwt-'.$environment.'.json';$cached=is_file($path)?json_decode(file_get_contents($path),true):null;
            $identity=hash('sha256',$c['team_id'].$key['key_id'].hash_file('sha256',$key['key_path']));
            if (($cached['identity']??'')===$identity && ($cached['iat']??0)>$now-2700 && ($cached['iat']??0)<=$now) { return $cached['token']; }
            $encode=static fn(string $s): string=>rtrim(strtr(base64_encode($s),'+/','-_'),'=');
            $unsigned=$encode(Service::json(['alg'=>'ES256','kid'=>$key['key_id']])).'.'.$encode(Service::json(['iss'=>$c['team_id'],'iat'=>$now]));
            $private=openssl_pkey_get_private(file_get_contents($key['key_path']));
            $details=$private?openssl_pkey_get_details($private):false;
            if (!$details||($details['ec']['curve_name']??'')!=='prime256v1'||!openssl_sign($unsigned,$der,$private,OPENSSL_ALGO_SHA256)) { throw new \RuntimeException('push_key_unavailable'); }
            // OpenSSL returns ASN.1 DER; APNs ES256 requires the 64-byte JOSE R || S signature.
            if (strlen($der)<8||ord($der[0])!==48||ord($der[2])!==2) { throw new \RuntimeException('push_signature_invalid'); }
            $rlen=ord($der[3]);$r=substr($der,4,$rlen);$offset=4+$rlen;
            if (ord($der[$offset])!==2) { throw new \RuntimeException('push_signature_invalid'); }
            $s=substr($der,$offset+2,ord($der[$offset+1]));
            $r=ltrim($r,"\0");$s=ltrim($s,"\0");
            if (strlen($r)>32||strlen($s)>32) { throw new \RuntimeException('push_signature_invalid'); }
            $jwt=$unsigned.'.'.$encode(str_pad($r,32,"\0",STR_PAD_LEFT).str_pad($s,32,"\0",STR_PAD_LEFT));
            $temp=$path.'.'.bin2hex(random_bytes(8));
            file_put_contents($temp,Service::json(['identity'=>$identity,'iat'=>$now,'token'=>$jwt]),LOCK_EX);chmod($temp,0600);rename($temp,$path);
            return $jwt;
        } finally { flock($lock,LOCK_UN);fclose($lock); }
    }
}
