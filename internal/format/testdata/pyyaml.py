# Writes pyyaml.json: what PyYAML's SafeLoader, which NetBox's YAML import and
# devicetype-library's tests use, makes of each plain scalar, so that the
# reader is tested against PyYAML rather than against our reading of it.
# Run it with the PyYAML NetBox 4.7.2 ships, 6.0.x:
#
#   python3 -I internal/format/testdata/pyyaml.py > internal/format/testdata/pyyaml.json
import datetime
import json
import random
import yaml

CURATED = [
    'yes', 'Yes', 'YES', 'no', 'No', 'NO', 'true', 'True', 'TRUE', 'false', 'False', 'FALSE',
    'on', 'On', 'ON', 'off', 'Off', 'OFF', 'y', 'Y', 'n', 'N', 'yES', 'tRUE', 'oN',
    '~', 'null', 'Null', 'NULL', 'nULL', 'none', 'None',
    '0', '-0', '+0', '1', '-1', '+1', '007', '010', '08', '09', '0_7', '02351930', '0o10', '0b101', '-0b101',
    '0b_', '0x1F', '0x_1f', '-0x1F', '0x', '0xg', '1_000', '1__0', '_1', '1_', '1:30', '-1:30', '1:30:59',
    '1:60', '0:30', '190:20:30', '9223372036854775807', '-9223372036854775808', '9223372036854775808',
    '1.0', '1.', '-1.', '.5', '-.5', '+.5', '._5', '1._', '1.10', '12.21', '0.1', '1e3', '1E3', '1.0e3',
    '1.0e+3', '1.0E-3', '1.e+5', '6.8523015e+5', '685.230_15e+03', '685_230.15', '190:20:30.15',
    '1:30.5', '.inf', '-.inf', '+.Inf', '.INF', '.nan', '.NaN', '.NAN', 'inf', 'nan', '1.00000000000000001',
    '0.30000000000000004', '1' + '0' * 30 + '.0', '1' + '0' * 400 + '.0', '1.0e+400', '1.0e-400',
    '123456789012345678.9', '2024-01-01', '2001-12-14t21:59:43.10-05:00', '2001-12-14 21:59:43.10 -5',
    '2001-12-15T02:59:43.1Z', '2002-12-14', '2024-1-1', '20240101', '<<', '<', '=', '==',
    'model', 'Dell', 'R670', '1.0.0', '1-2', 'v1', '0x1.8p1', '1e', '1e+', '+', '-', '.', '..',
    '1_0.5', '1:30:', ':30', '1::30', '0:0', '1:5', '1:05', '1:5.5',
]

def answer(s):
    try:
        node = yaml.compose(s, Loader=yaml.SafeLoader)
    except yaml.YAMLError:
        return None
    if not isinstance(node, yaml.ScalarNode) or node.style is not None or node.value != s:
        return None
    try:
        v = yaml.safe_load(s)
    except Exception as e:  # PyYAML raises ValueError, not only YAMLError, for some.
        return {'scalar': s, 'error': type(e).__name__}
    if isinstance(v, bool):
        return {'scalar': s, 'tag': 'bool', 'value': 'true' if v else 'false'}
    if isinstance(v, int):
        if not -2**63 <= v < 2**63:
            return {'scalar': s, 'error': 'too large'}
        return {'scalar': s, 'tag': 'int', 'value': str(v)}
    if isinstance(v, float):
        if v != v or v in (float('inf'), float('-inf')):
            return {'scalar': s, 'error': 'not finite'}
        return {'scalar': s, 'tag': 'float', 'value': repr(v)}
    if isinstance(v, str):
        return {'scalar': s, 'tag': 'str', 'value': v}
    if v is None:
        return {'scalar': s, 'tag': 'null', 'value': ''}
    if isinstance(v, (datetime.date, datetime.datetime)):
        return {'scalar': s, 'error': 'timestamp'}
    return {'scalar': s, 'error': type(v).__name__}

rng = random.Random(9)
# Random scalars made of what PyYAML's numbers, booleans and nulls are made
# of. Most are text, so only one in ten of those is kept.
alphabet = '0123456789_.:+-eExXbBo~<=yYnNtTfFOinaI'
cases, seen = [], set()
randoms = [''.join(rng.choice(alphabet) for _ in range(rng.randint(1, 7))) for _ in range(20000)]
for i, s in enumerate(CURATED + randoms):
    if s in seen:
        continue
    seen.add(s)
    a = answer(s)
    if a is None or (i >= len(CURATED) and a.get('tag') == 'str' and rng.random() > 0.1):
        continue
    cases.append(a)
print('[\n' + ',\n'.join(json.dumps(c, ensure_ascii=True) for c in cases) + '\n]')
