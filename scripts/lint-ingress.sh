#!/usr/bin/env bash
# P7: the live context receives only events returned by Session.Record's writer.
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
assert record.index('redact.Payload(') < record.index('s.logw.Append(') < record.index('s.events = append(')
assert 'redact.JSON(raw, nil)' in Path('internal/log/log.go').read_text()
print('ingress boundary ok')
PY
