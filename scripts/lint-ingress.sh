#!/usr/bin/env bash
# P7: live and restored history must pass redaction before the sole append point.
set -euo pipefail
python3 - <<'PY'
from pathlib import Path
import re
root = Path('internal')
writes = []
for p in root.rglob('*.go'):
    if p.name.endswith('_test.go'):
        continue
    for line in p.read_text().splitlines():
        if re.search(r'\.events\s*=|\.events\s*\[.*\]\s*=', line):
            writes.append((str(p), line.strip()))
assert writes == [('internal/core/events.go', 's.events = append(s.events, ev)')], writes
s = Path('internal/core/events.go').read_text()
record = s.split('func (s *Session) Record(',1)[1].split('\n}\n',1)[0]
assert record.index('redact.Payload(') < record.index('s.logw.Append(') < record.index('s.remember(')
restore = s.split('func (s *Session) restoreHistory(',1)[1].split('\n}\n',1)[0]
assert restore.index('redact.JSON(') < restore.index('s.remember(')
remember_calls = []
for p in root.rglob('*.go'):
    if not p.name.endswith('_test.go'):
        remember_calls.extend((str(p), line.strip()) for line in p.read_text().splitlines() if '.remember(' in line)
assert remember_calls == [('internal/core/events.go', 's.remember(ev)'), ('internal/core/events.go', 's.remember(ev)')], remember_calls
assert 'redact.JSON(raw, nil)'  in Path('internal/log/log.go').read_text()
print('ingress boundary ok')
PY
