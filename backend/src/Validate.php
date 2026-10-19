<?php
declare(strict_types=1);
namespace UPS;

final class Validate {
    public const VARIABLES = ['ups.status','battery.charge','battery.charge.low','battery.charge.warning','ups.load','battery.runtime','battery.voltage','battery.voltage.nominal','input.voltage','input.voltage.nominal','output.voltage','ups.realpower','ups.realpower.nominal','device.mfr','device.model','ups.mfr','ups.model','ups.temperature'];
    public const EVENTS = ['on_battery','power_restored','low_battery','voltage_drop','nut_server_unavailable','nut_server_reconnected'];

    public static function text(mixed $value, int $max = 200): string {
        if (!is_string($value) || $value === '' || strlen($value)>$max || preg_match('/[\x00-\x1F\x7F]/', $value)) { throw new ApiError(422,'invalid_text'); }
        return $value;
    }
    public static function id(mixed $id): string {
        if (!is_string($id) || !preg_match('/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/D', $id)) { throw new ApiError(422,'invalid_id'); }
        return $id;
    }
    public static function hash(mixed $id): string {
        if (!is_string($id) || !preg_match('/^[0-9a-f]{64}$/D', $id)) { throw new ApiError(422,'invalid_device_or_source'); }
        return $id;
    }
    public static function integer(mixed $value, int $min, int $max): int {
        if (!is_int($value) || $value<$min || $value>$max) { throw new ApiError(422,'invalid_number'); } return $value;
    }
    public static function number(mixed $value, float $max = 1.0e12): float|int {
        if ((!is_float($value) && !is_int($value)) || !is_finite((float)$value) || $value<0 || $value>$max) { throw new ApiError(422,'invalid_number'); } return $value;
    }
    public static function boolean(mixed $value): bool { if (!is_bool($value)) { throw new ApiError(422,'invalid_boolean'); } return $value; }
    public static function keys(mixed $object, array $allowed): array {
        if (!is_array($object) || array_diff(array_keys($object), $allowed)) { throw new ApiError(422,'unexpected_fields'); } return $object;
    }
    public static function list(mixed $items, int $max): array {
        if ($items === null) { return []; }
        if (!is_array($items) || !array_is_list($items) || count($items)>$max) { throw new ApiError(422,'invalid_list'); } return $items;
    }
    public static function timestamp(mixed $value): array {
        if (!is_string($value) || !preg_match('/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/D',$value)) { throw new ApiError(422,'invalid_timestamp'); }
        $input=preg_replace('/(\.\d{6})\d+/', '$1', $value);
        try { $date=new \DateTimeImmutable($input); $errors=\DateTimeImmutable::getLastErrors(); } catch (\Throwable) { throw new ApiError(422,'invalid_timestamp'); }
        if ($errors && ($errors['warning_count'] || $errors['error_count'])) { throw new ApiError(422,'invalid_timestamp'); }
        $seconds=$date->getTimestamp(); if ($seconds<0 || $seconds>4102444800) { throw new ApiError(422,'invalid_timestamp'); }
        return [$value,$seconds];
    }
    public static function canonical(mixed $value): string {
        $sort=function(mixed $v) use (&$sort): mixed { if (is_array($v)) { if (!array_is_list($v)) { ksort($v,SORT_STRING); } return array_map($sort,$v); } return $v; };
        return json_encode($sort($value), JSON_THROW_ON_ERROR|JSON_UNESCAPED_SLASHES|JSON_UNESCAPED_UNICODE|JSON_PRESERVE_ZERO_FRACTION);
    }
    public static function reading(mixed $input): array {
        $r=self::keys($input,['device_id','source_id','ups_key','observed_at','variables','power_watts','power_estimated']);
        self::hash($r['device_id']??null); self::hash($r['source_id']??null); self::text($r['ups_key']??null);
        self::timestamp($r['observed_at']??null);
        $vars=self::keys($r['variables']??null,self::VARIABLES);
        foreach ($vars as $value) { if (!is_string($value) || strlen($value)>256 || preg_match('/[\x00-\x1F\x7F]/',$value)) { throw new ApiError(422,'invalid_reading'); } }
        if (array_key_exists('power_watts',$r)) { self::number($r['power_watts']); }
        self::boolean($r['power_estimated']??null);
        // Do not trust a remotely supplied estimate. Recompute using the same optional variables.
        unset($r['power_watts']); $r['power_estimated']=false;
        $numeric=static function(string $key) use ($vars): ?float { $v=$vars[$key]??''; if (!is_numeric($v)) { return null; } $n=(float)$v; return is_finite($n)&&$n>=0 ? $n : null; };
        if (($watts=$numeric('ups.realpower'))!==null) { $r['power_watts']=$watts; }
        elseif (($nominal=$numeric('ups.realpower.nominal'))!==null && $nominal>0 && ($load=$numeric('ups.load'))!==null && $load<=100 && is_finite($nominal*$load/100)) { $r['power_watts']=$nominal*$load/100; $r['power_estimated']=true; }
        return $r;
    }

