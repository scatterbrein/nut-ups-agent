#!/usr/bin/env python3
"""Build a self-contained PHP entry point; no secrets or runtime state enter this artifact."""
from pathlib import Path
import base64
import re

root=Path(__file__).resolve().parents[1]
target=root/'dist'
target.mkdir(exist_ok=True)
parts=[]
for name in ['Database.php','Validate.php','Service.php','App.php']:
    source=(root/'src'/name).read_text()
    source=source.replace('<?php','').replace('declare(strict_types=1);','').replace('namespace UPS;','')
    source=re.sub(r'^require_once .*?;\s*$', '', source, flags=re.M)
    if name=='Database.php':
        encoded=base64.b64encode((root/'src/schema.sql').read_bytes()).decode()
        source=source.replace("file_get_contents(__DIR__ . '/schema.sql')", "base64_decode('"+encoded+"')")
    parts.append(source)
entry=(root/'public/index.php').read_text().replace('<?php','').replace('declare(strict_types=1);','')
entry=entry.replace("require_once dirname(__DIR__).'/src/App.php';",'')
entry=entry.replace("dirname(__DIR__).'/config/local.php'", "__DIR__.'/ups-api-config.php'")
entry=entry.replace("dirname(__DIR__,4).'/.ups-monitor-private'", "dirname(__DIR__).'/.ups-monitor-private'")
(target/'ups-api.php').write_text('<?php\ndeclare(strict_types=1);\nnamespace UPS {\n'+'\n'.join(parts)+'\n}\nnamespace {\n'+entry+'\n}\n')
print(target/'ups-api.php')
