<?php
declare(strict_types=1);
use UPS\{App,Service};

function pushFixture(): array {
    [$a,$owner,$now,$dir]=fixture();
    $private=openssl_pkey_new(['private_key_type'=>OPENSSL_KEYTYPE_EC,'curve_name'=>'prime256v1']);openssl_pkey_export($private,$pem);
    mkdir($dir.'/apns',0700);file_put_contents($dir.'/apns/test.p8',$pem);chmod($dir.'/apns/test.p8',0600);
    $config=['team_id'=>'TESTTEAM12','topic'=>'local.UPSMonitor'];
    foreach(['sandbox','production'] as $env){$config[$env]=['key_id'=>'TESTKEY123','key_path'=>$dir.'/apns/test.p8'];}
    file_put_contents($dir.'/apns/config.json',json_encode($config));
    $a=new App(['storage_dir'=>$dir,'public_url'=>'https://ups.example/api/v1']);$agent=paired($a,$owner['owner_token'],$now);upload($a,$agent,packet($agent['agent_id'],1,$now),$now);
    $p=$a->handle('POST','/mobile-pairings',$owner['owner_token'],[],[],'p',$now);
    $phone=$a->handle('POST','/mobile-pairings/claim','',['code'=>$p['pairing_code']],[],'p',$now);
    return [$a,$owner,$now,$dir,$agent,$phone];
}
function subscribePush(App $a,array $phone,array $agent,int $now,array $overrides=[]): array {
    return $a->handle('POST','/push-subscriptions/'.$agent['agent_id'],$phone['access_token'],$overrides+['device_token'=>str_repeat('a1',32),'environment'=>'sandbox','kinds'=>'on_battery,power_restored,low_battery,voltage_drop'],[],'push-peer',$now);
}
function pushEvent(App $a,array $agent,int $sequence,int $now,string $kind='on_battery',?int $observed=null): array {
    $p=packet($agent['agent_id'],$sequence,$now,'OB','87');
    $p['messages'][0]['events']=[['id'=>Service::uuid(),'kind'=>$kind,'source_id'=>str_repeat('a',64),'device_id'=>str_repeat('b',64),'observed_at'=>gmdate('Y-m-d\TH:i:s\Z',$observed??$now)]];
    upload($a,$agent,$p,$now);return $p;
}

test('low battery events and push require percentage evidence, including legacy agents',function(){
    [$a,$o,$n,$dir,$agent,$phone]=pushFixture();subscribePush($a,$phone,$agent,$n);
    $sequence=1;$accepted=0;
    foreach ([['100','10',false],['87','10',false],['','10',false],['NaN','10',false],['101','10',false],['9','10',true],['0','0',true]] as [$charge,$threshold,$expected]) {
        $sequence++;$now=$n+$sequence;
        $p=packet($agent['agent_id'],$sequence,$now,'OL LB',$charge);
        $p['messages'][0]['readings'][0]['variables']['battery.charge.low']=$threshold;
        $p['messages'][0]['readings'][0]['variables']['battery.runtime']='1';
        $event=['id'=>Service::uuid(),'kind'=>'low_battery','source_id'=>str_repeat('a',64),'device_id'=>str_repeat('b',64),'observed_at'=>gmdate('Y-m-d\TH:i:s\Z',$now)];
        $p['messages'][0]['events']=[$event];
        $receipt=upload($a,$agent,$p,$now);
        if($expected){$accepted++;}
        check(countRows($a,'events')===$accepted&&countRows($a,'push_jobs')===$accepted,'LB/runtime bypassed percentage policy');
        check(state($a,$o,$agent,$now)['last_sequence']===$sequence,'ignored event blocked ordinary telemetry');
    }
    $job=$a->service->db->row('SELECT payload FROM push_jobs ORDER BY created_at LIMIT 1');$payload=json_decode($job['payload'],true);
    check($payload['battery_charge']===9.0||$payload['battery_charge']===9,'missing percentage proof in push');
    check(str_contains($payload['aps']['alert']['body'],'9%')&&str_contains($payload['aps']['alert']['body'],'10%'),'low charge notification lacks actual percentage');
});
test('agent charge threshold fallback is accepted and inconsistent evidence is suppressed',function(){
    [$a,$o,$n,$dir,$agent,$phone]=pushFixture();subscribePush($a,$phone,$agent,$n);
    $p=packet($agent['agent_id'],2,$n+1,'OL','18');
    $event=['id'=>Service::uuid(),'kind'=>'low_battery','source_id'=>str_repeat('a',64),'device_id'=>str_repeat('b',64),'observed_at'=>gmdate('Y-m-d\TH:i:s\Z',$n+1),'battery_charge'=>18,'charge_threshold'=>20];
    $p['messages'][0]['events']=[$event];upload($a,$agent,$p,$n+1);
    check(countRows($a,'events')===1&&countRows($a,'push_jobs')===1,'generic configured percentage threshold was ignored');
    $p=packet($agent['agent_id'],3,$n+2,'OL LB','100');$event['id']=Service::uuid();$event['observed_at']=gmdate('Y-m-d\TH:i:s\Z',$n+2);
    $event['battery_charge']=9;$p['messages'][0]['events']=[$event];upload($a,$agent,$p,$n+2);
    check(countRows($a,'events')===1&&countRows($a,'push_jobs')===1,'event claimed a low percentage contrary to the reading');
    foreach(['battery_charge','charge_threshold'] as $field){$invalid=$p;$invalid['messages'][0]['events'][0][$field]=-1;fails(422,fn()=>upload($a,$agent,$invalid,$n+2));}
    $reading=$p['messages'][0]['readings'][0];$event['battery_charge']=100;
    $a->service->push->enqueue($agent['agent_id'],$event,$reading,$n+2);
    check(countRows($a,'push_jobs')===1,'direct enqueue bypassed the percentage policy');
});