    public static function message(mixed $input, string $agent): array {
        $m=self::keys($input,['schema_version','agent_id','id','sequence','recorded_at','sources','readings','last_known_readings','events','dropped_messages_total']);
        if (($m['schema_version']??null)!==1 || ($m['agent_id']??null)!==$agent) { throw new ApiError(422,'agent_or_schema_mismatch'); }
        self::id($m['id']??null); self::integer($m['sequence']??null,1,PHP_INT_MAX); self::timestamp($m['recorded_at']??null);
        self::integer($m['dropped_messages_total']??null,0,PHP_INT_MAX);
        $sources=[];
        foreach (self::list($m['sources']??null,16) as $source) {
            self::keys($source,['source_id','name','connection','error_code','discovered_ups','unavailable_ups']);
            $id=self::hash($source['source_id']??null); if (isset($sources[$id])) { throw new ApiError(422,'duplicate_source'); }
            if (!in_array($source['connection']??null,['reachable','unavailable','read_error'],true)) { throw new ApiError(422,'invalid_connection'); }
            if (isset($source['name'])) { self::text($source['name']); }
            if (isset($source['error_code']) && !in_array($source['error_code'],['unavailable','credentials_unavailable','authentication_failed','authentication_required','read_error','invalid_response','response_too_large'],true)) { throw new ApiError(422,'invalid_error_code'); }
            foreach (['discovered_ups','unavailable_ups'] as $field) { $keys=self::list($source[$field]??null,64); foreach($keys as $key){self::text($key);} if(count(array_unique($keys))!==count($keys)){throw new ApiError(422,'duplicate_ups');} }
            $sources[$id]=$source;
        }
        if (!$sources) { throw new ApiError(422,'missing_sources'); }
        $m['sources']=array_values($sources); $devices=[];
        foreach (['readings','last_known_readings'] as $field) {
            $output=[];
            foreach (self::list($m[$field]??null,1024) as $inputReading) {
                $r=self::reading($inputReading); $id=$r['device_id'];
                if (!isset($sources[$r['source_id']]) || isset($devices[$id])) { throw new ApiError(422,'invalid_or_duplicate_device'); }
                if ($field==='readings' && $sources[$r['source_id']]['connection']!=='reachable') { throw new ApiError(422,'fresh_reading_on_unavailable_source'); }
                $devices[$id]=$r['source_id']; $output[]=$r;
            }
            $m[$field]=$output;
        }
        if (count($devices)>1024) { throw new ApiError(422,'too_many_devices'); }
        $events=[];
        foreach (self::list($m['events']??null,4096) as $e) {
            self::keys($e,['id','kind','source_id','device_id','observed_at','disconnected_seconds','timing_uncertain']);
            $id=self::id($e['id']??null); $source=self::hash($e['source_id']??null); self::timestamp($e['observed_at']??null);
            if (isset($events[$id]) || !isset($sources[$source]) || !in_array($e['kind']??null,self::EVENTS,true)) { throw new ApiError(422,'invalid_event'); }
            if (str_starts_with($e['kind'],'nut_server_')) { if (isset($e['device_id'])) { throw new ApiError(422,'invalid_event_device'); } }
            else { $device=self::hash($e['device_id']??null); if (($devices[$device]??null)!==$source) { throw new ApiError(422,'invalid_event_device'); } }
            if (isset($e['disconnected_seconds'])) { self::number($e['disconnected_seconds'],315576000); }
            if (isset($e['timing_uncertain'])) { self::boolean($e['timing_uncertain']); }
            $events[$id]=$e;
        }
        $m['events']=array_values($events);
        return $m;
    }
}
