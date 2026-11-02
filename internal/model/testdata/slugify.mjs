// Writes slugify.json: what NetBox's own slugify, from its web UI, makes of
// each test name, so that model.Slugify is tested against NetBox rather than
// against our reading of it. Run it on the file `make reference` fetches:
//
//	node internal/model/testdata/slugify.mjs \
//	  .cache/reference/netbox-4.7.2/netbox/project-static/src/buttons/reslug.ts \
//	  > internal/model/testdata/slugify.json
import { readFileSync } from 'node:fs';

const source = readFileSync(process.argv[2], 'utf8');
const match = source.match(/function slugify\(slug: string, chars: number\): string \{[\s\S]*?\n\}/);
if (!match) {
  throw new Error('no slugify function in ' + process.argv[2]);
}
// Strip the TypeScript types, which are all the function has that isn't
// JavaScript.
const slugify = new Function(
  'return ' + match[0].replace('(slug: string, chars: number): string', '(slug, chars)'),
)();

const names = [
  'Dell', 'Hewlett Packard Enterprise', 'Rohde & Schwarz', 'Reichle & De-Massari', 'GL.iNet', 'GL-iNet',
  'NETGEAR', 'Netgear', 'Unipi technology', '3Com', 'AT&T', 'C++', 'A/B Networks', 'Foo (Bar)', '1.2.3',
  'Wireless-tag Technology', 'Schweitzer Engineering Laboratories', '360 Vision Technology',
  '  Padded  ', '\tTabbed\t', 'Line\nBreak', 'Foo.', '.Foo', '..Foo..', '-Foo-', '_Foo_', 'a--b', 'a - b',
  'a . b', 'a_b', 'a__b', 'a-_-b', '.', '...', '-', '_', '', '   ', '&', '&&&', 'Foo & & Bar',
  'Ünïcödé', 'Señor', '日本電気', 'Straße', 'İstanbul', 'ǅ',
  'A B', 'A B', 'A B', 'A B', 'A B', 'A B', 'A B', 'A B',
  'A　B', 'A﻿B', 'A​B', 'A\u0085B', 'A᠎B', 'A\u001cB', 'A\u000bB', 'A\u000cB',
  ' Lead', 'Trail　', '﻿.Dot.﻿', 'A😀B', 'A\u0000B', 'ÁB', 'é',
  'x'.repeat(49), 'x'.repeat(50), 'x'.repeat(51), 'y'.repeat(100), 'y'.repeat(101),
  'a'.repeat(49) + ' b', 'a'.repeat(49) + '..b', 'Ab'.repeat(30) + ' & ' + 'Cd'.repeat(30),
];
const cases = names.map((name) => ({ name, slug: slugify(name, 50) }));
cases.push(...['y'.repeat(101), 'a'.repeat(99) + ' b'].map((name) => ({ name, chars: 100, slug: slugify(name, 100) })));
process.stdout.write(JSON.stringify(cases, null, 2) + '\n');