test('push registration is opt-in and scoped to phone session and owned agent',function(){
    [$a,$o,$n,$dir,$agent,$phone]=pushFixture();$path='/push-subscriptions/'.$agent['agent_id'];
    check(!$a->handle('GET',$path,$phone['access_token'],[],[],'p',$n)['enabled'],'enabled without consent');
    fails(404,fn()=>$a->handle('POST',$path,$o['owner_token'],[],[],'p',$n));
    check(subscribePush($a,$phone,$agent,$n)['enabled'],'registration failed');
    $result=$a->handle('GET',$path,$phone['access_token'],[],[],'p',$n);
    check($result['enabled']&&!str_contains(json_encode($result),str_repeat('a1',32)),'token exposed by read API');
    $other=$a->handle('POST','/admin/workspaces',$o['admin_token'],['name'=>'Other'],[],'p',$n);$otherAgent=paired($a,$other['owner_token'],$n);
    fails(404,fn()=>subscribePush($a,$phone,$otherAgent,$n));
    fails(422,fn()=>subscribePush($a,$phone,$agent,$n,['kinds'=>'shutdown']));
    fails(422,fn()=>subscribePush($a,$phone,$agent,$n,['environment'=>'http://attacker']));
});
test('push only enqueues new subscribed events and deduplicates retry',function(){
    [$a,$o,$n,$dir,$agent,$phone]=pushFixture();subscribePush($a,$phone,$agent,$n,['kinds'=>'on_battery']);
    pushEvent($a,$agent,2,$n+1,'low_battery');pushEvent($a,$agent,3,$n+2,'on_battery',$n-1);
    pushEvent($a,$agent,4,$n+3,'on_battery',$n-1000);pushEvent($a,$agent,5,$n+4,'on_battery',$n+1000);
    check(countRows($a,'push_jobs')===0,'backlog or unchecked event notified');
    $p=pushEvent($a,$agent,6,$n+5);upload($a,$agent,$p,$n+6);
    check(countRows($a,'push_jobs')===1,'retry duplicate alert');
    $job=$a->service->db->row('SELECT * FROM push_jobs');$payload=json_decode($job['payload'],true);
    check($payload['device_id']===str_repeat('b',64)&&$payload['kind']==='on_battery','wrong routing');
    check(!str_contains($job['payload'],'device_token'),'token in notification');
});
test('push worker retries transient failures then stops after success',function(){
    [$a,$o,$n,$dir,$agent,$phone]=pushFixture();subscribePush($a,$phone,$agent,$n);pushEvent($a,$agent,2,$n+1);
    $calls=0;$a->service->push->drain($n+1,function($job)use(&$calls){$calls++;return ['status'=>503];});
    $a->service->push->drain($n+2,function($job)use(&$calls){$calls++;return ['status'=>200];});
    check($calls===1,'backoff ignored');
    $a->service->push->drain($n+32,function($job)use(&$calls){$calls++;return ['status'=>200];});
    $a->service->push->drain($n+100,function($job)use(&$calls){$calls++;return ['status'=>200];});
    check($calls===2&&$a->service->db->row('SELECT state FROM push_jobs')['state']==='sent','retry missing or repeated');
});
test('push expiry invalid token and session revocation stop delivery',function(){
    [$a,$o,$n,$dir,$agent,$phone]=pushFixture();subscribePush($a,$phone,$agent,$n);pushEvent($a,$agent,2,$n+1);
    $a->service->push->drain($n+602,function(){throw new RuntimeException('expired job sent');});
    check($a->service->db->row('SELECT state FROM push_jobs')['state']==='expired','expired retained');
    pushEvent($a,$agent,3,$n+603);
    $a->service->push->drain($n+603,fn()=>['status'=>410,'timestamp'=>($n+604)*1000]);
    check(countRows($a,'push_subscriptions')===0&&countRows($a,'push_jobs')===0,'inactive device retained');
    subscribePush($a,$phone,$agent,$n+605);pushEvent($a,$agent,4,$n+606);
    $a->handle('DELETE','/mobile-session',$phone['access_token'],[],[],'p',$n+607);
    check(countRows($a,'push_subscriptions')===0&&countRows($a,'push_jobs')===0,'revocation retained token');
});
test('push disabling rotation and concurrent leases preserve privacy and avoid duplicates',function(){
    [$a,$o,$n,$dir,$agent,$phone]=pushFixture();subscribePush($a,$phone,$agent,$n);pushEvent($a,$agent,2,$n+1);
    $calls=0;$a->service->push->drain($n+1,function()use($a,$n,&$calls){$calls++;$a->service->push->drain($n+1,function()use(&$calls){$calls++;return ['status'=>200];});return ['status'=>200];});
    check($calls===1&&$a->service->db->row('SELECT state FROM push_jobs')['state']==='sent','overlap sent twice or stole lease');
    pushEvent($a,$agent,3,$n+2);subscribePush($a,$phone,$agent,$n+3,['device_token'=>str_repeat('b2',32)]);
    check((int)$a->service->db->query("SELECT COUNT(*) FROM push_jobs WHERE state='pending'")->fetchColumn()===0,'old token job retained');
    pushEvent($a,$agent,4,$n+4);$a->handle('DELETE','/push-subscriptions/'.$agent['agent_id'],$phone['access_token'],[],[],'p',$n+5);
    check(countRows($a,'push_jobs')===0,'disable retained pending delivery');
});
test('APNs ES256 signature verifies and provider token is reused',function(){
    [$a,$o,$n,$dir]=pushFixture();$method=new ReflectionMethod($a->service->push,'providerToken');
    $jwt=$method->invoke($a->service->push,'sandbox',$n);$again=$method->invoke($a->service->push,'sandbox',$n+60);
    check($jwt===$again,'JWT regenerated on every request');$parts=explode('.',$jwt);
    $decode=static fn($s)=>base64_decode(strtr($s,'-_','+/'));
    $raw=$decode($parts[2]);check(strlen($raw)===64,'wrong JOSE signature length');
    $encode=static function($v){$v=ltrim($v,"\0");if(ord($v[0])>127){$v="\0".$v;}return "\2".chr(strlen($v)).$v;};
    $body=$encode(substr($raw,0,32)).$encode(substr($raw,32));$der="\x30".chr(strlen($body)).$body;
    $key=openssl_pkey_get_private(file_get_contents($dir.'/apns/test.p8'));$public=openssl_pkey_get_details($key)['key'];
    check(openssl_verify($parts[0].'.'.$parts[1],$der,$public,OPENSSL_ALGO_SHA256)===1,'invalid APNs ES256 signature');
    check((fileperms($dir.'/apns/jwt-sandbox.json')&0777)===0600,'JWT cache permissions');
});
test('schema two migrates with consistent backup and keeps mobile access',function(){
    [$a,$o,$n,$dir,$agent,$phone]=pushFixture();unset($a);$old=new PDO('sqlite:'.$dir.'/monitor.sqlite');
    $old->exec('DROP TABLE push_jobs; DROP TABLE push_subscriptions; PRAGMA user_version=2');unset($old);
    $a=new App(['storage_dir'=>$dir,'public_url'=>'https://ups.example/api/v1']);
    check(is_file($dir.'/monitor-before-v3.sqlite'),'no migration backup');
    check(count($a->handle('GET','/agents',$phone['access_token'],[],[],'p',$n)['agents'])===1,'phone access lost');
    check((int)$a->service->db->pdo->query('PRAGMA user_version')->fetchColumn()===4,'push migration missing');
});

