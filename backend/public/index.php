<?php
declare(strict_types=1);

ini_set('display_errors','0');
ini_set('session.use_cookies','0');
header_remove('X-Powered-By');
header('Content-Type: application/json; charset=utf-8');
header('Cache-Control: private, no-store, max-age=0');
header('Pragma: no-cache');
header('X-Content-Type-Options: nosniff');
header('X-Frame-Options: DENY');
header('Referrer-Policy: no-referrer');
header('X-Robots-Tag: noindex, nofollow');
set_time_limit(15);
$requestID=bin2hex(random_bytes(8));

try {
    require_once dirname(__DIR__).'/src/App.php';
    set_error_handler(static function(int $severity): never { throw new \RuntimeException('runtime_warning'); });
    $local=dirname(__DIR__).'/config/local.php';
    $config=is_file($local)?require $local:[];
    $config+=['storage_dir'=>dirname(__DIR__,4).'/.ups-monitor-private','bootstrap_hash'=>'','public_url'=>'https://ups.omstudioit.com/api/v1'];
    // Environment overrides are for local CLI tests and operator configuration, never request parameters.
    if (PHP_SAPI==='cli-server') {
        if (($directory=getenv('UPS_STORAGE_DIR'))!==false) { $config['storage_dir']=$directory; }
        if (($hash=getenv('UPS_BOOTSTRAP_HASH'))!==false) { $config['bootstrap_hash']=$hash; }
        if (($url=getenv('UPS_PUBLIC_URL'))!==false) { $config['public_url']=$url; }
    }
    $https=($_SERVER['HTTPS']??'')==='on'||($_SERVER['SERVER_PORT']??'')==='443';
    $loopback=in_array($_SERVER['REMOTE_ADDR']??'',['127.0.0.1','::1'],true);
    if (!$https && !(PHP_SAPI==='cli-server' && $loopback && getenv('UPS_ALLOW_LOOPBACK_HTTP')==='1')) { throw new \UPS\ApiError(426,'https_required'); }
    $method=$_SERVER['REQUEST_METHOD']??'';
    $path=parse_url($_SERVER['REQUEST_URI']??'',PHP_URL_PATH);
    $prefix=parse_url($config['public_url'],PHP_URL_PATH);
    if (!is_string($path)||!is_string($prefix)||!str_starts_with($path,$prefix.'/')||strlen($path)>256) { throw new \UPS\ApiError(404,'not_found'); }
    $path=substr($path,strlen($prefix));
    if (!in_array($method,['GET','POST','DELETE'],true)) { throw new \UPS\ApiError(405,'method_not_allowed'); }
    $authorization=$_SERVER['HTTP_AUTHORIZATION']??$_SERVER['REDIRECT_HTTP_AUTHORIZATION']??'';
    $token=preg_match('/^Bearer ([A-Za-z0-9_-]{32,128})$/D',$authorization,$m)?$m[1]:'';
    $body=[];
    if (in_array($method,['POST','DELETE'],true)) {
        if (strtolower(trim(explode(';',$_SERVER['CONTENT_TYPE']??'')[0]))!=='application/json') { throw new \UPS\ApiError(415,'json_required'); }
        if ((int)($_SERVER['CONTENT_LENGTH']??0)>8*1024*1024) { throw new \UPS\ApiError(413,'request_too_large'); }
        $raw=file_get_contents('php://input',false,null,0,8*1024*1024+1);
        if ($raw===false||strlen($raw)>8*1024*1024) { throw new \UPS\ApiError(413,'request_too_large'); }
        try { $body=json_decode($raw,true,32,JSON_THROW_ON_ERROR); }
        catch (\JsonException) { throw new \UPS\ApiError(400,'invalid_json'); }
        if (!is_array($body)||($body!==[]&&array_is_list($body))) { throw new \UPS\ApiError(400,'json_object_required'); }
    }
    $app=new \UPS\App($config);
    $result=$app->handle($method,$path,$token,$body,$_GET,$_SERVER['REMOTE_ADDR']??'unknown',time());
    echo \UPS\Service::json($result);
    // A push failure never rolls back or changes the durable ingestion acknowledgement.
    if ($method==='POST' && $path==='/ingest') {
        if (function_exists('fastcgi_finish_request')) { fastcgi_finish_request(); }
        try { $app->service->push->drain(time()); } catch (\Throwable) { error_log('UPS push worker unavailable'); }
    }
} catch (\Throwable $error) {
    $status=$error instanceof \UPS\ApiError?$error->status:503;
    $code=$error instanceof \UPS\ApiError?$error->errorCode:'service_unavailable';
    http_response_code($status);
    if ($status===429) { header('Retry-After: 60'); }
    if ($status===503) { header('Retry-After: 5');error_log('UPS API internal error; request '.$requestID); }
    echo json_encode(['error'=>['code'=>$code],'request_id'=>$requestID]);
}
