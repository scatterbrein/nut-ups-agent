<?php
declare(strict_types=1);
namespace UPS;

final class ApiError extends \RuntimeException {
    public function __construct(public readonly int $status, public readonly string $errorCode) { parent::__construct($errorCode); }
}

final class Database {
    public readonly \PDO $pdo;
    private array $statements = [];

    public function __construct(string $directory) {
        umask(0077);
        if (!is_dir($directory) && !mkdir($directory, 0700, true) && !is_dir($directory)) { throw new \RuntimeException('storage_unavailable'); }
        if (is_link($directory) || !is_writable($directory)) { throw new \RuntimeException('storage_unavailable'); }
        $this->pdo = new \PDO('sqlite:' . $directory . '/monitor.sqlite', null, null, [\PDO::ATTR_ERRMODE => \PDO::ERRMODE_EXCEPTION, \PDO::ATTR_DEFAULT_FETCH_MODE => \PDO::FETCH_ASSOC]);
        $this->pdo->exec('PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000; PRAGMA synchronous=FULL;');
        $version = (int)$this->pdo->query('PRAGMA user_version')->fetchColumn();
        if ($version > 4) { throw new \RuntimeException('unsupported_schema'); }
        if ($version < 3) {
            $lock = fopen($directory . '/schema.lock', 'c');
            if (!$lock || !flock($lock, LOCK_EX)) { throw new \RuntimeException('storage_unavailable'); }
            try {
                if ((int)$this->pdo->query('PRAGMA user_version')->fetchColumn() < 3) {
                    if ((int)$this->pdo->query('PRAGMA user_version')->fetchColumn() > 0 && !file_exists($directory.'/monitor-before-v3.sqlite')) {
                        $this->pdo->exec('VACUUM INTO '.$this->pdo->quote($directory.'/monitor-before-v3.sqlite'));
                    }
                    $this->pdo->beginTransaction();
                    if ((int)$this->pdo->query('PRAGMA user_version')->fetchColumn() === 0) {
                        $this->pdo->exec(file_get_contents(__DIR__ . '/schema.sql'));
                    }
                    if ((int)$this->pdo->query('PRAGMA user_version')->fetchColumn() < 2) { $this->pdo->exec('CREATE TABLE mobile_pairings(code_hash TEXT PRIMARY KEY,workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,expires_at INTEGER NOT NULL,used_at INTEGER); CREATE TABLE mobile_sessions(id TEXT PRIMARY KEY,workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,token_hash TEXT NOT NULL UNIQUE,created_at INTEGER NOT NULL,expires_at INTEGER NOT NULL); CREATE INDEX mobile_owner ON mobile_sessions(workspace_id); PRAGMA user_version=2;'); }
                    $this->pdo->exec(file_get_contents(__DIR__ . '/push-schema.sql'));
                    $this->pdo->exec('PRAGMA user_version=3;');
                    $this->pdo->commit();
                }
            } catch (\Throwable $error) {
                if ($this->pdo->inTransaction()) { $this->pdo->rollBack(); }
                throw $error;
            } finally { flock($lock, LOCK_UN); fclose($lock); }
        }
        if ((int)$this->pdo->query('PRAGMA user_version')->fetchColumn() < 4) {
            $lock=fopen($directory.'/schema.lock','c');
            if (!$lock||!flock($lock,LOCK_EX)) { throw new \RuntimeException('storage_unavailable'); }
            try {
                if ((int)$this->pdo->query('PRAGMA user_version')->fetchColumn()<4) {
                    if (!file_exists($directory.'/monitor-before-v4.sqlite')) { $this->pdo->exec('VACUUM INTO '.$this->pdo->quote($directory.'/monitor-before-v4.sqlite')); }
                    $this->pdo->beginTransaction();
                    $this->pdo->exec(file_get_contents(__DIR__ . '/onboarding-schema.sql'));
                    $this->pdo->exec('PRAGMA user_version=4;');$this->pdo->commit();
                }
            } catch (\Throwable $error) { if ($this->pdo->inTransaction()) { $this->pdo->rollBack(); } throw $error; }
            finally { flock($lock,LOCK_UN);fclose($lock); }
        }
    }

    public function query(string $sql, array $params = []): \PDOStatement {
        $statement = $this->statements[$sql] ??= $this->pdo->prepare($sql);
        $statement->closeCursor();
        $statement->execute($params);
        return $statement;
    }
    public function row(string $sql, array $params = []): ?array { return $this->query($sql, $params)->fetch() ?: null; }

    public function transaction(callable $action): mixed {
        $this->pdo->beginTransaction();
        try {
            // Acquire the writer lock before reading mutable authentication, pairing or receipt state.
            $this->pdo->exec("UPDATE meta SET value=value WHERE key='schema'");
            $result = $action();
            $this->pdo->commit();
            return $result;
        } catch (\Throwable $error) {
            if ($this->pdo->inTransaction()) { $this->pdo->rollBack(); }
            throw $error;
        }
    }

    public function rate(string $key, int $limit, int $now, int $window = 60): void {
        $start = intdiv($now, $window) * $window;
        $this->query('INSERT INTO rates(key,window,count) VALUES(?,?,1) ON CONFLICT(key,window) DO UPDATE SET count=count+1', [$key, $start]);
        if ((int)$this->row('SELECT count FROM rates WHERE key=? AND window=?', [$key,$start])['count'] > $limit) { throw new ApiError(429, 'rate_limited'); }
    }
    public function cleanup(int $now): void {
        $last = (int)($this->row("SELECT value FROM meta WHERE key='cleanup_at'")['value'] ?? 0);
        if ($now - $last < 60) { return; }
        foreach (['samples'=>'observed_at','events'=>'observed_at','messages'=>'received_at'] as $table=>$column) {
            $this->query("DELETE FROM $table WHERE rowid IN (SELECT rowid FROM $table WHERE $column < ? LIMIT 10000)", [$now - 7*86400]);
        }
        $this->query('DELETE FROM enrollments WHERE claimed_at IS NULL AND expires_at < ?', [$now]);
        $this->query('DELETE FROM pairings WHERE expires_at < ?', [$now-86400]);
        $this->query('DELETE FROM mobile_pairings WHERE expires_at < ?', [$now-86400]);
        $this->query('DELETE FROM mobile_sessions WHERE expires_at < ?', [$now-86400]);
        $this->query('DELETE FROM push_jobs WHERE created_at < ?', [$now-7*86400]);
        $this->query('DELETE FROM rates WHERE window < ?', [$now-86400]);
        $this->query("INSERT INTO meta(key,value) VALUES('cleanup_at',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", [(string)$now]);
    }
}
