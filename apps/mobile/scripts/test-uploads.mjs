import { mkdtempSync, writeFileSync, rmSync } from 'node:fs';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { execFileSync, spawnSync } from 'node:child_process';

// Compile only the platform-independent API/engine, then use Node's built-in
// runner. No Expo runtime, AWS credentials, provider calls or new test framework.
const require = createRequire(import.meta.url);
const temp = mkdtempSync(join(tmpdir(), 'mobile-upload-tests-'));
try {
  execFileSync(process.execPath, [fileURLToPath(new URL('./generate-api-types.mjs', import.meta.url)), '--check'], { stdio: 'inherit' });
  const root = fileURLToPath(new URL('../src/lib/', import.meta.url));
  const out = join(temp, 'build');
  const config = join(temp, 'tsconfig.json');
  writeFileSync(config, JSON.stringify({
    compilerOptions: { target: 'ES2022', module: 'Node16', moduleResolution: 'Node16', strict: true,
      skipLibCheck: true, types: [], lib: ['ES2022', 'DOM'], rootDir: root, outDir: out },
    files: [join(root, 'api/uploads.ts'), join(root, 'uploads/upload-video.ts')],
  }));
  execFileSync(process.execPath, [require.resolve('typescript/bin/tsc'), '-p', config], { stdio: 'inherit' });
  const tests = spawnSync(process.execPath, ['--test', fileURLToPath(new URL('../tests/uploads.test.cjs', import.meta.url))], {
    stdio: 'inherit', env: { ...process.env, UPLOAD_TEST_BUILD_DIR: out },
  });
  process.exitCode = tests.status ?? 1;
} finally { rmSync(temp, { recursive: true, force: true }); }