test('push registration rotates safely and agent revocation removes tokens',function(){
    [$a,$o,$n,$dir,$agent,$phone]=pushFixture();subscribePush($a,$phone,$agent,$n);pushEvent($a,$agent,2,$n+1);
    $a->service->push->drain($n+1,function()use($a,$phone,$agent,$n){
        subscribePush($a,$phone,$agent,$n+2,['device_token'=>str_repeat('b2',32)]);
        return ['status'=>410,'timestamp'=>($n+3)*1000];
    });
    check(countRows($a,'push_subscriptions')===1,'stale response removed rotated token');
    $a->handle('POST','/agents/'.$agent['agent_id'].'/revoke',$o['owner_token'],[],[],'p',$n+4);
    check(countRows($a,'push_subscriptions')===0&&countRows($a,'push_jobs')===0,'revoked agent retained push subscription');
});
test('optional APNs setup failure does not interrupt ingestion',function(){
    [$a,$o,$n,$dir,$agent,$phone]=pushFixture();file_put_contents($dir.'/apns/config.json','invalid JSON');
    $a=new App(['storage_dir'=>$dir,'public_url'=>'https://ups.example/api/v1']);
    upload($a,$agent,packet($agent['agent_id'],2,$n+1),$n+1);
    check(state($a,$o,$agent,$n+1)['last_sequence']===2,'APNs configuration broke readings');
    fails(503,fn()=>subscribePush($a,$phone,$agent,$n+1));
});

test('APNs operator provisioning is admin-only and stores keys privately',function(){
    [$a,$o,$n,$dir,$agent,$phone]=pushFixture();$pem=file_get_contents($dir.'/apns/test.p8');
    $body=['team_id'=>'86D2TRN5L5','topic'=>'local.UPSMonitor','environment'=>'sandbox','key_id'=>'NEWKEY1234','private_key'=>$pem];
    foreach([$o['owner_token'],$phone['access_token'],$agent['agent_token']] as $token){
        fails(401,fn()=>$a->handle('POST','/admin/apns',$token,$body,[],'p',$n));
        fails(401,fn()=>$a->handle('GET','/admin/apns',$token,[],[],'p',$n));
        fails(401,fn()=>$a->handle('POST','/admin/apns/probe',$token,['environment'=>'sandbox'],[],'p',$n));
    }
    $result=$a->handle('POST','/admin/apns',$o['admin_token'],$body,[],'p',$n);
    check($result['configured']&&$result['ready'],'operator provisioning failed');
    $stored=json_decode(file_get_contents($dir.'/apns/config.json'),true);
    check($stored['production']['key_id']==='TESTKEY123','other environment overwritten');
    check((fileperms($stored['sandbox']['key_path'])&0777)===0600&&(fileperms($dir.'/apns')&0777)===0700,'private key permissions');
    $status=$a->handle('GET','/admin/apns',$o['admin_token'],[],[],'p',$n);
    check($status['environments']['sandbox']['key_id']==='NEWKEY1234'&&!str_contains(json_encode($status),'PRIVATE KEY')&&!str_contains(json_encode($status),'key_path'),'operator status exposes secret');
    $before=file_get_contents($dir.'/apns/config.json');$bad=$body;$bad['private_key']='-----BEGIN PRIVATE KEY-----invalid';
    fails(422,fn()=>$a->handle('POST','/admin/apns',$o['admin_token'],$bad,[],'p',$n));
    $bad=$body;$bad['team_id']='OTHERTEAM1';fails(422,fn()=>$a->handle('POST','/admin/apns',$o['admin_token'],$bad,[],'p',$n));
    check(file_get_contents($dir.'/apns/config.json')===$before,'invalid upload changed working key configuration');
    fails(422,fn()=>$a->handle('POST','/admin/apns/probe',$o['admin_token'],['environment'=>'http://attacker'],[],'p',$n));
});
